// Package modelbroker is the only inference bridge from
// the GjallarOS agent to the private Ollama API.
package modelbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	SocketPath string
	Upstream   string
	Model      string
	// UpstreamModel is the name the upstream knows Model by; empty means Model.
	UpstreamModel string
	// UpstreamToken is the bearer token for a central server. A non-loopback
	// upstream requires it and HTTPS.
	UpstreamToken        string
	AllowedUID           uint32
	RequiredCgroupPrefix string
	MaxRequestBytes      int64
	Timeout              time.Duration
}

type peerInfo struct {
	PID int
	UID uint32
}

type peerKey struct{}

func Serve(ctx context.Context, cfg Config) error {
	if cfg.SocketPath == "" {
		return errors.New("model broker socket path is required")
	}
	if cfg.Upstream == "" {
		cfg.Upstream = "http://127.0.0.1:11434"
	}
	if cfg.Model == "" {
		return errors.New("model broker model is required")
	}
	if err := checkUpstream(cfg.Upstream, cfg.UpstreamToken); err != nil {
		return err
	}
	if cfg.RequiredCgroupPrefix == "" {
		return errors.New("model broker cgroup prefix is required")
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = 16 << 20
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Minute
	}

	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0750); err != nil {
		return err
	}

	_ = os.Remove(cfg.SocketPath)

	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(cfg.SocketPath)

	if err := os.Chmod(cfg.SocketPath, 0660); err != nil {
		return err
	}

	if err := notifyReady(); err != nil {
		return err
	}

	server := &http.Server{
		Handler:           handler(cfg),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,

		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			p, err := unixPeer(conn)
			if err != nil {
				return ctx
			}

			return context.WithValue(ctx, peerKey{}, p)
		},
	}

	done := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			defer cancel()

			_ = server.Shutdown(shutdownCtx)

		case <-done:
		}
	}()

	err = server.Serve(ln)
	close(done)

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

// notifyReady tells systemd (Type=notify) that the socket takes
// connections, so units ordered after the broker never have to poll it.
// Without NOTIFY_SOCKET (started by hand, tests) it does nothing.
func notifyReady() error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}

	conn, err := net.DialUnix(
		"unixgram",
		nil,
		&net.UnixAddr{Name: addr, Net: "unixgram"},
	)
	if err != nil {
		return fmt.Errorf("notify systemd: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("READY=1")); err != nil {
		return fmt.Errorf("notify systemd: %w", err)
	}

	return nil
}

func handler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
			return
		}

		p, ok := r.Context().Value(peerKey{}).(peerInfo)

		if !ok || !authorizedPeer(p, cfg) {
			fmt.Fprintf(
				os.Stderr,
				"modelbroker deny pid=%d uid=%d path=%s reason=peer\n",
				p.PID,
				p.UID,
				r.URL.Path,
			)

			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		switch {
		case r.Method == http.MethodGet &&
			r.URL.Path == "/v1/models":

			w.Header().Set("Content-Type", "application/json")

			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data": []map[string]any{
					{
						"id":       cfg.Model,
						"object":   "model",
						"owned_by": "gjallaros",
					},
				},
			})

			return

		case r.Method == http.MethodPost &&
			(r.URL.Path == "/v1/chat/completions" ||
				r.URL.Path == "/v1/completions"):

		default:
			fmt.Fprintf(
				os.Stderr,
				"modelbroker deny pid=%d path=%s reason=endpoint\n",
				p.PID,
				r.URL.Path,
			)

			http.Error(
				w,
				"endpoint not permitted",
				http.StatusForbidden,
			)
			return
		}

		body := http.MaxBytesReader(
			w,
			r.Body,
			cfg.MaxRequestBytes,
		)

		raw, err := io.ReadAll(body)
		if err != nil {
			http.Error(
				w,
				"request too large or unreadable",
				http.StatusBadRequest,
			)
			return
		}

		raw, err = validateModel(raw, cfg.Model)
		if err == nil {
			raw, err = renameModel(raw, cfg.Model, cfg.UpstreamModel)
		}
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"modelbroker deny pid=%d path=%s reason=model\n",
				p.PID,
				r.URL.Path,
			)

			http.Error(
				w,
				err.Error(),
				http.StatusForbidden,
			)
			return
		}

		upstream := strings.TrimRight(
			cfg.Upstream,
			"/",
		) + r.URL.Path

		req, err := http.NewRequestWithContext(
			r.Context(),
			http.MethodPost,
			upstream,
			bytes.NewReader(raw),
		)
		if err != nil {
			http.Error(
				w,
				"upstream request creation failed",
				http.StatusInternalServerError,
			)
			return
		}

		req.Header.Set(
			"Content-Type",
			"application/json",
		)

		if cfg.UpstreamToken != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.UpstreamToken)
		}

		if accept := r.Header.Get("Accept"); accept != "" {
			req.Header.Set("Accept", accept)
		}

		client := &http.Client{
			Timeout: cfg.Timeout,
		}

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"modelbroker upstream pid=%d error=%v\n",
				p.PID,
				err,
			)

			http.Error(
				w,
				"model backend unavailable",
				http.StatusBadGateway,
			)
			return
		}

		defer resp.Body.Close()

		for k, values := range resp.Header {
			if hopHeader(k) {
				continue
			}

			for _, value := range values {
				w.Header().Add(k, value)
			}
		}

		w.WriteHeader(resp.StatusCode)

		buf := make([]byte, 32*1024)
		flusher, _ := w.(http.Flusher)

		for {
			n, readErr := resp.Body.Read(buf)

			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					return
				}

				if flusher != nil {
					flusher.Flush()
				}
			}

			if readErr == io.EOF {
				break
			}

			if readErr != nil {
				return
			}
		}

		fmt.Fprintf(
			os.Stderr,
			"modelbroker allow pid=%d path=%s status=%d\n",
			p.PID,
			r.URL.Path,
			resp.StatusCode,
		)
	})
}

func gjallarMessageText(content any) string {
	switch value := content.(type) {
	case string:
		return value

	case []any:
		var parts []string

		for _, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}

			partType, _ := part["type"].(string)

			if partType != "" && partType != "text" {
				continue
			}

			text, _ := part["text"].(string)
			if text != "" {
				parts = append(parts, text)
			}
		}

		return strings.Join(parts, "\n")
	}

	return ""
}

func gjallarLastUserMessage(
	payload map[string]any,
) string {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return ""
	}

	for i := len(messages) - 1; i >= 0; i-- {
		message, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}

		role, _ := message["role"].(string)

		if role != "user" {
			continue
		}

		return gjallarMessageText(
			message["content"],
		)
	}

	return ""
}

func gjallarIsTrivial(text string) bool {
	value := strings.ToLower(
		strings.TrimSpace(text),
	)

	value = strings.Trim(
		value,
		" \t\r\n.!?,;:()[]{}",
	)

	switch value {
	case
		"hi",
		"hello",
		"hey",
		"yo",
		"sup",
		"hiya",
		"howdy",
		"ping",
		"test",
		"thanks",
		"thank you",
		"thx",
		"ty",
		"nice",
		"cool",
		"lol",
		"lmao",
		"ok",
		"okay":
		return true
	}

	return false
}

func gjallarTrivialFastPath(
	payload map[string]any,
) bool {
	user := gjallarLastUserMessage(payload)

	if !gjallarIsTrivial(user) {
		return false
	}

	payload["messages"] = []any{
		map[string]any{
			"role": "system",
			"content": "You are gjallarCode. " +
				"Caveman style. " +
				"Reply to trivial chat naturally in 1-8 words. " +
				"No repo analysis. No OpenCode commentary.",
		},
		map[string]any{
			"role":    "user",
			"content": user,
		},
	}

	delete(payload, "tools")
	delete(payload, "tool_choice")
	delete(payload, "parallel_tool_calls")

	payload["max_tokens"] = 32

	return true
}

// renameModel swaps the client-facing model name for the upstream one, so a
// shared server can hold per-definition models under one client name.
// checkUpstream refuses a central server without a token or over plain
// HTTP, where the token and every prompt would cross the network in clear.
func checkUpstream(upstream, token string) error {
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("invalid upstream %q", upstream)
	}
	if ip := net.ParseIP(parsed.Hostname()); (ip != nil && ip.IsLoopback()) || parsed.Hostname() == "localhost" {
		return nil
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("upstream %s must use https", upstream)
	}
	if token == "" {
		return fmt.Errorf("upstream %s requires a token (--upstream-token-file)", upstream)
	}
	return nil
}

func renameModel(raw []byte, model, upstream string) ([]byte, error) {
	if upstream == "" || upstream == model {
		return raw, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("invalid JSON request")
	}
	payload["model"] = upstream
	return json.Marshal(payload)
}

func validateModel(raw []byte, model string) ([]byte, error) {
	var payload map[string]any

	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("invalid JSON request")
	}

	value, ok := payload["model"].(string)

	if !ok || value == "" {
		return nil, errors.New("model is required")
	}

	switch value {
	case model:
		return raw, nil

	case "ollama/" + model:
		payload["model"] = model
		if gjallarTrivialFastPath(payload) {
			return json.Marshal(payload)
		}

		return json.Marshal(payload)

	default:
		return nil, errors.New("model is not permitted")
	}
}

func authorizedPeer(p peerInfo, cfg Config) bool {
	// The Unix socket supplies kernel-authenticated PID/UID via SO_PEERCRED.
	//
	// Trust requires BOTH:
	//   1. the configured desktop UID; and
	//   2. membership in the controlled ai-session@ systemd cgroup.
	//
	// The relay executable itself is intentionally not a trust anchor.
	if p.PID <= 1 || p.UID != cfg.AllowedUID {
		return false
	}

	cgroup, err := os.ReadFile(
		fmt.Sprintf("/proc/%d/cgroup", p.PID),
	)
	if err != nil {
		return false
	}

	return strings.Contains(
		string(cgroup),
		cfg.RequiredCgroupPrefix,
	)
}

func unixPeer(conn net.Conn) (peerInfo, error) {
	unixConn, ok := conn.(*net.UnixConn)

	if !ok {
		return peerInfo{}, errors.New(
			"connection is not unix",
		)
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return peerInfo{}, err
	}

	var (
		cred       *syscall.Ucred
		controlErr error
	)

	err = raw.Control(func(fd uintptr) {
		cred, controlErr = syscall.GetsockoptUcred(
			int(fd),
			syscall.SOL_SOCKET,
			syscall.SO_PEERCRED,
		)
	})

	if err != nil {
		return peerInfo{}, err
	}

	if controlErr != nil {
		return peerInfo{}, controlErr
	}

	if cred == nil {
		return peerInfo{}, errors.New(
			"missing peer credentials",
		)
	}

	return peerInfo{
		PID: int(cred.Pid),
		UID: cred.Uid,
	}, nil
}

func hopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection",
		"proxy-connection",
		"keep-alive",
		"proxy-authenticate",
		"proxy-authorization",
		"te",
		"trailer",
		"transfer-encoding",
		"upgrade":
		return true

	default:
		return false
	}
}

package modelbroker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateModelAllowsDerivedModel(t *testing.T) {
	in := []byte(
		`{"model":"gjallaros-caveman-ai","messages":[]}`,
	)

	out, err := validateModel(
		in,
		"gjallaros-caveman-ai",
	)

	if err != nil {
		t.Fatal(err)
	}

	if string(out) != string(in) {
		t.Fatalf(
			"unexpected rewrite: %s",
			out,
		)
	}
}

func TestValidateModelNormalizesProviderPrefix(t *testing.T) {
	in := []byte(
		`{"model":"ollama/gjallaros-caveman-ai","messages":[]}`,
	)

	out, err := validateModel(
		in,
		"gjallaros-caveman-ai",
	)

	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any

	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if got["model"] != "gjallaros-caveman-ai" {
		t.Fatalf(
			"unexpected model: %v",
			got["model"],
		)
	}
}

func TestValidateModelRejectsBaseModel(t *testing.T) {
	_, err := validateModel(
		[]byte(
			`{"model":"qwen2.5-coder:14b"}`,
		),
		"gjallaros-caveman-ai",
	)

	if err == nil {
		t.Fatal("expected model rejection")
	}
}

func TestRenameModelUsesUpstreamName(t *testing.T) {
	in, err := validateModel(
		[]byte(`{"model":"ollama/gjallaros-caveman-ai","messages":[]}`),
		"gjallaros-caveman-ai",
	)
	if err != nil {
		t.Fatal(err)
	}

	out, err := renameModel(in, "gjallaros-caveman-ai", "gjallaros-caveman-ai-0123abcd")
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gjallaros-caveman-ai-0123abcd" {
		t.Fatalf("unexpected model: %v", got["model"])
	}

	same, err := renameModel(in, "gjallaros-caveman-ai", "")
	if err != nil || string(same) != string(in) {
		t.Fatalf("empty upstream rewrote request: %s %v", same, err)
	}
}

func TestCheckUpstreamRequiresTokenAndHTTPSForRemote(t *testing.T) {
	token := strings.Repeat("c3", 32)
	for upstream, ok := range map[string]bool{
		"http://127.0.0.1:11434":     true,
		"http://localhost:11434":     true,
		"https://ai.example:443":     true,
		"http://192.168.8.205:11434": false,
	} {
		if err := checkUpstream(upstream, token); (err == nil) != ok {
			t.Fatalf("%s with token: err=%v", upstream, err)
		}
	}
	if err := checkUpstream("https://ai.example", ""); err == nil {
		t.Fatal("remote upstream without token accepted")
	}
	if err := checkUpstream("http://127.0.0.1:11434", ""); err != nil {
		t.Fatalf("local upstream without token refused: %v", err)
	}
}

// Serve must tell systemd it is ready only once the socket answers.
func TestServeNotifiesSystemdWhenListening(t *testing.T) {
	dir := t.TempDir()
	notifyPath := filepath.Join(dir, "notify")
	notify, err := net.ListenUnixgram(
		"unixgram",
		&net.UnixAddr{Name: notifyPath, Net: "unixgram"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer notify.Close()
	t.Setenv("NOTIFY_SOCKET", notifyPath)

	socket := filepath.Join(dir, "model.sock")
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- Serve(ctx, Config{
			SocketPath:           socket,
			Model:                "gjallaros-caveman-ai",
			RequiredCgroupPrefix: "/ai-session@",
		})
	}()

	if err := notify.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := notify.Read(buf)
	if err != nil {
		t.Fatalf("no notify message: %v", err)
	}
	if got := string(buf[:n]); got != "READY=1" {
		t.Fatalf("notify message %q", got)
	}

	client := http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := client.Get("http://broker/health")
	if err != nil {
		t.Fatalf("health after READY=1: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", resp.StatusCode)
	}

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

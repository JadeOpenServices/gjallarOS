package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Hyprland renders on the GPUs it got at login: the session launcher
// (system/wm/hyprland) exports what `oddc rendergpu` printed for the screens
// connected then as AQ_DRM_DEVICES and leaves a copy in renderGPUPath. A
// screen plugged in later may want another GPU; only a new login moves
// Hyprland there, so a card offers one (ODDC 78).
func renderGPUPath() string {
	return filepath.Join(filepath.Dir(runtimeStatePath()), "render-gpu")
}

// renderGPUSettle lets a dock bring up all its screens before ODDC is asked.
const renderGPUSettle = 2 * time.Second

func runHyprlandRenderGPU(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl hyprland-render-gpu")
		return 2
	}
	data, err := os.ReadFile(renderGPUPath())
	if err != nil {
		// A session started before this rebuild: nothing to compare with.
		fmt.Fprintf(stderr, "render GPU at login unknown: %v\n", err)
		return 0
	}
	current := strings.TrimSpace(string(data))
	if _, err := oddcRenderGPU(); err != nil {
		// Models without ODDC's render quirk keep the boot GPU.
		fmt.Fprintf(stderr, "no render GPU choice on this model: %v\n", err)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "hypr", os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"), ".socket2.sock")
	conn, err := net.Dial("unix", socket)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: Hyprland events: %v\n", err)
		return 1
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	changes := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			if screenEvent(scanner.Text()) {
				select {
				case changes <- struct{}{}:
				default:
				}
			}
		}
		close(changes)
	}()

	w := renderGPUWatch{current: current, ask: askNewLogin, wanted: wantedRenderGPU}
	// Also once at start, for a screen plugged in during login.
	settle := time.NewTimer(renderGPUSettle)
	for {
		select {
		case _, ok := <-changes:
			if !ok {
				w.close()
				if ctx.Err() != nil {
					return 0
				}
				fmt.Fprintln(stderr, "ERROR: Hyprland events ended")
				return 1
			}
			settle.Reset(renderGPUSettle)
		case <-settle.C:
			w.check(ctx, stderr)
		}
	}
}

// screenEvent: a screen came or went.
func screenEvent(line string) bool {
	event, _, _ := strings.Cut(line, ">>")
	switch event {
	case "monitoradded", "monitoraddedv2", "monitorremoved", "monitorremovedv2":
		return true
	}
	return false
}

// renderGPUWatch keeps at most one card. It asks once for each GPU choice
// that differs from the login one, and takes the card back when the screens
// match the login again.
type renderGPUWatch struct {
	current string
	wanted  func() (string, bool, error)
	ask     func(ctx context.Context) bool

	asked  string
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *renderGPUWatch) check(ctx context.Context, stderr io.Writer) {
	want, ok, err := w.wanted()
	if err != nil {
		fmt.Fprintf(stderr, "WARN: oddc rendergpu: %v\n", err)
		return
	}
	if !ok || want == w.current {
		w.close()
		w.asked = ""
		return
	}
	if want == w.asked {
		return
	}
	w.close()
	w.asked = want
	cardCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w.cancel, w.done = cancel, done
	go func() {
		defer close(done)
		if w.ask(cardCtx) {
			if err := exec.Command("hyprctl", "dispatch", "hl.dsp.exit()").Run(); err != nil {
				fmt.Fprintf(stderr, "ERROR: log out: %v\n", err)
			}
		}
	}()
}

// close takes the card back and waits until it is gone, so that a stopped
// service leaves no card whose buttons nobody answers.
func (w *renderGPUWatch) close() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		w.cancel, w.done = nil, nil
	}
}

// wantedRenderGPU is ODDC's choice for the screens connected now. Battery
// saver keeps the login choice, as at login.
func wantedRenderGPU() (string, bool, error) {
	profile, _ := exec.Command("powerprofilesctl", "get").Output()
	if strings.TrimSpace(string(profile)) == "power-saver" {
		return "", false, nil
	}
	want, err := oddcRenderGPU()
	return want, err == nil && want != "", err
}

func oddcRenderGPU() (string, error) {
	cmd := exec.Command("oddc", "rendergpu", "--explain")
	var explain strings.Builder
	cmd.Stderr = &explain
	out, err := cmd.Output()
	if err != nil {
		if text := strings.TrimSpace(explain.String()); text != "" {
			return "", fmt.Errorf("%w: %s", err, text)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// askNewLogin shows the card and reports whether to log out now.
func askNewLogin(ctx context.Context) bool {
	choice, err := showNotice(ctx, desktopNotice{
		App:     "GjallarOS",
		Icon:    "video-display",
		Summary: "New screen: log in again",
		Body:    "It needs another graphics chip. Save your work, then log out and back in.",
		Actions: []noticeAction{{"logout", "Log out"}, {"later", "Later"}},
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "ERROR: notice: %v\n", err)
	}
	return choice == "logout"
}

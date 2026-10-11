package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// testSessionBus starts a private dbus-daemon and points the session bus
// address at it.
func testSessionBus(t *testing.T) {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not available")
	}
	// Short path: unix socket names are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "bus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(config, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=`+dir+`</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*"/>
    <allow receive_sender="*"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(daemon, "--config-file="+config, "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	address, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(address))
}

// fakeNotifier is a notification server that reports what it was asked.
type fakeNotifier struct {
	conn     *dbus.Conn
	id       uint32
	notified chan uint32
	closed   chan uint32
}

func (f *fakeNotifier) Notify(app string, replaces uint32, icon, summary, body string, actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.notified <- f.id
	return f.id, nil
}

func (f *fakeNotifier) CloseNotification(id uint32) *dbus.Error {
	f.closed <- id
	return nil
}

func startFakeNotifier(t *testing.T, id uint32) *fakeNotifier {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNotifier{conn: conn, id: id, notified: make(chan uint32, 4), closed: make(chan uint32, 4)}
	if err := conn.Export(f, notifyPath, notifyName); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(notifyName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", notifyName, reply, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return f
}

func (f *fakeNotifier) emit(t *testing.T, member string, values ...any) {
	t.Helper()
	if err := f.conn.Emit(notifyPath, notifyName+"."+member, values...); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, ch chan uint32, what string) uint32 {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s", what)
		return 0
	}
}

type noticeResult struct {
	key string
	err error
}

func showNoticeAsync(ctx context.Context) chan noticeResult {
	done := make(chan noticeResult, 1)
	go func() {
		key, err := showNotice(ctx, desktopNotice{App: "test", Summary: "s", Actions: []noticeAction{{"review", "Review"}}})
		done <- noticeResult{key, err}
	}()
	return done
}

// A stopped caller must not leave its card behind.
func TestShowNoticeClosesCardWhenStopped(t *testing.T) {
	testSessionBus(t)
	server := startFakeNotifier(t, 7)
	ctx, cancel := context.WithCancel(context.Background())
	done := showNoticeAsync(ctx)
	receive(t, server.notified, "notice")
	cancel()
	if id := receive(t, server.closed, "close"); id != 7 {
		t.Fatalf("closed %d, want 7", id)
	}
	if r := <-done; r.err == nil {
		t.Fatal("stopped notice reported an answer")
	}
}

// A restarted notification server forgets its cards; the notice comes back,
// and only the server can answer it.
func TestShowNoticeSurvivesServerRestart(t *testing.T) {
	testSessionBus(t)
	first := startFakeNotifier(t, 7)
	done := showNoticeAsync(context.Background())
	receive(t, first.notified, "notice")
	_ = first.conn.Close()

	second := startFakeNotifier(t, 9)
	if id := receive(t, second.notified, "notice after restart"); id != 9 {
		t.Fatalf("posted %d, want 9", id)
	}

	other, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Emit(notifyPath, notifyName+".ActionInvoked", uint32(9), "review"); err != nil {
		t.Fatal(err)
	}
	// The round trip makes the bus deliver the forged signal first.
	if err := other.BusObject().Call("org.freedesktop.DBus.GetId", 0).Err; err != nil {
		t.Fatal(err)
	}
	second.emit(t, "ActionInvoked", uint32(9), "keep")

	select {
	case r := <-done:
		if r.err != nil || r.key != "keep" {
			t.Fatalf("got %q, %v; want the server's answer keep", r.key, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	if id := receive(t, second.closed, "close"); id != 9 {
		t.Fatalf("closed %d, want 9", id)
	}
}

func TestShowNoticeDismissed(t *testing.T) {
	testSessionBus(t)
	server := startFakeNotifier(t, 3)
	done := showNoticeAsync(context.Background())
	receive(t, server.notified, "notice")
	server.emit(t, "NotificationClosed", uint32(3), uint32(2))
	if r := <-done; r.err != nil || r.key != "" {
		t.Fatalf("got %q, %v; want dismissed", r.key, r.err)
	}
}

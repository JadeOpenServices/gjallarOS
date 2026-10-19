package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
)

type fakeReader struct{}

func (fakeReader) Status(
	context.Context,
) (broker.Status, error) {
	return broker.Status{
		StatePresent:    false,
		ExpectedDevices: 2,
		ObservedDevices: 4,
	}, nil
}

func (fakeReader) Audit(
	context.Context,
) (usbtrust.AuditResult, uint64, error) {
	return usbtrust.AuditResult{
		Findings: []usbtrust.Finding{
			{
				State:   usbtrust.AuditBlock,
				Code:    usbtrust.CodeUnknownExternal,
				Message: "synthetic external device",
			},
		},
	}, 0, nil
}

func TestServerRoundTripUsesUnixPeerCredentials(
	t *testing.T,
) {
	root := t.TempDir()

	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}

	socket := filepath.Join(
		root,
		"control.sock",
	)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	owner := uint32(os.Geteuid())
	server := Server{
		SocketPath:     socket,
		SocketMode:     0600,
		SocketOwner:    &owner,
		RequestTimeout: time.Second,
		Handler: broker.Handler{
			OwnerUID: owner,
			Reader:   fakeReader{},
		},
	}

	done := make(chan error, 1)

	go func() {
		done <- server.Serve(ctx)
	}()

	waitForSocket(t, socket)

	conn, err := net.DialUnix(
		"unix",
		nil,
		&net.UnixAddr{
			Name: socket,
			Net:  "unix",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.Write(
		[]byte(`{"action":"status"}`),
	); err != nil {
		t.Fatal(err)
	}

	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	var response broker.Response

	if err := json.NewDecoder(conn).Decode(
		&response,
	); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	// The answer came after Serve set owner and mode.
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if uid := info.Sys().(*syscall.Stat_t).Uid; uid != owner {
		t.Fatalf("socket owner = %d, want %d", uid, owner)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("socket mode = %o, want 600", mode)
	}

	if !response.OK {
		t.Fatalf(
			"broker response failed: %s",
			response.Error,
		)
	}

	if response.Status == nil {
		t.Fatal("status payload missing")
	}

	if response.Status.ExpectedDevices != 2 ||
		response.Status.ObservedDevices != 4 {
		t.Fatalf(
			"unexpected status %#v",
			response.Status,
		)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("USB trust server did not stop")
	}
}

func TestServerRejectsRelativeSocket(t *testing.T) {
	err := (Server{
		SocketPath: "control.sock",
	}).validate()

	if err == nil {
		t.Fatal("relative socket path accepted")
	}
}

func TestPrepareSocketPathRejectsRegularFile(
	t *testing.T,
) {
	path := filepath.Join(
		t.TempDir(),
		"control.sock",
	)

	if err := os.WriteFile(
		path,
		[]byte("do not replace"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	err := prepareSocketPath(path)
	if err == nil {
		t.Fatal("regular file accepted as stale socket")
	}

	if !strings.Contains(
		err.Error(),
		"non-socket",
	) {
		t.Fatalf(
			"unexpected error %q",
			err,
		)
	}
}

func TestPrepareSocketPathRejectsSymlink(
	t *testing.T,
) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	link := filepath.Join(root, "control.sock")

	if err := os.WriteFile(
		target,
		[]byte("target"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(
		target,
		link,
	); err != nil {
		t.Fatal(err)
	}

	err := prepareSocketPath(link)
	if err == nil {
		t.Fatal("socket-path symlink accepted")
	}
}

func waitForSocket(
	t *testing.T,
	path string,
) {
	t.Helper()

	deadline := time.Now().Add(
		2 * time.Second,
	)

	for time.Now().Before(deadline) {
		info, err := os.Lstat(path)

		if err == nil &&
			info.Mode()&os.ModeSocket != 0 {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf(
		"socket %q was not created",
		path,
	)
}

package winesandbox

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWlString(t *testing.T) {
	for _, s := range []string{"", "a", "abc", "abcd", SandboxEngine} {
		b := wlArg(s)
		if len(b)%4 != 0 {
			t.Errorf("%q: %d bytes, not padded", s, len(b))
		}
		if got, ok := wlString(b); !ok || got != s {
			t.Errorf("%q came back as %q", s, got)
		}
	}
}

// fakeCompositor answers like Hyprland: it lists globals and, if offered,
// accepts the security context. It records what the client set.
func fakeCompositor(t *testing.T, ln *net.UnixListener, offer bool, got chan<- []string) {
	conn, err := ln.AcceptUnix()
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	w := &wlConn{conn: conn}
	event := func(obj uint32, op uint16, body []byte) {
		if err := w.send(obj, op, body, nil); err != nil {
			t.Error(err)
		}
	}
	var seen []string
	buf := make([]byte, 4096)
	oob := make([]byte, 64)
	var pending []byte
	for {
		n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
		if err != nil {
			got <- seen
			return
		}
		if oobn > 0 {
			msgs, _ := unix.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				fds, _ := unix.ParseUnixRights(&m)
				seen = append(seen, "fds")
				for _, fd := range fds {
					unix.Close(fd)
				}
			}
		}
		pending = append(pending, buf[:n]...)
		for len(pending) >= 8 {
			size := int(binary.NativeEndian.Uint32(pending[4:]) >> 16)
			if len(pending) < size {
				break
			}
			obj := binary.NativeEndian.Uint32(pending)
			op := uint16(binary.NativeEndian.Uint32(pending[4:]))
			body := pending[8:size]
			pending = pending[size:]
			switch {
			case obj == 1 && op == 0:
				id := binary.NativeEndian.Uint32(body)
				if id == 3 {
					event(2, 0, append(append(u32(1), wlArg("wl_compositor")...), u32(6)...))
					if offer {
						event(2, 0, append(append(u32(7), wlArg(securityContextManager)...), u32(1)...))
					}
				}
				event(id, 0, u32(0))
			case obj == 2 && op == 0:
				iface, _ := wlString(body[4:])
				seen = append(seen, "bind "+iface)
			case obj == 5 && op >= 1 && op <= 3:
				s, _ := wlString(body)
				seen = append(seen, s)
			case obj == 5 && op == 4:
				seen = append(seen, "commit")
			}
		}
	}
}

func TestSecurityContext(t *testing.T) {
	dir := t.TempDir()
	display := filepath.Join(dir, "wayland-1")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: display, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []string, 1)
	go fakeCompositor(t, ln, true, got)

	path := filepath.Join(dir, "app.sock")
	sc, err := NewSecurityContext(display, path, "notepad", "notepad-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("socket missing: %v", err)
	}
	if err := sc.Close(); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
	want := "bind " + securityContextManager + ",fds," + SandboxEngine + ",notepad,notepad-1,commit"
	if seen := strings.Join(<-got, ","); seen != want {
		t.Errorf("compositor saw %s, want %s", seen, want)
	}
}

func TestSecurityContextMissing(t *testing.T) {
	dir := t.TempDir()
	display := filepath.Join(dir, "wayland-1")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: display, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []string, 1)
	go fakeCompositor(t, ln, false, got)
	path := filepath.Join(dir, "app.sock")
	if _, err := NewSecurityContext(display, path, "notepad", "notepad-1"); err == nil {
		t.Fatal("no error without the protocol")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket made anyway: %v", err)
	}
}

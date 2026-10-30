package winesandbox

import (
	"slices"
	"strings"
	"testing"
)

func TestSandboxArgs(t *testing.T) {
	s := Sandbox{
		Bwrap: "/store/bwrap", Self: "/store/gjallar-wine", Wine: "/store/wine",
		UID: 1000, GID: 100, User: "tester", Home: "/home/tester",
		RuntimeDir: "/run/user/1000", Wayland: "/run/user/1000/gjallar-wine/notepad.sock",
		ResolvConf: "/run/user/1000/gjallar-wine/notepad.resolv",
		Prefix:     "/home/tester/.local/share/gjallar-wine/notepad", Files: "/home/tester/WineApps/Notepad",
		ID: "notepad",
	}
	args := s.Args([]string{`C:\notepad.exe`, "a b"})
	joined := strings.Join(args, " ")
	pairs := func(flag, src, dst string) bool {
		for i := 0; i+2 < len(args); i++ {
			if args[i] == flag && args[i+1] == src && args[i+2] == dst {
				return true
			}
		}
		return false
	}
	if pairs("--ro-bind", "/", "/") || pairs("--bind", "/home/tester", "/home/tester") || strings.Contains(joined, "/home/tester/.local/share/gjallar-wine ") {
		t.Errorf("host tree bound:\n%s", joined)
	}
	for _, want := range [][3]string{
		{"--ro-bind", s.Wayland, "/run/user/1000/wayland-0"},
		{"--ro-bind", s.ResolvConf, "/etc/resolv.conf"},
		{"--bind", s.Prefix, s.Prefix},
		{"--bind", s.Files, s.Files},
	} {
		if !pairs(want[0], want[1], want[2]) {
			t.Errorf("missing %v", want)
		}
	}
	for _, sock := range []string{"/run/user/1000/bus", "pipewire", ".X11-unix", "--setenv DISPLAY", "DBUS_SESSION_BUS_ADDRESS"} {
		if strings.Contains(joined, sock) {
			t.Errorf("%s reaches the sandbox", sock)
		}
	}
	clear := slices.Index(args, "--clearenv")
	if clear < 0 || clear > slices.Index(args, "--setenv") {
		t.Error("environment not cleared before it is set")
	}
	for _, flag := range []string{"--disable-userns", "--unshare-pid", "--die-with-parent", "--new-session"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	tail := args[slices.Index(args, "--")+1:]
	want := []string{"/store/gjallar-wine", "lock", "--sym", s.Prefix, "--sym", "/tmp", "--refer", "/", "--", "/store/gjallar-wine", "stay", "/store/wine", `C:\notepad.exe`, "a b"}
	if !slices.Equal(tail, want) {
		t.Errorf("command = %q, want %q", tail, want)
	}
}

package movehelper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const livePasswd = "root:x:0:0:System administrator:/root:/bin/sh\n" +
	"nixos:x:60600:100::/home/nixos:/bin/sh\n" +
	"nobody:x:65534:65534:Unprivileged account:/var/empty:/bin/false\n"

func passwdFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Q254: the helper runs only where no account has an installed user's id.
func TestCheckLiveUsers(t *testing.T) {
	asRoot(t)
	if err := CheckLiveUsers(passwdFile(t, livePasswd)); err != nil {
		t.Fatalf("live accounts refused: %v", err)
	}
	for name, extra := range map[string]string{
		"lowest":    "x:x:1000:100::/home/x:/bin/sh\n",
		"highest":   "x:x:59999:100::/home/x:/bin/sh\n",
		"bad id":    "x:x:+1000:100::/home/x:/bin/sh\n",
		"short":     "x:x:1000\n",
		"nis entry": "+::::::\n",
	} {
		if err := CheckLiveUsers(passwdFile(t, livePasswd+extra)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The account list is read only from root's own plain file.
func TestCheckLiveUsersPlanted(t *testing.T) {
	asRoot(t)
	real := passwdFile(t, livePasswd)
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	writable := passwdFile(t, livePasswd)
	if err := os.Chmod(writable, 0o666); err != nil {
		t.Fatal(err)
	}
	large := passwdFile(t, livePasswd+strings.Repeat("y:x:70000:100::/:/bin/sh\n", maxPasswd/24))
	for name, p := range map[string]string{"link": link, "fifo": fifo, "writable": writable, "large": large, "missing": filepath.Join(dir, "none")} {
		if err := CheckLiveUsers(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	rootUID = uint32(os.Getuid()) + 1
	if err := CheckLiveUsers(real); err == nil {
		t.Error("someone else's file accepted")
	}
}

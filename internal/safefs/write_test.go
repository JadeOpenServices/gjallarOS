package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWriteFile(t *testing.T) {
	top := t.TempDir()
	if err := os.Chmod(top, 0o755); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	d := mustDir(t, top)
	if err := d.MakeDirs("etc/nm/conns", uid, gid, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(top, "etc/nm/conns")); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("made folder: %v %v", st, err)
	}
	// MakeDirs again finds them; nothing changes.
	if err := d.MakeDirs("etc/nm", uid, gid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteFile("etc/nm/conns/a", []byte("one"), uid, gid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteFile("etc/nm/conns/a", []byte("two"), uid, gid, 0o444); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(top, "etc/nm/conns/a"))
	if err != nil || string(b) != "two" {
		t.Fatalf("got %q, %v", b, err)
	}
	if st, _ := os.Stat(filepath.Join(top, "etc/nm/conns/a")); st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v", st.Mode())
	}
	names, _ := os.ReadDir(filepath.Join(top, "etc/nm/conns"))
	if len(names) != 1 {
		t.Errorf("left over: %v", names)
	}
	if err := d.WriteFile("etc/big", make([]byte, MaxWrite+1), uid, gid, 0o600); !errors.Is(err, ErrTooBig) {
		t.Errorf("over cap: %v", err)
	}
	if err := d.MakeDirs("x", uid, gid, 0o775); err == nil {
		t.Error("a mode others may write was taken")
	}
}

// Planted links, pipes and open folders at the target or on the way are
// refused, and what was planted stays as it was.
func TestWriteFilePlanted(t *testing.T) {
	top := t.TempDir()
	if err := os.Chmod(top, 0o755); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	outside := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"etc", "open"} {
		if err := os.Mkdir(filepath.Join(top, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(top, "open"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(top, "etc/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(top, "via")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(top, "etc/fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(top, "etc/dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := mustDir(t, top)
	for _, c := range []struct {
		rel  string
		want error
	}{
		{"etc/link", ErrNotPlain},
		{"etc/fifo", ErrNotPlain},
		{"etc/dir", ErrNotPlain},
		{"via/shadow", ErrNotPlain},
		{"open/file", ErrNotPrivate},
		{"../x", ErrNotPlain},
	} {
		if err := d.WriteFile(c.rel, []byte("bad"), uid, gid, 0o600); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", c.rel, err)
		}
	}
	if err := d.MakeDirs("via/x", uid, gid, 0o700); !errors.Is(err, ErrNotPlain) {
		t.Errorf("make through a link: %v", err)
	}
	if err := d.MakeDirs("open/x", uid, gid, 0o700); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("make in an open folder: %v", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Errorf("outside file changed: %q", b)
	}
	if st, err := os.Lstat(filepath.Join(top, "etc/fifo")); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("fifo: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "x")); err == nil {
		t.Error("made a folder through a link")
	}
	names, _ := os.ReadDir(filepath.Join(top, "etc"))
	for _, n := range names {
		if strings.Contains(n.Name(), ".new-") {
			t.Errorf("left over: %s", n.Name())
		}
	}
}

func TestRemove(t *testing.T) {
	top := t.TempDir()
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(top, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(top, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := mustDir(t, top)
	if err := d.Remove("link"); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("missing"); err != nil {
		t.Errorf("missing: %v", err)
	}
	if err := d.Remove("dir"); err == nil {
		t.Error("removed a folder")
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Errorf("link target changed: %q", b)
	}
}

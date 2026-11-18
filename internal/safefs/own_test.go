package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReadlink(t *testing.T) {
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, "a/b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nix/store/x-system", filepath.Join(top, "a/b/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(top, "via")); err != nil {
		t.Fatal(err)
	}
	d := mustDir(t, top)
	got, err := d.Readlink("a/b/link", 64)
	if err != nil || got != "/nix/store/x-system" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := d.Readlink("a/b/link", 5); !errors.Is(err, ErrTooBig) {
		t.Errorf("over max: %v", err)
	}
	// A link on the way is refused, not followed.
	if _, err := d.Readlink("via/b/link", 64); !errors.Is(err, ErrNotPlain) {
		t.Errorf("link on the way: %v", err)
	}
	for _, bad := range []string{"../x", "/a/b/link", "a/../a/b/link", "a//b/link", ""} {
		if _, err := d.Readlink(bad, 64); !errors.Is(err, ErrNotPlain) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// Not a link at all.
	if _, err := d.Readlink("a", 64); err == nil {
		t.Error("folder read as link")
	}
}

func TestOwn(t *testing.T) {
	top := t.TempDir()
	write(t, filepath.Join(top, "f"), "x")
	if err := os.Mkdir(filepath.Join(top, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	write(t, outside, "x")
	if err := os.Chmod(outside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(top, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(top, filepath.Join(top, "up")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(top, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	// L136: a hard link names the outside file itself.
	if err := os.Link(outside, filepath.Join(top, "hard")); err != nil {
		t.Fatal(err)
	}
	d := mustDir(t, top)
	uid, gid := os.Getuid(), os.Getgid()

	st, err := d.Own("f", uid, gid, 0o644)
	if err != nil || st.Mode&0o7777 != 0o644 {
		t.Fatalf("file: %o, %v", st.Mode, err)
	}
	st, err = d.Own("d", uid, gid, 0o755)
	if err != nil || st.Mode&0o7777 != 0o755 || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Fatalf("folder: %o, %v", st.Mode, err)
	}
	// A pipe would hang a reader; a link would hand over what it points at.
	for _, bad := range []string{"fifo", "link", "up/f", "hard"} {
		if _, err := d.Own(bad, uid, gid, 0o644); !errors.Is(err, ErrNotPlain) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if fi, err := os.Stat(outside); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("outside file changed: %v, %v", fi.Mode(), err)
	}
}

func TestMkdir(t *testing.T) {
	top := t.TempDir()
	old := unix.Umask(0o077)
	defer unix.Umask(old)
	d := mustDir(t, top)
	sub, err := d.Mkdir("cache", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()
	if fi, err := os.Lstat(filepath.Join(top, "cache")); err != nil || fi.Mode().Perm() != 0o777 {
		t.Fatalf("mode %v, %v", fi.Mode(), err)
	}
	if _, err := d.Mkdir("cache", 0o777); !errors.Is(err, ErrExists) {
		t.Errorf("again: %v", err)
	}
	// A planted link at the name is not followed.
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(top, "planted")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Mkdir("planted", 0o777); !errors.Is(err, ErrExists) {
		t.Errorf("planted: %v", err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() == 0o777 {
		t.Errorf("outside file changed: %v, %v", fi.Mode(), err)
	}
	for _, bad := range []string{"a/b", "..", "../x", ""} {
		if _, err := d.Mkdir(bad, 0o755); !errors.Is(err, ErrNotPlain) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestRemoveTree(t *testing.T) {
	top := t.TempDir()
	keep := t.TempDir()
	write(t, filepath.Join(keep, "precious"), "x")
	write(t, filepath.Join(top, "c/nar/a.nar"), "x")
	write(t, filepath.Join(top, "c/x.narinfo"), "x")
	if err := os.Symlink(keep, filepath.Join(top, "c/nar/out")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(top, "c/fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := mustDir(t, top)
	if err := d.RemoveTree("c", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(top, "c")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(keep, "precious")); err != nil {
		t.Errorf("followed the link: %v", err)
	}
	// Gone already is fine.
	if err := d.RemoveTree("c", 100); err != nil {
		t.Errorf("again: %v", err)
	}

	write(t, filepath.Join(top, "many/1"), "")
	write(t, filepath.Join(top, "many/2"), "")
	write(t, filepath.Join(top, "many/3"), "")
	if err := d.RemoveTree("many", 2); !errors.Is(err, ErrTooMany) {
		t.Errorf("over max: %v", err)
	}
}

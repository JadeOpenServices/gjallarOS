package stickkeyboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// setup returns a private base folder and makes the test user the owner
// every part must have, standing in for root.
func setup(t *testing.T) string {
	t.Helper()
	old := owner
	owner = uint32(os.Getuid())
	t.Cleanup(func() { owner = old })
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	return base
}

func TestWriteThenRead(t *testing.T) {
	base := setup(t)
	if err := write(base, Dir, "de"); err != nil {
		t.Fatal(err)
	}
	got, err := read(base, Dir)
	if err != nil || got != "de" {
		t.Fatalf("read = %q, %v; want de", got, err)
	}
	if err := write(base, Dir, "fr"); err != nil {
		t.Fatal(err)
	}
	if got, _ := read(base, Dir); got != "fr" {
		t.Fatalf("second write: read = %q, want fr", got)
	}
	st, err := os.Stat(filepath.Join(base, Dir, Name))
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode = %v, %v; want 0644", st.Mode(), err)
	}
	if _, err := os.Lstat(filepath.Join(base, Dir, Name+".new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file left behind: %v", err)
	}
}

func TestReadNothingYet(t *testing.T) {
	base := setup(t)
	if _, err := read(base, Dir); !errors.Is(err, ErrNone) {
		t.Fatalf("no folder: err = %v, want ErrNone", err)
	}
	if err := os.MkdirAll(filepath.Join(base, Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := read(base, Dir); !errors.Is(err, ErrNone) {
		t.Fatalf("no file: err = %v, want ErrNone", err)
	}
}

func TestWriteRefusesBadNames(t *testing.T) {
	base := setup(t)
	for _, bad := range []string{"", "De", "../x", "de\n", "de us", strings.Repeat("a", 33), "1us"} {
		if err := write(base, Dir, bad); err == nil {
			t.Errorf("write(%q) took it", bad)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, Dir, Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused name left a file: %v", err)
	}
}

// Loophole check: a link planted at the folder or its parent must not
// lead the writer or the reader elsewhere.
func TestLinkedFolderRefused(t *testing.T) {
	for _, at := range []string{"/run", "/run/gjallar", Dir} {
		t.Run(at, func(t *testing.T) {
			base := setup(t)
			elsewhere := filepath.Join(base, "elsewhere")
			if err := os.MkdirAll(filepath.Join(elsewhere, "stick"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(elsewhere, "stick", Name), []byte("de\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(elsewhere, Name), []byte("de\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(base, at)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, link); err != nil {
				t.Fatal(err)
			}
			if err := write(base, Dir, "fr"); err == nil {
				t.Fatal("write went through a linked folder")
			}
			if got, err := read(base, Dir); err == nil {
				t.Fatalf("read went through a linked folder and got %q", got)
			}
			for _, f := range []string{filepath.Join(elsewhere, "stick", Name), filepath.Join(elsewhere, Name)} {
				if data, _ := os.ReadFile(f); string(data) != "de\n" {
					t.Fatalf("%s changed to %q", f, data)
				}
			}
		})
	}
}

// Loophole check: a link at the file name must not be read through, and
// the writer replaces the link itself instead of writing where it points.
func TestLinkedFileRefused(t *testing.T) {
	base := setup(t)
	dir := filepath.Join(base, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	if err := os.WriteFile(target, []byte("de\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, Name)); err != nil {
		t.Fatal(err)
	}
	if got, err := read(base, Dir); err == nil {
		t.Fatalf("read followed the link and got %q", got)
	}
	if err := os.Symlink(target, filepath.Join(dir, Name+".new")); err != nil {
		t.Fatal(err)
	}
	if err := write(base, Dir, "fr"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "de\n" {
		t.Fatalf("link target changed to %q", data)
	}
	if got, err := read(base, Dir); err != nil || got != "fr" {
		t.Fatalf("read after write = %q, %v; want fr", got, err)
	}
}

// Loophole check: a FIFO at the name must be refused at once, not hang.
func TestFIFORefused(t *testing.T) {
	base := setup(t)
	dir := filepath.Join(base, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, Name), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := read(base, Dir); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read took a FIFO")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read hung on a FIFO")
	}
}

func TestLooseModesRefused(t *testing.T) {
	cases := map[string]func(base string) error{
		"folder group-writable": func(base string) error { return os.Chmod(filepath.Join(base, Dir), 0o775) },
		"parent other-writable": func(base string) error { return os.Chmod(filepath.Join(base, "run/gjallar"), 0o757) },
		"base other-writable":   func(base string) error { return os.Chmod(base, 0o1777) },
		"file group-writable":   func(base string) error { return os.Chmod(filepath.Join(base, Dir, Name), 0o664) },
	}
	for name, loosen := range cases {
		t.Run(name, func(t *testing.T) {
			base := setup(t)
			if err := write(base, Dir, "de"); err != nil {
				t.Fatal(err)
			}
			if err := loosen(base); err != nil {
				t.Fatal(err)
			}
			if got, err := read(base, Dir); err == nil {
				t.Fatalf("read took it and got %q", got)
			}
			if !strings.HasPrefix(name, "file") {
				if err := write(base, Dir, "fr"); err == nil {
					t.Fatal("write took it")
				}
			}
		})
	}
}

func TestWrongOwnerRefused(t *testing.T) {
	base := setup(t)
	if err := write(base, Dir, "de"); err != nil {
		t.Fatal(err)
	}
	owner++
	if _, err := read(base, Dir); err == nil {
		t.Fatal("read took a folder owned by someone else")
	}
}

func TestBadContentRefused(t *testing.T) {
	for name, data := range map[string]string{
		"too big":     strings.Repeat("a", maxSize+1),
		"not a name":  "de; rm\n",
		"empty":       "",
		"two lines":   "de\nus\n",
		"upper case":  "DE\n",
		"no trailing": "de us",
	} {
		t.Run(name, func(t *testing.T) {
			base := setup(t)
			dir := filepath.Join(base, Dir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, Name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, err := read(base, Dir); err == nil {
				t.Fatalf("read took %q as %q", data, got)
			}
		})
	}
}

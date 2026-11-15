package installedanswers

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func setup(t *testing.T) (root, dir string) {
	t.Helper()
	old := owner
	owner = uint32(os.Getuid())
	t.Cleanup(func() { owner = old })
	root = t.TempDir()
	dir = filepath.Join(root, filepath.Dir(Path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, dir
}

func refused(t *testing.T, root, want string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := Read(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Read = %v, want error with %q", err, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read hung")
	}
}

func TestReadsTheInstalledAnswers(t *testing.T) {
	root, dir := setup(t)
	want := []byte(`{"username":"example"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "user.config.json"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Read = %q, want %q", got, want)
	}
}

func TestMissingFileIsNotExist(t *testing.T) {
	root, _ := setup(t)
	if _, err := Read(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read = %v, want fs.ErrNotExist", err)
	}
	empty := t.TempDir()
	if _, err := Read(empty); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read on a root without etc = %v, want fs.ErrNotExist", err)
	}
}

func TestRefusesALinkAtTheFile(t *testing.T) {
	root, dir := setup(t)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "user.config.json")); err != nil {
		t.Fatal(err)
	}
	refused(t, root, "link")
}

func TestRefusesALinkInsideTheRoot(t *testing.T) {
	root, dir := setup(t)
	if err := os.WriteFile(filepath.Join(root, "other.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../other.json", filepath.Join(dir, "user.config.json")); err != nil {
		t.Fatal(err)
	}
	refused(t, root, "link")
}

func TestRefusesALinkedParentFolder(t *testing.T) {
	root, _ := setup(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "gjallar", "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "gjallar", "system", "user.config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	refused(t, root, "link")
}

func TestRefusesALinkedRoot(t *testing.T) {
	root, dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "user.config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "mnt")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	refused(t, link, "open installed root")
}

func TestRefusesAFIFOWithoutHanging(t *testing.T) {
	root, dir := setup(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "user.config.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused(t, root, "not a regular file")
}

func TestRefusesAFolder(t *testing.T) {
	root, dir := setup(t)
	if err := os.Mkdir(filepath.Join(dir, "user.config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused(t, root, "not a regular file")
}

func TestRefusesAnOversizeFile(t *testing.T) {
	root, dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "user.config.json"), bytes.Repeat([]byte(" "), MaxSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	refused(t, root, "larger than")
}

func TestRefusesAWritableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o664, 0o646} {
		root, dir := setup(t)
		path := filepath.Join(dir, "user.config.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		refused(t, root, "may write it")
	}
}

func TestRefusesAnotherOwner(t *testing.T) {
	root, dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "user.config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	owner++
	refused(t, root, "owned by uid")
}

func TestRefusesAHardLink(t *testing.T) {
	root, dir := setup(t)
	other := filepath.Join(root, "other.json")
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, filepath.Join(dir, "user.config.json")); err != nil {
		t.Fatal(err)
	}
	refused(t, root, "hard links")
}

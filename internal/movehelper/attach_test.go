package movehelper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// attachBase is a helper folder with an empty New, as the dirs op leaves it.
func attachBase(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "gjallar-move")
	mkdirs(t, filepath.Join(base, New))
	return base
}

// The target must be root's own mount, closed to everyone else; nothing
// else is copied to New, and a link or FIFO is never followed. The real
// mount is proven in the VM: a test user cannot mount.
func TestAttachPlanted(t *testing.T) {
	asRoot(t)
	elsewhere := t.TempDir()
	for name, c := range map[string]struct {
		plant func(t *testing.T, target string)
		want  string
	}{
		"not a mount": {func(t *testing.T, target string) { mkdirs(t, target) }, "not mounted"},
		"missing":     {func(t *testing.T, target string) {}, "open"},
		"link": {func(t *testing.T, target string) {
			if err := os.Symlink(elsewhere, target); err != nil {
				t.Fatal(err)
			}
		}, "open"},
		"dangling link": {func(t *testing.T, target string) {
			if err := os.Symlink("/nowhere", target); err != nil {
				t.Fatal(err)
			}
		}, "open"},
		"fifo": {func(t *testing.T, target string) {
			if err := unix.Mkfifo(target, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "open"},
		"file": {func(t *testing.T, target string) {
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "open"},
		"group writable": {func(t *testing.T, target string) {
			mkdirs(t, target)
			if err := os.Chmod(target, 0o775); err != nil {
				t.Fatal(err)
			}
		}, "other than root"},
		"world writable": {func(t *testing.T, target string) {
			mkdirs(t, target)
			if err := os.Chmod(target, 0o1777); err != nil {
				t.Fatal(err)
			}
		}, "other than root"},
	} {
		t.Run(name, func(t *testing.T) {
			base := attachBase(t)
			target := filepath.Join(t.TempDir(), "mnt")
			c.plant(t, target)
			err := attach(base, target)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("attach: %v, want %q", err, c.want)
			}
			if mounted, err := mountRootAt(filepath.Join(base, New)); err != nil || mounted {
				t.Errorf("New after refusal: mounted %v, %v", mounted, err)
			}
		})
	}
}

func TestAttachOtherOwner(t *testing.T) {
	asRoot(t)
	base := attachBase(t)
	target := filepath.Join(t.TempDir(), "mnt")
	mkdirs(t, target)
	rootUID = uint32(os.Getuid()) + 1
	if err := attach(base, target); err == nil || !strings.Contains(err.Error(), "not owned by root") {
		t.Errorf("attach: %v", err)
	}
}

// New itself must be a plain folder: a link there is refused, not followed.
func TestAttachNewPlanted(t *testing.T) {
	asRoot(t)
	base := filepath.Join(t.TempDir(), "gjallar-move")
	mkdirs(t, base)
	if err := os.Symlink(t.TempDir(), filepath.Join(base, New)); err != nil {
		t.Fatal(err)
	}
	if err := attach(base, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Errorf("attach over a link: %v", err)
	}
	if err := detachOp(base); err == nil {
		t.Error("detach over a link accepted")
	}
}

// Nothing attached: detach has nothing to do and says so with no error.
func TestDetachNothingAttached(t *testing.T) {
	if err := detachOp(attachBase(t)); err != nil {
		t.Error(err)
	}
}

// detach is a helper op; with nothing attached it answers an empty result.
func TestDetachOp(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	if _, err := run(t, base, `{"op":"dirs"}`); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, base, `{"op":"detach"}`)
	if err != nil || strings.TrimSpace(out) != "{}" {
		t.Errorf("detach: %q, %v", out, err)
	}
}

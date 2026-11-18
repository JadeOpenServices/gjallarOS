package rootprovision

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The provision tests use a fake runner; they must not depend on the /mnt
// of the machine they run on. TestCheckMountPoint tests the real check.
func TestMain(m *testing.M) {
	checkTarget = func() error { return nil }
	os.Exit(m.Run())
}

// asRoot makes the test user stand in for root as the owner.
func asRoot(t *testing.T) {
	t.Helper()
	was := rootUID
	rootUID = uint32(os.Getuid())
	t.Cleanup(func() { rootUID = was })
}

// parent is a folder only the test user ("root") can change.
func parent(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "top")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Q255: root mounts only on a mount point nobody else can change or swap.
func TestCheckMountPoint(t *testing.T) {
	asRoot(t)
	top := parent(t)
	if err := CheckMountPoint(filepath.Join(top, "mnt")); err != nil {
		t.Errorf("missing mount point refused: %v", err)
	}
	mnt := filepath.Join(top, "mnt")
	if err := os.Mkdir(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckMountPoint(mnt); err != nil {
		t.Errorf("empty root folder refused: %v", err)
	}
	for _, p := range []string{"", "mnt", "/", "/mnt/", "/x/../mnt"} {
		if CheckMountPoint(p) == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestCheckMountPointPlanted(t *testing.T) {
	asRoot(t)
	elsewhere := t.TempDir()
	for name, plant := range map[string]func(top string) error{
		"link": func(top string) error { return os.Symlink(elsewhere, filepath.Join(top, "mnt")) },
		"dangling link": func(top string) error {
			return os.Symlink(filepath.Join(elsewhere, "none"), filepath.Join(top, "mnt"))
		},
		"fifo": func(top string) error { return unix.Mkfifo(filepath.Join(top, "mnt"), 0o644) },
		"file": func(top string) error { return os.WriteFile(filepath.Join(top, "mnt"), nil, 0o644) },
		"not empty": func(top string) error {
			if err := os.Mkdir(filepath.Join(top, "mnt"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(top, "mnt", "x"), nil, 0o644)
		},
		"group writable": func(top string) error {
			if err := os.Mkdir(filepath.Join(top, "mnt"), 0o755); err != nil {
				return err
			}
			return os.Chmod(filepath.Join(top, "mnt"), 0o775)
		},
		"world writable": func(top string) error {
			if err := os.Mkdir(filepath.Join(top, "mnt"), 0o755); err != nil {
				return err
			}
			return os.Chmod(filepath.Join(top, "mnt"), 0o1777)
		},
		"parent writable":              func(top string) error { return os.Chmod(top, 0o777) },
		"parent writable, mnt missing": func(top string) error { return os.Chmod(top, 0o757) },
	} {
		top := parent(t)
		if err := plant(top); err != nil {
			t.Fatal(err)
		}
		if err := CheckMountPoint(filepath.Join(top, "mnt")); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// Someone else's folder: the test user is not "root" here.
	top := parent(t)
	if err := os.Mkdir(filepath.Join(top, "mnt"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootUID = uint32(os.Getuid()) + 1
	if err := CheckMountPoint(filepath.Join(top, "mnt")); err == nil {
		t.Error("folder of another user accepted")
	}
}

// A refused mount point stops the install before anything is formatted.
func TestProvisionStopsOnUnsafeMountPoint(t *testing.T) {
	was := checkTarget
	checkTarget = func() error { return errors.New("SYNTHETIC unsafe mount point") }
	t.Cleanup(func() { checkTarget = was })

	r := runner("btrfs")
	var out bytes.Buffer
	_, err := provision(context.Background(), Input{Plan: testPlan("btrfs"), Passphrase: []byte("SYNTHETIC"), Out: &out}, r)
	if err == nil || !strings.Contains(err.Error(), "SYNTHETIC unsafe") {
		t.Fatalf("unsafe mount point accepted: %v", err)
	}
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), "sudo") {
			t.Fatalf("root step ran: %v", call)
		}
	}
}

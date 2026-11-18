package movehelper

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

// mountsChild names the folder the namespace child of TestNewRootMounts
// works in; set only in that child.
const mountsChild = "GJ_MOVE_MOUNTS_CHILD"

// The new root has @config and @home mounted below it, as the installer
// mounts them below /mnt and attach clones them. The helper's walks never
// cross a mount, so each mount point is entered on its own: here with real
// mounts, in a user namespace. Skipped where user namespaces are off.
func TestNewRootMounts(t *testing.T) {
	if os.Getenv(mountsChild) != "" {
		t.Skip("parent test only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNewRootMountsChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), mountsChild+"="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC) {
		t.Skipf("no user namespaces here: %v", err)
	}
	if err != nil || !strings.Contains(string(out), "--- PASS: TestNewRootMountsChild") {
		t.Fatalf("namespace child: %v\n%s", err, out)
	}
}

func TestNewRootMountsChild(t *testing.T) {
	dir := os.Getenv(mountsChild)
	if dir == "" {
		t.Skip("namespace child only")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	tmpfs := func(t *testing.T, at string) {
		t.Helper()
		mkdirs(t, at)
		if err := unix.Mount("tmpfs", at, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=755"); err != nil {
			t.Fatal(err)
		}
	}

	root := filepath.Join(dir, "new")
	mkdirs(t, root)
	tmpfs(t, filepath.Join(root, newCheckout))
	mkdirs(t, filepath.Join(root, newCheckout, ".git/objects/pack"), filepath.Join(root, newCheckout, ".git/refs/heads"))
	if err := os.WriteFile(filepath.Join(root, newCheckout, ".git/HEAD"), []byte("ref: refs/heads/dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newRoot := openTest(t, root)
	if _, err := newRoot.Sub(newCheckout + "/.git"); !errors.Is(err, safefs.ErrNotPlain) {
		t.Fatalf("a plain walk crossed the @config mount: %v", err)
	}
	if err := newGitDir(newRoot); err != nil {
		t.Fatalf("mounted checkout refused: %v", err)
	}

	// A mount on the way to the checkout is not the installer's layout.
	odd := filepath.Join(dir, "odd")
	tmpfs(t, filepath.Join(odd, "etc"))
	mkdirs(t, filepath.Join(odd, newCheckout, ".git"))
	if err := newGitDir(openTest(t, odd)); err == nil {
		t.Error("checkout below a mount on the way accepted")
	}
}

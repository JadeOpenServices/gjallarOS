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

// newDisk lays out a new disk as the installer mounts it, with tmpfs in
// place of btrfs: one file system at dir/<name>.disk holds @, @home and
// @config, bound on dir/<name>, its home and its checkout. The mount table
// then shows the three on one file system with the roots /@, /@home and
// /@config, as for the btrfs subvolumes. Called in a namespace child.
func newDisk(t *testing.T, dir, name string) (root, disk string) {
	t.Helper()
	was := newDiskFS
	newDiskFS = "tmpfs"
	t.Cleanup(func() { newDiskFS = was })
	disk = filepath.Join(dir, name+".disk")
	mkdirs(t, disk)
	if err := unix.Mount("tmpfs", disk, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=755"); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, filepath.Join(disk, "@/home"), filepath.Join(disk, "@", newCheckout), filepath.Join(disk, "@home"), filepath.Join(disk, "@config"))
	root = filepath.Join(dir, name)
	mkdirs(t, root)
	bind(t, filepath.Join(disk, "@"), root)
	bind(t, filepath.Join(disk, "@home"), filepath.Join(root, "home"))
	bind(t, filepath.Join(disk, "@config"), filepath.Join(root, newCheckout))
	return root, disk
}

func bind(t *testing.T, from, to string) {
	t.Helper()
	if err := unix.Mount(from, to, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
}

func tmpfsAt(t *testing.T, at string) {
	t.Helper()
	mkdirs(t, at)
	if err := unix.Mount("tmpfs", at, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=755"); err != nil {
		t.Fatal(err)
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
	gitDir := func(t *testing.T, co string) {
		t.Helper()
		mkdirs(t, filepath.Join(co, ".git/objects/pack"), filepath.Join(co, ".git/refs/heads"))
		if err := os.WriteFile(filepath.Join(co, ".git/HEAD"), []byte("ref: refs/heads/dev\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	root, _ := newDisk(t, dir, "new")
	gitDir(t, filepath.Join(root, newCheckout))
	newRoot := openTest(t, root)
	if _, err := newRoot.Sub(newCheckout + "/.git"); !errors.Is(err, safefs.ErrNotPlain) {
		t.Fatalf("a plain walk crossed the @config mount: %v", err)
	}
	if err := newGitDir(newRoot); err != nil {
		t.Fatalf("mounted checkout refused: %v", err)
	}
	was := newDiskFS
	newDiskFS = "btrfs"
	if err := newGitDir(newRoot); err == nil || !strings.Contains(err.Error(), "as the installer mounts it") {
		t.Errorf("new root of another file system type: %v", err)
	}
	newDiskFS = was

	// A mount on the way to the checkout is not the installer's layout.
	odd := filepath.Join(dir, "odd")
	tmpfsAt(t, filepath.Join(odd, "etc"))
	mkdirs(t, filepath.Join(odd, newCheckout, ".git"))
	if err := newGitDir(openTest(t, odd)); err == nil {
		t.Error("checkout below a mount on the way accepted")
	}

	// Any mount root at the checkout is not enough (L156 H2): another file
	// system, or a folder of the new disk that is not @config, such as the
	// new root's own etc.
	root, _ = newDisk(t, dir, "foreign")
	tmpfsAt(t, filepath.Join(root, newCheckout))
	gitDir(t, filepath.Join(root, newCheckout))
	if err := newGitDir(openTest(t, root)); err == nil || !strings.Contains(err.Error(), "not on the new disk") {
		t.Errorf("another file system at the checkout: %v", err)
	}
	root, disk := newDisk(t, dir, "ownetc")
	gitDir(t, filepath.Join(disk, "@", newCheckout))
	bind(t, filepath.Join(disk, "@", newCheckout), filepath.Join(root, newCheckout))
	if err := newGitDir(openTest(t, root)); err == nil || !strings.Contains(err.Error(), "not the @config subvolume") {
		t.Errorf("the new root's own folder at the checkout: %v", err)
	}
}

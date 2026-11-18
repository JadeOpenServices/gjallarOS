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

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

// keyringRel is the login keyring in a home.
const keyringRel = ".local/share/keyrings/login.keyring"

// homeChild names the folder the namespace child of TestNewHome works in;
// set only in that child.
const homeChild = "GJ_MOVE_HOME_CHILD"

// oldHomeTop makes an old disk top with @home/example: a file, a Nextcloud
// folder named in the client's config, a link to a folder outside and a
// FIFO. As root (the VM run) the home goes to a normal account, as on a
// real disk.
func oldHomeTop(t *testing.T) (top string, uid int) {
	t.Helper()
	uid = os.Getuid()
	if os.Geteuid() != 0 && (uid < minUserID || uid > maxUserID) {
		t.Skip("the fixture home needs an account id between 1000 and 59999")
	}
	top = t.TempDir()
	home := filepath.Join(top, "@home/example")
	files := map[string]string{
		"docs/a":                          "note",
		"Nextcloud/big":                   strings.Repeat("n", 1000),
		".config/Nextcloud/nextcloud.cfg": "[Accounts]\n0\\Folders\\1\\localPath=/home/example/Nextcloud/\n",
	}
	for name, data := range files {
		mkdirs(t, filepath.Dir(filepath.Join(home, name)))
		if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(home, "Linked")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(home, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		uid = minUserID
		err := filepath.Walk(home, func(p string, _ os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(p, uid, usersGID)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return top, uid
}

func openOldHome(t *testing.T, top string) *freespace.OldHome {
	t.Helper()
	h, err := freespace.OpenHome(openTest(t, top), "example")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// The sync folders stay behind unless asked for; a link is copied as a
// link, a FIFO is never copied, and a copy over the budget is taken back
// whole.
func TestCopyHome(t *testing.T) {
	top, uid := oldHomeTop(t)
	// The login keyring goes as bytes with the home (L66): root never opens
	// it, and the user session unlocks or re-keys it after the move. The
	// bytes here are arbitrary, not a keyring.
	ring := filepath.Join(top, "@home/example", keyringRel)
	ringData := []byte{0, 1, 2, 0xfe, 0xff, '\n', 'k'}
	mkdirs(t, filepath.Dir(ring))
	if err := os.WriteFile(ring, ringData, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		for p := ring; p != filepath.Join(top, "@home/example"); p = filepath.Dir(p) {
			if err := os.Lchown(p, uid, usersGID); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := openOldHome(t, top)

	dir := t.TempDir()
	res, err := copyHome(old, openTest(t, dir), uid, usersGID, false, 1<<20)
	if err != nil || res.Failed != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Left != 1 || res.Skipped != 1 || res.Links != 1 {
		t.Errorf("without sync: %+v", res)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "docs/a")); err != nil || string(got) != "note" {
		t.Errorf("docs/a: %q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "Nextcloud")); !os.IsNotExist(err) {
		t.Errorf("the sync folder came along: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, keyringRel)); err != nil || string(got) != string(ringData) {
		t.Errorf("keyring: %q %v", got, err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, keyringRel)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("keyring mode: %v %v", fi, err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "Linked")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Linked: %v %v", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "pipe")); !os.IsNotExist(err) {
		t.Errorf("the FIFO was copied: %v", err)
	}

	dir = t.TempDir()
	res, err = copyHome(old, openTest(t, dir), uid, usersGID, true, 1<<20)
	if err != nil || res.Failed != "" || res.Left != 0 {
		t.Fatalf("with sync: %+v %v", res, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "Nextcloud/big")); err != nil || len(got) != 1000 {
		t.Errorf("Nextcloud/big: %d %v", len(got), err)
	}

	dir = t.TempDir()
	res, err = copyHome(old, openTest(t, dir), uid, usersGID, true, 600)
	if err != nil || res.Failed == "" {
		t.Fatalf("over the budget: %+v %v", res, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("not taken back: %v", entries)
	}

	// A budget that runs out halfway (review T5): what was written before
	// is taken back too.
	dir = t.TempDir()
	res, err = copyHome(old, openTest(t, dir), uid, usersGID, true, 4*safefs.EntryCost)
	if err != nil || res.Failed == "" || res.Files+res.Folders == 0 {
		t.Fatalf("halfway: %+v %v", res, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("halfway copy not taken back: %v", entries)
	}
}

// A link planted as the old home is not followed.
func TestOpenHomeRefusesLink(t *testing.T) {
	top := t.TempDir()
	mkdirs(t, filepath.Join(top, "@home"), filepath.Join(top, "elsewhere"))
	if err := os.Symlink(filepath.Join(top, "elsewhere"), filepath.Join(top, "@home/example")); err != nil {
		t.Fatal(err)
	}
	if _, err := freespace.OpenHome(openTest(t, top), "example"); !errors.Is(err, freespace.ErrNoHome) {
		t.Fatalf("a linked old home: %v", err)
	}
}

// home on the new root is the @home mount: a plain folder there is not the
// installer's layout.
func TestNewHomeNeedsMount(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, filepath.Join(root, "home/example"))
	if _, _, err := newHome(openTest(t, root), "example"); !errors.Is(err, safefs.ErrNotMount) {
		t.Fatalf("plain home folder: %v", err)
	}
	if _, _, err := newHome(openTest(t, root), "../x"); err == nil {
		t.Fatal("odd user name accepted")
	}
}

// newHome on a real mount, in a user namespace where the test runs as uid
// 1000 in group users and keeps the right to mount. Skipped where user
// namespaces are off.
func TestNewHome(t *testing.T) {
	if os.Getenv(homeChild) != "" {
		t.Skip("parent test only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNewHomeChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), homeChild+"="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: minUserID, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: usersGID, HostID: os.Getgid(), Size: 1}},
		AmbientCaps: []uintptr{unix.CAP_SYS_ADMIN},
	}
	out, err := cmd.CombinedOutput()
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC) {
		t.Skipf("no user namespaces here: %v", err)
	}
	if err != nil || !strings.Contains(string(out), "--- PASS: TestNewHomeChild") {
		t.Fatalf("namespace child: %v\n%s", err, out)
	}
}

func TestNewHomeChild(t *testing.T) {
	dir := os.Getenv(homeChild)
	if dir == "" {
		t.Skip("namespace child only")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	// Only this uid is mapped, so it stands in for root as the owner of
	// the home mount.
	was := rootUID
	rootUID = uint32(os.Getuid())
	t.Cleanup(func() { rootUID = was })
	newRoot := func(t *testing.T, name string) string {
		t.Helper()
		root, _ := newDisk(t, dir, name)
		return root
	}

	root := newRoot(t, "new")
	if err := os.Mkdir(filepath.Join(root, "home/example"), 0o700); err != nil {
		t.Fatal(err)
	}
	dst, uid, err := newHome(openTest(t, root), "example")
	if err != nil || uid != os.Getuid() {
		t.Fatalf("the installer's home refused: %d %v", uid, err)
	}
	t.Cleanup(func() { dst.Close() })
	top, _ := oldHomeTop(t)
	if res, err := copyHome(openOldHome(t, top), dst, uid, usersGID, false, 1<<20); err != nil || res.Failed != "" || res.Files != 2 {
		t.Fatalf("copy into the mounted home: %+v %v", res, err)
	}
	if _, _, err := newHome(openTest(t, root), "example"); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("a home with files in it: %v", err)
	}

	// home not root's alone: another owner, or writable by the group.
	rootUID = was
	if _, _, err := newHome(openTest(t, root), "example"); err == nil || !strings.Contains(err.Error(), "root's alone") {
		t.Errorf("home of another owner: %v", err)
	}
	rootUID = uint32(os.Getuid())
	odd := newRoot(t, "groupwritable")
	mkdirs(t, filepath.Join(odd, "home/example"))
	if err := os.Chmod(filepath.Join(odd, "home"), 0o775); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newHome(openTest(t, odd), "example"); err == nil || !strings.Contains(err.Error(), "root's alone") {
		t.Errorf("group-writable home: %v", err)
	}

	// A link or a file planted as the user's home is refused, never
	// followed.
	odd = newRoot(t, "planted")
	outside := filepath.Join(dir, "outside")
	mkdirs(t, outside)
	if err := os.Symlink(outside, filepath.Join(odd, "home/example")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odd, "home/other"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"example", "other", "missing"} {
		if _, _, err := newHome(openTest(t, odd), name); err == nil {
			t.Errorf("planted %s accepted", name)
		}
	}

	// A relative link as home to the real @home mounted elsewhere in the
	// new root is not followed (L156 T2): it would pass every other check.
	odd, disk := newDisk(t, dir, "rellink")
	if err := unix.Unmount(filepath.Join(odd, "home"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(odd, "home")); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, filepath.Join(odd, "mnt"), filepath.Join(disk, "@home/example"))
	bind(t, filepath.Join(disk, "@home"), filepath.Join(odd, "mnt"))
	if err := os.Symlink("mnt", filepath.Join(odd, "home")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newHome(openTest(t, odd), "example"); !errors.Is(err, safefs.ErrNotPlain) {
		t.Errorf("home as a link to the mount: %v", err)
	}

	// A mount on the way: home itself is not a mount, its parent is.
	odd = filepath.Join(dir, "deep")
	tmpfsAt(t, odd)
	mkdirs(t, filepath.Join(odd, "home/example"))
	if _, _, err := newHome(openTest(t, filepath.Join(dir, "deep")), "example"); !errors.Is(err, safefs.ErrNotMount) {
		t.Errorf("home not a mount of its own: %v", err)
	}

	// Any mount root at home is not enough (L156 H2): another file system,
	// a folder of the new disk that is not @home, or a new root that is not
	// the disk's @.
	odd = newRoot(t, "foreign")
	tmpfsAt(t, filepath.Join(odd, "home"))
	mkdirs(t, filepath.Join(odd, "home/example"))
	if _, _, err := newHome(openTest(t, odd), "example"); err == nil || !strings.Contains(err.Error(), "not on the new disk") {
		t.Errorf("another file system at home: %v", err)
	}
	odd, disk = newDisk(t, dir, "wrongsub")
	mkdirs(t, filepath.Join(disk, "@config/example"))
	bind(t, filepath.Join(disk, "@config"), filepath.Join(odd, "home"))
	if _, _, err := newHome(openTest(t, odd), "example"); err == nil || !strings.Contains(err.Error(), "not the @home subvolume") {
		t.Errorf("@config at home: %v", err)
	}
	_, disk = newDisk(t, dir, "whole")
	mkdirs(t, filepath.Join(disk, "home"), filepath.Join(disk, "@home/example"))
	bind(t, filepath.Join(disk, "@home"), filepath.Join(disk, "home"))
	if _, _, err := newHome(openTest(t, disk), "example"); err == nil || !strings.Contains(err.Error(), "as the installer mounts it") {
		t.Errorf("the whole disk as the new root: %v", err)
	}
}

const homeOwnerChild = "GJ_MOVE_HOME_OWNER_CHILD"

// The new home must belong to a normal account in the users group (L152,
// review T3/T4): run as a Nix build account, as root or in another group,
// the namespace child makes a home that newHome refuses. Each id is real
// in the child, so no check can be skipped by a test seam.
func TestNewHomeOwner(t *testing.T) {
	if os.Getenv(homeOwnerChild) != "" {
		t.Skip("parent test only")
	}
	for _, c := range []struct {
		name     string
		uid, gid int
		want     string
	}{
		{"nixbld", nixbldUID + 1, usersGID, "not a normal user"},
		{"root", 0, usersGID, "not a normal user"},
		{"group", minUserID, usersGID + 1, "not users"},
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNewHomeOwnerChild$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), homeOwnerChild+"="+t.TempDir()+"|"+c.want)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: c.uid, HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: c.gid, HostID: os.Getgid(), Size: 1}},
			AmbientCaps: []uintptr{unix.CAP_SYS_ADMIN},
		}
		out, err := cmd.CombinedOutput()
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC) {
			t.Skipf("no user namespaces here: %v", err)
		}
		if err != nil || !strings.Contains(string(out), "--- PASS: TestNewHomeOwnerChild") {
			t.Errorf("%s: namespace child: %v\n%s", c.name, err, out)
		}
	}
}

func TestNewHomeOwnerChild(t *testing.T) {
	dir, want, ok := strings.Cut(os.Getenv(homeOwnerChild), "|")
	if !ok {
		t.Skip("namespace child only")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	was := rootUID
	rootUID = uint32(os.Getuid())
	t.Cleanup(func() { rootUID = was })
	root, _ := newDisk(t, dir, "new")
	if err := os.Mkdir(filepath.Join(root, "home/example"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newHome(openTest(t, root), "example"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("uid %d gid %d: %v, want %q", os.Getuid(), os.Getgid(), err, want)
	}
}

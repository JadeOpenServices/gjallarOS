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
)

// usernsChild names the folder the namespace child of TestPrivateBase works
// in; set only in that child.
const usernsChild = "GJ_MOVE_USERNS_CHILD"

// L153: in namespaces of its own, with a shared /run and a peer of it the
// way a service's namespace is, nothing the helper mounts below base shows
// up at the peer. Skipped where user namespaces are off.
func TestPrivateBase(t *testing.T) {
	if os.Getenv(usernsChild) != "" {
		t.Skip("parent test only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrivateBaseChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), usernsChild+"="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC) {
		t.Skipf("no user namespaces here: %v", err)
	}
	if err != nil || !strings.Contains(string(out), "--- PASS: TestPrivateBaseChild") {
		t.Fatalf("namespace child: %v\n%s", err, out)
	}
}

func TestPrivateBaseChild(t *testing.T) {
	dir := os.Getenv(usernsChild)
	if dir == "" {
		t.Skip("runs only inside TestPrivateBase")
	}
	run, peer := filepath.Join(dir, "run"), filepath.Join(dir, "peer")
	tmpfsMode := func(p, mode string) {
		t.Helper()
		mkdirs(t, p)
		if err := unix.Mount("tmpfs", p, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode="+mode); err != nil {
			t.Fatalf("mount %s: %v", p, err)
		}
	}
	tmpfs := func(p string) { t.Helper(); tmpfsMode(p, "755") }
	mountedAt := func(p string) bool {
		t.Helper()
		m, err := mountRootAt(p)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	tmpfs(run)
	if err := unix.Mount("", run, "", unix.MS_SHARED, ""); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, peer)
	if err := unix.Mount(run, peer, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	// Control: a plain folder below the shared /run leaks to the peer.
	tmpfs(filepath.Join(run, "ctl"))
	if !mountedAt(filepath.Join(peer, "ctl")) {
		t.Fatal("control mount did not reach the peer: the test proves nothing")
	}
	if err := checkPrivate(run); err == nil {
		t.Error("shared /run passed checkPrivate")
	}

	base := filepath.Join(run, "gjallar-move")
	unlock, err := lock(base)
	if err != nil {
		t.Fatal(err)
	}
	if !mountedAt(base) {
		t.Error("base is not a mount after lock")
	}
	// The old disk, as openOp mounts it.
	tmpfs(filepath.Join(base, Old))
	if err := checkPrivate(base); err != nil {
		t.Error(err)
	}
	if mountedAt(filepath.Join(peer, "gjallar-move", Old)) {
		t.Error("old disk mount reached the peer")
	}
	// The new root, as attach moves it.
	target := filepath.Join(dir, "mnt")
	tmpfs(target)
	if err := attach(base, target); err != nil {
		t.Fatal(err)
	}
	if !mountedAt(filepath.Join(base, New)) {
		t.Fatal("new root not attached")
	}
	if mountedAt(filepath.Join(peer, "gjallar-move", New)) {
		t.Error("attached new root reached the peer")
	}
	if err := detachOp(base); err != nil {
		t.Fatal(err)
	}
	if err := unix.Unmount(filepath.Join(base, Old), 0); err != nil {
		t.Fatal(err)
	}
	unlock()

	// A second step finds base already private and goes on.
	unlock, err = lock(base)
	if err != nil {
		t.Fatalf("second lock: %v", err)
	}
	unlock()

	// A mount left below a base that is no mount of its own: the bind
	// would hide it, so lock refuses.
	stale := filepath.Join(run, "stale")
	if err := Dirs(stale); err != nil {
		t.Fatal(err)
	}
	// Mode 700, as Dirs wants it: only the mount stops lock.
	tmpfsMode(filepath.Join(stale, Old), "700")
	if _, err := lock(stale); err == nil || !strings.Contains(err.Error(), "earlier run") {
		t.Errorf("mount below a plain base: %v", err)
	}
}

func TestParseMountinfo(t *testing.T) {
	data := strings.Join([]string{
		"22 1 0:20 / /run rw,nosuid shared:5 - tmpfs tmpfs rw",
		"40 22 0:20 /gjallar-move /run/gjallar-move rw - tmpfs tmpfs rw",
		"41 40 8:1 / /run/gjallar-move/old rw,nosuid - ext4 /dev/sda1 rw",
		"42 40 8:2 / /run/gjallar-move/new rw master:7 - ext4 /dev/sda2 rw",
		"43 22 0:30 / /run/gjallar-move2 rw shared:9 - tmpfs tmpfs rw",
		`44 40 0:31 / /run/gjallar-move/old/a\040b rw shared:3 master:2 - tmpfs tmpfs rw`,
	}, "\n") + "\n"
	got, err := parseMountinfo([]byte(data), "/run/gjallar-move")
	if err != nil {
		t.Fatal(err)
	}
	want := []mountEntry{
		{"/run/gjallar-move", false},
		{"/run/gjallar-move/old", false},
		{"/run/gjallar-move/new", true},
		{"/run/gjallar-move/old/a b", true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: %v, want %v", i, got[i], want[i])
		}
	}
	for _, bad := range []string{
		"22 1 0:20 / /run rw shared:5 tmpfs tmpfs rw\n",
		"22 1 0:20 / /run\n",
		"- 1 0:20 / /run rw\n",
	} {
		if _, err := parseMountinfo([]byte(bad), "/run"); err == nil {
			t.Errorf("malformed line accepted: %q", bad)
		}
	}
}

func TestUnescapeMount(t *testing.T) {
	for in, want := range map[string]string{
		`/a\040b`:  "/a b",
		`/a\011b`:  "/a\tb",
		`/a\134b`:  `/a\b`,
		`/a\04`:    `/a\04`,
		`/a\9xx`:   `/a\9xx`,
		`/plain`:   "/plain",
		`/end\040`: "/end ",
		`/a\777b`:  `/a\777b`,
	} {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
}

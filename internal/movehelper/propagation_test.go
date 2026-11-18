package movehelper

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// usernsChild names the folder the namespace child of TestIsolateBase works
// in; set only in that child and in its namespace helpers.
const usernsChild = "GJ_MOVE_USERNS_CHILD"

// nsHelper is set only in a namespace helper of the child: "before" keeps a
// plain copy of the child's mounts, "service" turns its copy into what
// systemd gives a namespaced unit (rslave, then rshared).
const nsHelper = "GJ_MOVE_NS_HELPER"

// L153, L154: in namespaces of its own, with a shared /run, a peer of it in
// the same namespace and other namespaces the way services have them,
// nothing the helper mounts below base reaches a namespace that existed
// before the first lock; a namespace made later gets the helper's mounts
// and its unmounts too, and sends nothing back. Skipped where user
// namespaces are off.
func TestIsolateBase(t *testing.T) {
	if os.Getenv(usernsChild) != "" {
		t.Skip("parent test only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestIsolateBaseChild$", "-test.count=1", "-test.v")
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
	if err != nil || !strings.Contains(string(out), "--- PASS: TestIsolateBaseChild") {
		t.Fatalf("namespace child: %v\n%s", err, out)
	}
}

// nsProc is a namespace helper: another mount namespace in the child's
// user namespace, asked over its stdin.
type nsProc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func startNS(t *testing.T, mode string) *nsProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestIsolateBaseHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), nsHelper+"="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &nsProc{cmd, in, bufio.NewReader(out)}
	t.Cleanup(func() { p.in.Close(); p.cmd.Wait() })
	if got := p.answer(t); got != "ready" {
		t.Fatalf("namespace helper %s: %q", mode, got)
	}
	return p
}

func (p *nsProc) answer(t *testing.T) string {
	t.Helper()
	for {
		line, err := p.out.ReadString('\n')
		if err != nil {
			t.Fatalf("namespace helper: %v", err)
		}
		if a, ok := strings.CutPrefix(strings.TrimSpace(line), "GJ:"); ok {
			return a
		}
	}
}

func (p *nsProc) ask(t *testing.T, cmd, path string) string {
	t.Helper()
	if _, err := fmt.Fprintf(p.in, "%s %s\n", cmd, path); err != nil {
		t.Fatal(err)
	}
	return p.answer(t)
}

func (p *nsProc) mounted(t *testing.T, path string) bool {
	t.Helper()
	switch a := p.ask(t, "mounted", path); a {
	case "yes":
		return true
	case "no":
		return false
	default:
		t.Fatalf("namespace helper: %s", a)
		return false
	}
}

func TestIsolateBaseHelper(t *testing.T) {
	mode := os.Getenv(nsHelper)
	if mode == "" {
		t.Skip("runs only as a namespace helper")
	}
	if mode == "service" {
		for _, prop := range []uintptr{unix.MS_SLAVE, unix.MS_SHARED} {
			if err := unix.Mount("", "/", "", prop|unix.MS_REC, ""); err != nil {
				fmt.Println("GJ:setup", err)
				return
			}
		}
	}
	fmt.Println("GJ:ready")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		cmd, path, _ := strings.Cut(sc.Text(), " ")
		switch cmd {
		case "mounted":
			m, err := mountRootAt(path)
			switch {
			case err != nil:
				fmt.Println("GJ:", err)
			case m:
				fmt.Println("GJ:yes")
			default:
				fmt.Println("GJ:no")
			}
		case "mount":
			if err := unix.Mount("tmpfs", path, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=755"); err != nil {
				fmt.Println("GJ:", err)
			} else {
				fmt.Println("GJ:ok")
			}
		}
	}
}

func TestIsolateBaseChild(t *testing.T) {
	dir := os.Getenv(usernsChild)
	if dir == "" || os.Getenv(nsHelper) != "" {
		t.Skip("runs only inside TestIsolateBase")
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
	if err := checkBase(run); err == nil {
		t.Error("/run with a peer outside it passed checkBase")
	}

	base := filepath.Join(run, "gjallar-move")
	old, newDir := filepath.Join(base, Old), filepath.Join(base, New)
	// A namespace that exists before the first lock, a plain copy that
	// receives everything /run's group gets.
	before := startNS(t, "before")
	unlock, err := lock(base)
	if err != nil {
		t.Fatal(err)
	}
	if !mountedAt(base) {
		t.Error("base is not a mount after lock")
	}
	// A service started during the move.
	service := startNS(t, "service")
	// The old disk, as openOp mounts it.
	tmpfs(old)
	if err := checkBase(base); err != nil {
		t.Error(err)
	}
	if mountedAt(filepath.Join(peer, "gjallar-move", Old)) {
		t.Error("old disk mount reached the peer")
	}
	if before.mounted(t, old) {
		t.Error("old disk mount reached a namespace made before the lock")
	}
	if !service.mounted(t, old) {
		t.Fatal("old disk mount did not reach a namespace made during the move: the unmount check proves nothing")
	}
	// The new root, as attach moves it.
	target := filepath.Join(dir, "mnt")
	tmpfs(target)
	// On the stick /mnt is shared: give it a peer, so that a copy attach
	// left shared would fail checkBase (c7 S1).
	if err := unix.Mount("", target, "", unix.MS_SHARED, ""); err != nil {
		t.Fatal(err)
	}
	mntPeer := filepath.Join(dir, "mntpeer")
	mkdirs(t, mntPeer)
	if err := unix.Mount(target, mntPeer, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	if err := attach(base, target); err != nil {
		t.Fatal(err)
	}
	tmpfs(filepath.Join(newDir, "boot"))
	if mountedAt(filepath.Join(mntPeer, "boot")) || mountedAt(filepath.Join(target, "boot")) {
		t.Error("a mount below the attached new root reached /mnt or its peer")
	}
	if err := unix.Unmount(filepath.Join(newDir, "boot"), 0); err != nil {
		t.Fatal(err)
	}
	if !mountedAt(newDir) {
		t.Fatal("new root not attached")
	}
	if mountedAt(filepath.Join(peer, "gjallar-move", New)) {
		t.Error("attached new root reached the peer")
	}
	if before.mounted(t, newDir) {
		t.Error("attached new root reached a namespace made before the lock")
	}
	// L154: a service started with both disks mounted copies them.
	late := startNS(t, "service")
	if !late.mounted(t, old) || !late.mounted(t, newDir) {
		t.Fatal("a namespace made with the disks mounted has no copy: the unmount check proves nothing")
	}
	if err := detachOp(base); err != nil {
		t.Fatal(err)
	}
	for name, ns := range map[string]*nsProc{"during": service, "late": late} {
		if ns.mounted(t, newDir) {
			t.Errorf("detach did not reach the %s namespace", name)
		}
	}
	if err := unix.Unmount(old, 0); err != nil {
		t.Fatal(err)
	}
	// The unmount reaches the services' copies, so the disk is free.
	for name, ns := range map[string]*nsProc{"during": service, "late": late} {
		if ns.mounted(t, old) {
			t.Errorf("unmount did not reach the %s namespace", name)
		}
	}
	// Nothing comes back from the service's copy.
	if a := service.ask(t, "mount", newDir); a != "ok" {
		t.Fatalf("mount in the service namespace: %s", a)
	}
	if mountedAt(newDir) {
		t.Error("a mount made in a service namespace reached base")
	}
	if err := checkBase(base); err != nil {
		t.Error(err)
	}
	unlock()

	// A second step finds base set up and goes on.
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

	// A base left half done: bound on itself but still in /run's group, or
	// private after a crash before the shared step. Later locks refuse. A
	// slave of /run's group that has a group of its own as well is refused
	// by the master check alone (c7 S2).
	for name, prop := range map[string]uintptr{"run-group": 0, "private": unix.MS_PRIVATE, "slave-shared": unix.MS_SLAVE} {
		half := filepath.Join(run, "half-"+name)
		if err := Dirs(half); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mount(half, half, "", unix.MS_BIND, ""); err != nil {
			t.Fatal(err)
		}
		if prop != 0 {
			if err := unix.Mount("", half, "", prop, ""); err != nil {
				t.Fatal(err)
			}
		}
		if prop == unix.MS_SLAVE {
			if err := unix.Mount("", half, "", unix.MS_SHARED, ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := lock(half); err == nil || !strings.Contains(err.Error(), restart) {
			t.Errorf("half done base %s: %v", name, err)
		}
	}
}

func TestParseMountinfo(t *testing.T) {
	data := strings.Join([]string{
		"22 1 0:20 / /run rw,nosuid shared:5 - tmpfs tmpfs rw",
		"40 22 0:20 /gjallar-move /run/gjallar-move rw - tmpfs tmpfs rw",
		"41 40 8:1 / /run/gjallar-move/old rw,nosuid - ext4 /dev/sda1 rw",
		"42 40 8:2 / /run/gjallar-move/new rw master:7 - ext4 /dev/sda2 rw",
		"43 22 0:30 / /run/gjallar-move2 rw shared:9 - tmpfs tmpfs rw",
		`44 40 0:31 / /run/gjallar-move/old/a\040b rw shared:3 master:2 - tmpfs tmp\040fs rw`,
	}, "\n") + "\n"
	got, err := parseMountinfo([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []mountEntry{
		{id: 22, dev: "0:20", root: "/", point: "/run", group: 5, fstype: "tmpfs", source: "tmpfs"},
		{id: 40, dev: "0:20", root: "/gjallar-move", point: "/run/gjallar-move", fstype: "tmpfs", source: "tmpfs"},
		{id: 41, dev: "8:1", root: "/", point: "/run/gjallar-move/old", fstype: "ext4", source: "/dev/sda1"},
		{id: 42, dev: "8:2", root: "/", point: "/run/gjallar-move/new", master: true, fstype: "ext4", source: "/dev/sda2"},
		{id: 43, dev: "0:30", root: "/", point: "/run/gjallar-move2", group: 9, fstype: "tmpfs", source: "tmpfs"},
		{id: 44, dev: "0:31", root: "/", point: "/run/gjallar-move/old/a b", group: 3, master: true, fstype: "tmpfs", source: "tmp fs"},
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
		"22 1 0:20 / /run rw shared:x - tmpfs tmpfs rw\n",
		"22 1 0:20 / /run rw shared:0 - tmpfs tmpfs rw\n",
		"22 1 0:20 / /run rw shared:5 - tmpfs\n",
		"x 1 0:20 / /run rw shared:5 - tmpfs tmpfs rw\n",
	} {
		if _, err := parseMountinfo([]byte(bad)); err == nil {
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

func TestBelow(t *testing.T) {
	for point, want := range map[string]bool{
		"/run/gjallar-move":      true,
		"/run/gjallar-move/old":  true,
		"/run/gjallar-move2":     false,
		"/run/gjallar-move2/old": false,
		"/run":                   false,
		"/run/gjallar-mov":       false,
	} {
		if below(point, "/run/gjallar-move") != want {
			t.Errorf("below(%q) = %v", point, !want)
		}
	}
}

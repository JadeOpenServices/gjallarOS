package safefs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func mustDir(t *testing.T, path string) *Dir {
	t.Helper()
	d, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withTimeout fails the test when f hangs: a pipe must never block a read.
func withTimeout(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hung")
	}
}

// L59: the read side on an old-disk fixture.
func TestReadFileRefusesPlantedOldDisk(t *testing.T) {
	old := t.TempDir()
	write(t, filepath.Join(old, "home/x/NetworkManager/system-connections/a.nmconnection"), "planted")
	if err := os.Symlink("home/x", filepath.Join(old, "etc")); err != nil {
		t.Fatal(err)
	}
	sc := filepath.Join(old, "var/lib/NetworkManager/system-connections")
	if err := os.MkdirAll(sc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(sc, "home.nmconnection"), 0o644); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(old, "user.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(2 << 30); err != nil {
		t.Fatal(err)
	}
	big.Close()
	write(t, filepath.Join(old, "ok.json"), "{}")
	if err := os.Symlink("/dev/zero", filepath.Join(old, "zero")); err != nil {
		t.Fatal(err)
	}

	d := mustDir(t, old)
	cases := []struct {
		rel  string
		want error
	}{
		{"etc/NetworkManager/system-connections/a.nmconnection", ErrNotPlain},
		{"var/lib/NetworkManager/system-connections/home.nmconnection", ErrNotPlain},
		{"user.config.json", ErrTooBig},
		{"zero", ErrNotPlain},
		{"../" + filepath.Base(old) + "/ok.json", ErrNotPlain},
		{"/etc/passwd", ErrNotPlain},
		{"/proc/self/root/etc/passwd", ErrNotPlain},
	}
	for _, c := range cases {
		withTimeout(t, func() {
			_, err := d.ReadFile(c.rel, 1<<20)
			if !errors.Is(err, c.want) {
				t.Errorf("%s: got %v, want %v", c.rel, err, c.want)
			}
		})
	}
	got, err := d.ReadFile("ok.json", 1<<20)
	if err != nil || string(got) != "{}" {
		t.Fatalf("plain file: %q %v", got, err)
	}
}

func TestReadFileRefusesDevice(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("making a device node needs root")
	}
	old := t.TempDir()
	if err := unix.Mknod(filepath.Join(old, "tty"), unix.S_IFCHR|0o600, int(unix.Mkdev(5, 0))); err != nil {
		t.Fatal(err)
	}
	withTimeout(t, func() {
		if _, err := mustDir(t, old).ReadFile("tty", 1<<20); !errors.Is(err, ErrNotPlain) {
			t.Errorf("device: %v", err)
		}
	})
}

// fixtureUID owns the source trees: the test user, or a normal user when
// the tests run as root (the copy refuses files of root).
func fixtureUID() int {
	if os.Geteuid() == 0 {
		return 1000
	}
	return os.Getuid()
}

func fixtureGID() int {
	if os.Geteuid() == 0 {
		return 100
	}
	return os.Getgid()
}

// own hands the tree at root to fixtureUID. It clears setuid bits and file
// capabilities, so tests call it before setting those.
func own(t *testing.T, root string) {
	t.Helper()
	if os.Geteuid() != 0 {
		return
	}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) == fixtureUID() {
			return nil // already handed over; chown again would clear setuid
		}
		return os.Lchown(p, fixtureUID(), fixtureGID())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string, budget int64, keep func(string) bool) (Stats, error) {
	t.Helper()
	own(t, src)
	var stats Stats
	var err error
	withTimeout(t, func() {
		stats, err = Copy(mustDir(t, src), mustDir(t, dst), CopyOptions{
			SourceUID: fixtureUID(), UID: fixtureUID(), GID: fixtureGID(), Budget: budget, KeepXattr: keep,
		})
	})
	return stats, err
}

// L61: the write side.
func TestCopyStripsAndCounts(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()

	sparse, err := os.Create(filepath.Join(src, "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sparse.Truncate(100 << 30); err != nil {
		t.Fatal(err)
	}
	mid := bytes.Repeat([]byte("x"), 1<<20)
	if _, err := sparse.WriteAt(mid, 50<<30); err != nil {
		t.Fatal(err)
	}
	sparse.Close()

	write(t, filepath.Join(src, "suid"), "#!/bin/sh\n")
	if err := unix.Mkfifo(filepath.Join(src, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "Nextcloud/doc.txt"), "hello")
	xattrs := unix.Setxattr(filepath.Join(src, "Nextcloud/doc.txt"), "user.openvfs.state", []byte("1"), 0) == nil
	if xattrs {
		if err := unix.Setxattr(filepath.Join(src, "Nextcloud/doc.txt"), "user.other", []byte("2"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(src, "suid"), filepath.Join(src, "suid2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	own(t, src)
	if err := unix.Chmod(filepath.Join(src, "suid"), 0o4755); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		// A file capability: version 2, cap_net_raw effective.
		capv := []byte{0, 0, 0, 2, 0, 0x20, 0, 0, 0, 0x20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		if err := unix.Setxattr(filepath.Join(src, "suid"), "security.capability", capv, 0); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(src, "Nextcloud/doc.txt"), old, old); err != nil {
		t.Fatal(err)
	}

	stats, err := copyTree(t, src, dst, 1<<30, func(n string) bool { return n == "user.openvfs.state" })
	if err != nil {
		t.Fatal(err)
	}

	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(dst, "sparse"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Size != 100<<30 {
		t.Errorf("sparse size %d", st.Size)
	}
	if st.Blocks*512 > 64<<20 {
		t.Errorf("sparse copy uses %d bytes on disk", st.Blocks*512)
	}
	if stats.Bytes > 16<<20 {
		t.Errorf("counted %d data bytes for a 1 MiB file with holes", stats.Bytes)
	}
	got := make([]byte, len(mid))
	f, _ := os.Open(filepath.Join(dst, "sparse"))
	if _, err := f.ReadAt(got, 50<<30); err != nil || !bytes.Equal(got, mid) {
		t.Errorf("sparse data lost: %v", err)
	}
	f.Close()

	for _, n := range []string{"suid", "suid2"} {
		fi, err := os.Stat(filepath.Join(dst, n))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || fi.Mode().Perm() != 0o755 {
			t.Errorf("%s mode %v", n, fi.Mode())
		}
		if _, err := unix.Getxattr(filepath.Join(dst, n), "security.capability", make([]byte, 64)); err == nil {
			t.Errorf("%s kept security.capability", n)
		}
	}
	if stats.SetIDStripped != 2 || stats.HardLinks != 2 {
		t.Errorf("stripped %d, hard links %d", stats.SetIDStripped, stats.HardLinks)
	}
	if _, err := os.Lstat(filepath.Join(dst, "fifo")); !os.IsNotExist(err) {
		t.Errorf("fifo copied: %v", err)
	}
	if stats.Skipped != 1 {
		t.Errorf("skipped %d", stats.Skipped)
	}
	if target, err := os.Readlink(filepath.Join(dst, "link")); err != nil || target != "/etc/shadow" {
		t.Errorf("link: %q %v", target, err)
	}

	doc := filepath.Join(dst, "Nextcloud/doc.txt")
	if fi, err := os.Stat(doc); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("mtime not kept: %v", err)
	}
	if xattrs {
		if _, err := unix.Getxattr(doc, "user.openvfs.state", make([]byte, 8)); err != nil {
			t.Errorf("openvfs attribute lost: %v", err)
		}
		if _, err := unix.Getxattr(doc, "user.other", make([]byte, 8)); err == nil {
			t.Errorf("user.other kept")
		}
		if stats.XattrsDropped < 1 {
			t.Errorf("dropped %d", stats.XattrsDropped)
		}
	}
}

func TestCopyStopsAtBudget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a"), string(bytes.Repeat([]byte("a"), 3<<20)))
	if _, err := copyTree(t, src, dst, 2<<20, nil); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("got %v", err)
	}
	// Many empty files count too.
	src2, dst2 := t.TempDir(), t.TempDir()
	for i := range 100 {
		write(t, filepath.Join(src2, "e", string(rune('a'+i%26))+string(rune('a'+i/26))), "")
	}
	if _, err := copyTree(t, src2, dst2, 50*EntryCost, nil); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("empty files: got %v", err)
	}
}

// Links planted in the target before the copy: nothing is written through
// them, and the copy stops.
func TestCopyRefusesPlantedTarget(t *testing.T) {
	outside := t.TempDir()
	for _, c := range []struct {
		name    string
		srcDir  bool
		planted string
	}{
		{"Documents", true, outside},
		{"notes.txt", false, filepath.Join(outside, "notes.txt")},
	} {
		src, dst := t.TempDir(), t.TempDir()
		if c.srcDir {
			write(t, filepath.Join(src, c.name, "secret"), "s")
		} else {
			write(t, filepath.Join(src, c.name), "s")
		}
		if err := os.Symlink(c.planted, filepath.Join(dst, c.name)); err != nil {
			t.Fatal(err)
		}
		if _, err := copyTree(t, src, dst, 1<<20, nil); !errors.Is(err, ErrExists) {
			t.Errorf("%s: got %v", c.name, err)
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Errorf("%s: wrote through the planted link: %v", c.name, entries)
		}
	}
}

// A folder swapped for a link in the source after it was listed is not
// followed.
func TestCopyNeverFollowsSourceLinks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "shadow"), "secret")
	if err := os.Symlink(outside, filepath.Join(src, "dir")); err != nil {
		t.Fatal(err)
	}
	stats, err := copyTree(t, src, dst, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Links != 1 || stats.Files != 0 {
		t.Errorf("stats %+v", stats)
	}
	fi, err := os.Lstat(filepath.Join(dst, "dir"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("source link not copied as a link: %v", err)
	}
}

func TestCopyDepthCap(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	deep := src
	for range MaxDepth + 2 {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := copyTree(t, src, dst, 1<<30, nil); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("got %v", err)
	}
}

// Files someone else left in the tree stay behind.
func TestCopySkipsForeign(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("giving a file to another user needs root")
	}
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "mine"), "m")
	write(t, filepath.Join(src, "root-only/secret"), "s")
	for _, p := range []string{src, filepath.Join(src, "mine")} {
		if err := os.Lchown(p, 1000, 100); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := Copy(mustDir(t, src), mustDir(t, dst), CopyOptions{SourceUID: 1000, UID: 1001, GID: 100, Budget: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Foreign != 1 || stats.Files != 1 {
		t.Errorf("stats %+v", stats)
	}
	if _, err := os.Lstat(filepath.Join(dst, "root-only")); !os.IsNotExist(err) {
		t.Errorf("foreign folder copied: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(dst, "mine"), &st); err != nil || st.Uid != 1001 {
		t.Errorf("owner %d %v", st.Uid, err)
	}
}

func TestCopyRefusesOtherOwnersTree(t *testing.T) {
	_, err := Copy(mustDir(t, t.TempDir()), mustDir(t, t.TempDir()), CopyOptions{
		SourceUID: os.Getuid() + 1, UID: os.Getuid(), GID: os.Getgid(), Budget: 1 << 20,
	})
	if err == nil {
		t.Fatal("copied a tree of another user")
	}
	if _, err := Copy(mustDir(t, t.TempDir()), mustDir(t, t.TempDir()), CopyOptions{UID: os.Getuid(), GID: os.Getgid(), Budget: 1 << 20}); err == nil {
		t.Fatal("copied without a source owner")
	}
}

func TestCopyNeedsBudget(t *testing.T) {
	if _, err := copyTree(t, t.TempDir(), t.TempDir(), 0, nil); err == nil {
		t.Fatal("copy without budget ran")
	}
}

// Lstat looks at the last part of a path without following it, and refuses
// a path that goes through a link or out of the folder.
func TestLstatNeverFollows(t *testing.T) {
	old := t.TempDir()
	write(t, filepath.Join(old, "home/x/NIXOS"), "")
	if err := os.Symlink("home/x", filepath.Join(old, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(old, "home/passwd")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(old, "home/fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := mustDir(t, old)

	for _, rel := range []string{"etc/NIXOS", "../" + filepath.Base(old) + "/home", "/etc", "home/../home/x", "home/", ""} {
		withTimeout(t, func() {
			if _, err := d.Lstat(rel); err == nil {
				t.Errorf("%q: accepted", rel)
			}
		})
	}
	for rel, mode := range map[string]uint32{
		"etc":          unix.S_IFLNK,
		"home/passwd":  unix.S_IFLNK,
		"home/fifo":    unix.S_IFIFO,
		"home/x":       unix.S_IFDIR,
		"home/x/NIXOS": unix.S_IFREG,
	} {
		withTimeout(t, func() {
			st, err := d.Lstat(rel)
			if err != nil {
				t.Errorf("%s: %v", rel, err)
			} else if st.Mode&unix.S_IFMT != mode {
				t.Errorf("%s: mode %o", rel, st.Mode)
			}
		})
	}
	if _, err := d.Lstat("home/missing"); !errors.Is(err, unix.ENOENT) {
		t.Errorf("missing: %v", err)
	}
}

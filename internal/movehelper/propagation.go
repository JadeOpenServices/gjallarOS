package movehelper

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// maxMountinfo caps what checkPrivate reads of the mount table.
const maxMountinfo = 8 << 20

// mountinfo is the mount table checkPrivate reads; tests may not change it.
const mountinfo = "/proc/self/mountinfo"

// privateBase makes base a mount of its own that shares nothing (L153).
// /run is a shared mount on systemd: a disk mounted in a plain folder below
// it shows up in every namespace that shares /run, the namespaces of
// running services included, and stays busy there after the helper
// unmounts it. Base is bound on itself and marked private before anything
// is mounted below it; the bind itself still reaches the other namespaces,
// but only as the empty, root-only folder. Called with base locked.
func privateBase(base string) error {
	mounted, err := mountRootAt(base)
	if err != nil {
		return err
	}
	if !mounted {
		if err := nothingBelow(base); err != nil {
			return err
		}
		fd, err := unix.Open(base, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", base, err)
		}
		tree, err := unix.OpenTree(fd, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
		if err == nil {
			err = unix.MoveMount(tree, "", fd, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH)
			unix.Close(tree)
		}
		unix.Close(fd)
		if err != nil {
			return fmt.Errorf("bind %s on itself: %w", base, err)
		}
	}
	// Opened again: the path now leads to the bind, not the folder under it.
	fd, err := unix.Open(base, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", base, err)
	}
	defer unix.Close(fd)
	attr := unix.MountAttr{Propagation: unix.MS_PRIVATE}
	if err := unix.MountSetattr(fd, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &attr); err != nil {
		return fmt.Errorf("make %s private: %w", base, err)
	}
	return checkPrivate(base)
}

// nothingBelow refuses to bind base on itself while something is mounted
// below it: the bind would hide that mount, which would stay shared.
func nothingBelow(base string) error {
	mounts, err := mountsAt(base)
	if err != nil {
		return err
	}
	if len(mounts) > 0 {
		return fmt.Errorf("%s is mounted below %s from an earlier run; restart the stick", mounts[0].point, base)
	}
	return nil
}

// checkPrivate refuses when the mount at base, or any mount below it, is
// shared with or receives mounts from another namespace (L153).
func checkPrivate(base string) error {
	mounts, err := mountsAt(base)
	if err != nil {
		return err
	}
	for _, m := range mounts {
		if m.shared {
			return fmt.Errorf("%s is shared with other namespaces", m.point)
		}
	}
	return nil
}

type mountEntry struct {
	point  string
	shared bool
}

// mountsAt lists the mounts at p or below it in this namespace.
func mountsAt(p string) ([]mountEntry, error) {
	f, err := os.Open(mountinfo)
	if err != nil {
		return nil, fmt.Errorf("read the mount table: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMountinfo+1))
	if err != nil {
		return nil, fmt.Errorf("read the mount table: %w", err)
	}
	if len(data) > maxMountinfo {
		return nil, errors.New("the mount table is too large")
	}
	return parseMountinfo(data, p)
}

func parseMountinfo(data []byte, p string) ([]mountEntry, error) {
	var out []mountEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), maxMountinfo)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if len(fields) < 6 || sep < 0 {
			return nil, errors.New("the mount table has a line that is not a mount")
		}
		point := unescapeMount(fields[4])
		if point != p && !strings.HasPrefix(point, p+"/") {
			continue
		}
		m := mountEntry{point: point}
		for _, tag := range fields[6:sep] {
			if strings.HasPrefix(tag, "shared:") || strings.HasPrefix(tag, "master:") {
				m.shared = true
			}
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// unescapeMount undoes the octal escapes the kernel writes for space, tab,
// newline and backslash in a mount point.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, ok := octal(s[i+1 : i+4]); ok {
				b.WriteByte(v)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func octal(s string) (byte, bool) {
	if len(s) != 3 {
		return 0, false
	}
	var v int
	for _, c := range s {
		if c < '0' || c > '7' {
			return 0, false
		}
		v = v*8 + int(c-'0')
	}
	if v > 0xff {
		return 0, false
	}
	return byte(v), true
}

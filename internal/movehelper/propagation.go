package movehelper

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// maxMountinfo caps what checkBase reads of the mount table.
const maxMountinfo = 8 << 20

// mountinfo is the mount table checkBase reads; tests may not change it.
const mountinfo = "/proc/self/mountinfo"

// restart is the advice when base is not in the state isolateBase leaves it.
const restart = "restart the stick"

// isolateBase makes base a mount with a peer group of its own (L153, L154).
//
// /run is a shared mount on systemd: a disk mounted in a plain folder below
// it shows up in every namespace that shares /run, the namespaces of
// running services included, and stays busy there after the helper
// unmounts it (L153). A base that is merely private has the opposite
// problem: a namespace made during the move (a service with PrivateTmp,
// ProtectSystem or PrivateMounts, a timer, systemd-run) copies the old and
// new disk mounts, and the helper's unmount never reaches that copy (L154).
//
// So on the first lock base is bound on itself, made private, which takes
// it out of /run's peer group, and then shared, which gives it a new group
// of its own. A namespace that existed before sees only the bind: the empty,
// root-only folder. A namespace made later holds a copy of base in the new
// group, and the helper's mounts and unmounts below base reach it.
//
// Propagation runs one way. systemd starts a namespaced unit as rslave and
// then shares it again, so its copies show master:X shared:Y and send
// nothing back; a copy made in an unprivileged user namespace the kernel
// turns into a slave. Only root can push a mount into base from outside.
//
// Later locks find base a mount already and only check it: making it
// private again would cut off the copies made since. A base left half done
// by a crash between the two steps is refused, never fixed. Called with
// base locked.
func isolateBase(base string) error {
	mounted, err := mountRootAt(base)
	if err != nil {
		return err
	}
	if mounted {
		return checkBase(base)
	}
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
	// Opened again: the path now leads to the bind, not the folder under it.
	fd, err = unix.Open(base, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", base, err)
	}
	defer unix.Close(fd)
	for _, step := range []struct {
		prop  uint64
		flags uint
		what  string
	}{
		{unix.MS_PRIVATE, unix.AT_RECURSIVE, "private"},
		{unix.MS_SHARED, 0, "shared"},
	} {
		attr := unix.MountAttr{Propagation: step.prop}
		if err := unix.MountSetattr(fd, "", unix.AT_EMPTY_PATH|step.flags, &attr); err != nil {
			return fmt.Errorf("make %s %s: %w", base, step.what, err)
		}
	}
	return checkBase(base)
}

// nothingBelow refuses to bind base on itself while something is mounted
// below it: the bind would hide that mount, which would stay shared.
func nothingBelow(base string) error {
	mounts, err := mountTable()
	if err != nil {
		return err
	}
	for _, m := range mounts {
		if below(m.point, base) {
			return fmt.Errorf("%s is mounted below %s from an earlier run; %s", m.point, base, restart)
		}
	}
	return nil
}

// checkBase refuses unless base is a mount and it and every mount below it
// are shared, receive from no master, and share their peer group with no
// mount outside base (L153, L154). Mounts below a shared base get new
// groups of their own from the kernel, so the only peers they may have are
// copies in namespaces made during the move. A peer outside base in this
// namespace means base never left /run's group, or a mount was copied in
// from elsewhere.
func checkBase(base string) error {
	mounts, err := mountTable()
	if err != nil {
		return err
	}
	groups := map[int]string{}
	isMount := false
	for _, m := range mounts {
		if !below(m.point, base) {
			continue
		}
		isMount = isMount || m.point == base
		switch {
		case m.group == 0:
			return fmt.Errorf("%s does not pass unmounts on to other namespaces; %s", m.point, restart)
		case m.master:
			return fmt.Errorf("%s receives mounts from another namespace; %s", m.point, restart)
		}
		groups[m.group] = m.point
	}
	if !isMount {
		return fmt.Errorf("%s is not a mount of its own; %s", base, restart)
	}
	for _, m := range mounts {
		if p, ok := groups[m.group]; ok && !below(m.point, base) {
			return fmt.Errorf("%s shares its mounts with %s; %s", p, m.point, restart)
		}
	}
	return nil
}

// below is true for p itself and anything under it: /run/gjallar-move2 is
// not below /run/gjallar-move.
func below(point, p string) bool {
	return point == p || strings.HasPrefix(point, p+"/")
}

type mountEntry struct {
	id     uint64 // the mount id, as statx STATX_MNT_ID reports it
	dev    string // major:minor of the file system
	root   string // the folder of the file system mounted here, /@home for a subvolume
	point  string
	group  int  // the shared:N peer group, 0 when not shared
	master bool // a master:N tag: receives from another group
	fstype string
	source string
}

// mountTable lists the mounts in this namespace.
func mountTable() ([]mountEntry, error) {
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
	return parseMountinfo(data)
}

func parseMountinfo(data []byte) ([]mountEntry, error) {
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
		if len(fields) < 6 || sep < 0 || sep+2 >= len(fields) {
			return nil, errors.New("the mount table has a line that is not a mount")
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("the mount table has a bad mount id %q", fields[0])
		}
		m := mountEntry{
			id:     id,
			dev:    fields[2],
			root:   unescapeMount(fields[3]),
			point:  unescapeMount(fields[4]),
			fstype: fields[sep+1],
			source: unescapeMount(fields[sep+2]),
		}
		for _, tag := range fields[6:sep] {
			name, num, _ := strings.Cut(tag, ":")
			switch name {
			case "shared":
				g, err := strconv.Atoi(num)
				if err != nil || g <= 0 {
					return nil, fmt.Errorf("the mount table has a bad tag %q", tag)
				}
				m.group = g
			case "master":
				m.master = true
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

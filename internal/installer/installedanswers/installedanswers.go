// Package installedanswers reads the installer answers of an installed
// gjallarOS from its unlocked root, for a reinstall from the recovery
// partition. The recovery image carries no answers of its own; they stay
// inside the encrypted root (L78).
//
// The recovery installer runs as root and the installed root is a disk it
// did not make. The read follows no link in any part of the path, opens
// nothing but a regular file, never blocks on a pipe and stops at MaxSize.
package installedanswers

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// Path is where an installed system keeps its answers, relative to its
// root: the root-owned system checkout (@config).
const Path = "etc/gjallar/system/user.config.json"

// MaxSize caps the read. A real file is a few KiB.
const MaxSize = 256 << 10

// owner is the uid the file must belong to. Tests change it.
var owner uint32 = 0

// Read returns the answers file below root. A missing file returns an
// error that matches fs.ErrNotExist.
func Read(root string) ([]byte, error) {
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open installed root %s: %w", root, err)
	}
	defer unix.Close(rootFD)

	// O_PATH opens no device and no pipe; the checks below run on this
	// handle before anything is read.
	pathFD, err := unix.Openat2(rootFD, Path, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, fmt.Errorf("%s below %s: %w", Path, root, fs.ErrNotExist)
	}
	if errors.Is(err, unix.ELOOP) {
		return nil, fmt.Errorf("refuse %s below %s: a part of the path is a link", Path, root)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s below %s: %w", Path, root, err)
	}
	defer unix.Close(pathFD)

	var st unix.Stat_t
	if err := unix.Fstat(pathFD, &st); err != nil {
		return nil, fmt.Errorf("stat %s below %s: %w", Path, root, err)
	}
	if err := check(st); err != nil {
		return nil, fmt.Errorf("refuse %s below %s: %w", Path, root, err)
	}

	// Reopen the checked inode for reading. The /proc link names the same
	// file the handle holds, so nothing swapped in after the checks counts.
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(pathFD), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s below %s for reading: %w", Path, root, err)
	}
	f := os.NewFile(uintptr(fd), Path)
	defer f.Close()

	var again unix.Stat_t
	if err := unix.Fstat(fd, &again); err != nil {
		return nil, fmt.Errorf("stat %s below %s: %w", Path, root, err)
	}
	if again.Dev != st.Dev || again.Ino != st.Ino {
		return nil, fmt.Errorf("refuse %s below %s: the file changed while it was opened", Path, root)
	}

	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s below %s: %w", Path, root, err)
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("refuse %s below %s: larger than %d bytes", Path, root, MaxSize)
	}
	return data, nil
}

func check(st unix.Stat_t) error {
	// O_PATH with O_NOFOLLOW opens a link at the last part as the link.
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return errors.New("the file is a link")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("not a regular file")
	}
	if st.Uid != owner {
		return fmt.Errorf("owned by uid %d, not %d", st.Uid, owner)
	}
	if st.Mode&0o022 != 0 {
		return fmt.Errorf("group or others may write it (mode %04o)", st.Mode&0o7777)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("has %d hard links", st.Nlink)
	}
	if st.Size > MaxSize {
		return fmt.Errorf("larger than %d bytes", MaxSize)
	}
	return nil
}

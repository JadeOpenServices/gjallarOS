// Package safefs reads and copies files as root in trees that someone else
// may have shaped: an old disk brought to the move (345), or a new root that
// gets a user's files.
//
// Every name is resolved below an open folder handle with openat2 and
// RESOLVE_BENEATH, RESOLVE_NO_SYMLINKS, RESOLVE_NO_MAGICLINKS and
// RESOLVE_NO_XDEV: a link in any part of a path, a ".." that climbs out, or
// a mount point on the way refuses the path. A file is opened as O_PATH
// first and checked before it is opened for reading, so a pipe or a device
// is never opened at all. Owners and modes are set on the open handle.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

var (
	// ErrNotPlain refuses something that is not a plain file or folder
	// where one is needed, or a path with a link in it.
	ErrNotPlain = errors.New("not a plain file or folder")
	// ErrTooBig refuses a file over the size cap.
	ErrTooBig = errors.New("file is over the size cap")
	// ErrChanged refuses a file that was swapped while it was opened.
	ErrChanged = errors.New("file changed while it was opened")
	// ErrTooMany refuses a folder with more entries, or more name bytes,
	// than the cap.
	ErrTooMany = errors.New("folder has more names than the cap")
	// ErrNotMount refuses a folder that SubMount needs to be a mount point.
	ErrNotMount = errors.New("not a mount point")
)

const resolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

// Dir is an open folder. Paths given to its methods are relative to it and
// never leave it.
type Dir struct {
	fd   int
	name string
}

// OpenDir opens the folder at path. path itself must be trusted: its
// parents are walked by the kernel as usual. Only the last part may not be
// a link.
func OpenDir(path string) (*Dir, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open folder %s: %w", path, refusal(err))
	}
	return &Dir{fd: fd, name: path}, nil
}

// Close closes the folder handle.
func (d *Dir) Close() error { return unix.Close(d.fd) }

// Name is the path the folder was opened with, for messages.
func (d *Dir) Name() string { return d.name }

// Stat reports on the open folder itself: the folder the handle holds, not
// whatever its path names now.
func (d *Dir) Stat() (unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstat(d.fd, &st); err != nil {
		return st, fmt.Errorf("check %s: %w", d.name, err)
	}
	return st, nil
}

// Sub opens the folder rel below d.
func (d *Dir) Sub(rel string) (*Dir, error) {
	fd, err := openat2(d.fd, rel, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("open folder %s below %s: %w", rel, d.name, refusal(err))
	}
	return &Dir{fd: fd, name: d.name + "/" + rel}, nil
}

// SubMount opens the folder rel below d whose last part is a mount point,
// such as home on a new root that has @home mounted there. The parts on the
// way are resolved as in Sub; only the last step may enter the file system
// mounted there, and it must: a plain folder never stands in for the mount.
// rel "." is refused: d itself is never the mount asked for.
func (d *Dir) SubMount(rel string) (*Dir, error) {
	if rel == "." {
		return nil, fmt.Errorf("open mount %s below %s: %w: not a name below the folder", rel, d.name, ErrNotPlain)
	}
	dirFD, last, done, err := d.parent(rel, "open")
	if err != nil {
		return nil, err
	}
	defer done()
	fd, err := openat2Resolve(dirFD, last, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolve&^unix.RESOLVE_NO_XDEV)
	if err != nil {
		return nil, fmt.Errorf("open mount %s below %s: %w", rel, d.name, refusal(err))
	}
	var stx unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_TYPE, &stx); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("check mount %s below %s: %w", rel, d.name, err)
	}
	if stx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT == 0 || stx.Attributes&unix.STATX_ATTR_MOUNT_ROOT == 0 {
		unix.Close(fd)
		return nil, fmt.Errorf("open mount %s below %s: %w", rel, d.name, ErrNotMount)
	}
	return &Dir{fd: fd, name: d.name + "/" + rel}, nil
}

// MountID is the id of the mount the open folder is on, the first field of
// its line in /proc/self/mountinfo. It is taken from the handle, so the
// answer is about the folder that was checked, not whatever its path names
// now.
func (d *Dir) MountID() (uint64, error) {
	var stx unix.Statx_t
	if err := unix.Statx(d.fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &stx); err != nil {
		return 0, fmt.Errorf("check the mount of %s: %w", d.name, err)
	}
	if stx.Mask&unix.STATX_MNT_ID == 0 {
		return 0, fmt.Errorf("check the mount of %s: the kernel does not tell", d.name)
	}
	return stx.Mnt_id, nil
}

// Names lists the folder rel below d ("." for d itself), sorted, without
// . and .. . A folder with more than max entries, or with names over
// MaxNameBytes, is refused with ErrTooMany, so a crafted folder cannot make
// root hold millions of names.
func (d *Dir) Names(rel string, max int) ([]string, error) {
	fd, err := openat2(d.fd, rel, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("list %s below %s: %w", rel, d.name, refusal(err))
	}
	defer unix.Close(fd)
	names, _, err := readNames(fd, max, MaxNameBytes)
	if err != nil {
		return nil, fmt.Errorf("list %s below %s: %w", rel, d.name, err)
	}
	return names, nil
}

// Lstat reports on rel below d without following a link at its end; links
// on the way there are refused as everywhere else. rel must be a clean path
// inside d.
func (d *Dir) Lstat(rel string) (unix.Stat_t, error) {
	var st unix.Stat_t
	dirFD, base, done, err := d.parent(rel, "check")
	if err != nil {
		return st, err
	}
	defer done()
	if err := unix.Fstatat(dirFD, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return st, fmt.Errorf("check %s below %s: %w", rel, d.name, err)
	}
	return st, nil
}

// ReadFile reads the plain file rel below d. A file over max bytes, a pipe,
// a device, a socket or a path with a link in it is refused without
// reading anything.
func (d *Dir) ReadFile(rel string, max int64) ([]byte, error) {
	f, st, err := d.openPlain(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st.Size > max {
		return nil, fmt.Errorf("read %s below %s: %w (%d bytes, cap %d)", rel, d.name, ErrTooBig, st.Size, max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("read %s below %s: %w", rel, d.name, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("read %s below %s: %w", rel, d.name, ErrTooBig)
	}
	return data, nil
}

// openPlain opens rel for reading once it is known to be a plain file.
func (d *Dir) openPlain(rel string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	pathFD, err := openat2(d.fd, rel, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return nil, st, fmt.Errorf("open %s below %s: %w", rel, d.name, refusal(err))
	}
	defer unix.Close(pathFD)
	f, st, err := reopen(pathFD)
	if err != nil {
		return nil, st, fmt.Errorf("open %s below %s: %w", rel, d.name, err)
	}
	return f, st, nil
}

// reopen opens the O_PATH handle pathFD for reading after checking it
// holds a plain file. The /proc link names the inode the handle holds, so a
// swap after the check does not count; the inode is compared again anyway.
func reopen(pathFD int) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstat(pathFD, &st); err != nil {
		return nil, st, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, st, fmt.Errorf("%w: %s", ErrNotPlain, kind(st.Mode))
	}
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(pathFD), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, st, err
	}
	var again unix.Stat_t
	if err := unix.Fstat(fd, &again); err != nil {
		unix.Close(fd)
		return nil, st, err
	}
	if again.Dev != st.Dev || again.Ino != st.Ino || again.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, st, ErrChanged
	}
	return os.NewFile(uintptr(fd), "safefs"), again, nil
}

func openat2(dirFD int, rel string, flags uint64) (int, error) {
	return openat2Mode(dirFD, rel, flags, 0)
}

func openat2Mode(dirFD int, rel string, flags, mode uint64) (int, error) {
	return openat2Resolve(dirFD, rel, flags, mode, resolve)
}

func openat2Resolve(dirFD int, rel string, flags, mode, how uint64) (int, error) {
	for {
		fd, err := unix.Openat2(dirFD, rel, &unix.OpenHow{Flags: flags, Mode: mode, Resolve: how})
		if err == unix.EINTR || err == unix.EAGAIN {
			// openat2 returns EAGAIN when a rename raced the lookup.
			continue
		}
		return fd, err
	}
}

// refusal names the errors openat2 gives for a refused path.
func refusal(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return fmt.Errorf("%w: a part of the path is a link", ErrNotPlain)
	case errors.Is(err, unix.EXDEV):
		return fmt.Errorf("%w: the path leaves the folder or crosses a mount", ErrNotPlain)
	case errors.Is(err, unix.ENOTDIR):
		return fmt.Errorf("%w: a part of the path is not a folder", ErrNotPlain)
	}
	return err
}

func kind(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return "file"
	case unix.S_IFDIR:
		return "folder"
	case unix.S_IFLNK:
		return "link"
	case unix.S_IFIFO:
		return "pipe"
	case unix.S_IFSOCK:
		return "socket"
	case unix.S_IFCHR, unix.S_IFBLK:
		return "device"
	}
	return "unknown"
}

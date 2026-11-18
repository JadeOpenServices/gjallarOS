package rootprovision

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// rootUID is the owner CheckMountPoint wants; tests swap in their own user.
var rootUID uint32

// checkTarget runs before anything is formatted; tests swap it so they do
// not depend on the /mnt of the machine they run on.
var checkTarget = func() error { return CheckMountPoint(mountPoint) }

// CheckMountPoint refuses a mount point that someone other than root could
// change or swap between this check and root's mounts (Q255, L58): on the
// USB stick the live login runs next to the installer. The folder above it
// must be root's and writable only by root; the mount point itself must be
// missing (root creates it) or root's plain, empty folder, writable only by
// root. A link, FIFO or file there is refused, never followed.
func CheckMountPoint(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("mount point %q is not a clean absolute path", path)
	}
	if err := rootOnlyDir(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := rootOnlyDir(path); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open mount point %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if err := rootOnlyFd(fd, path); err != nil {
		return err
	}
	if names, err := f.Readdirnames(1); len(names) > 0 {
		return fmt.Errorf("mount point %s is not empty", path)
	} else if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read mount point %s: %w", path, err)
	}
	return nil
}

func rootOnlyDir(path string) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	return rootOnly(&st, path)
}

func rootOnlyFd(fd int, path string) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	return rootOnly(&st, path)
}

func rootOnly(st *unix.Stat_t, path string) error {
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%s is not a plain folder", path)
	case st.Uid != rootUID:
		return fmt.Errorf("%s is not owned by root", path)
	case st.Mode&0o022 != 0:
		return fmt.Errorf("%s can be changed by users other than root", path)
	}
	return nil
}

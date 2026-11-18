package movehelper

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// TargetRoot is where the installer mounts the new root before
// nixos-install (rootprovision). Root checked it before mounting (Q255).
const TargetRoot = "/mnt"

// attachOp shows the new root, with every mount below it, at New. The
// installer has mounted it at TargetRoot; the helper works only below its
// own folder, so it takes a copy of that mount tree from an fd, never
// through a path the live login could change. The copy is nosuid, nodev
// and private: no mount or unmount in it reaches /mnt or back. The kernel
// makes a tree moved below a shared mount shared again, so it is moved only
// below base, which lock made a private mount, and checked after the move
// (L153).
func attachOp(base string) error { return attach(base, TargetRoot) }

func attach(base, target string) error {
	newDir := filepath.Join(base, New)
	if mounted, err := mountRootAt(newDir); err != nil {
		return err
	} else if mounted {
		return errors.New("the new disk is already attached")
	}
	src, err := unix.Open(target, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}
	defer unix.Close(src)
	if err := targetMount(src, target); err != nil {
		return err
	}
	tree, err := unix.OpenTree(src, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH|unix.AT_RECURSIVE)
	if err != nil {
		return fmt.Errorf("copy the mounts at %s: %w", target, err)
	}
	defer unix.Close(tree)
	attr := unix.MountAttr{
		Attr_set:    unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV,
		Propagation: unix.MS_PRIVATE,
	}
	if err := unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &attr); err != nil {
		return fmt.Errorf("close the mounts copied from %s: %w", target, err)
	}
	dst, err := unix.Open(newDir, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", newDir, err)
	}
	defer unix.Close(dst)
	if err := unix.MoveMount(tree, "", dst, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach the new disk at %s: %w", newDir, err)
	}
	if err := checkBase(base); err != nil {
		return errors.Join(err, detachOp(base))
	}
	return nil
}

// targetMount refuses a target that is not root's own mount, closed to
// everyone else: root mounted the new root there itself.
func targetMount(fd int, target string) error {
	var stx unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_TYPE|unix.STATX_MODE|unix.STATX_UID, &stx); err != nil {
		return fmt.Errorf("inspect %s: %w", target, err)
	}
	switch {
	case stx.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%s is not a folder", target)
	case stx.Uid != rootUID:
		return fmt.Errorf("%s is not owned by root", target)
	case stx.Mode&0o022 != 0:
		return fmt.Errorf("%s can be changed by users other than root", target)
	case stx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT == 0:
		return errors.New("this kernel does not say whether a folder is a mount")
	case stx.Attributes&unix.STATX_ATTR_MOUNT_ROOT == 0:
		return fmt.Errorf("the new disk is not mounted at %s", target)
	}
	return nil
}

// mountRootAt says whether p, which must not be a link, is the root of a
// mount.
func mountRootAt(p string) (bool, error) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, p, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE, &stx); err != nil {
		return false, fmt.Errorf("inspect %s: %w", p, err)
	}
	if stx.Mode&unix.S_IFMT != unix.S_IFDIR {
		return false, fmt.Errorf("%s is not a folder", p)
	}
	if stx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT == 0 {
		return false, errors.New("this kernel does not say whether a folder is a mount")
	}
	return stx.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0, nil
}

// detachOp takes the new root away from New again, with every mount below
// it. Nothing attached is fine: the step runs again after a failure.
func detachOp(base string) error {
	newDir := filepath.Join(base, New)
	mounted, err := mountRootAt(newDir)
	if err != nil || !mounted {
		return err
	}
	if err := unix.Unmount(newDir, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW); err != nil {
		return fmt.Errorf("detach the new disk from %s: %w", newDir, err)
	}
	return nil
}

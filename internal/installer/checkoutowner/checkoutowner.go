// Package checkoutowner hands files that a root process wrote into a user's
// checkout back to the checkout's owner. A sudo installer run or a root
// rebuild otherwise leaves root-owned 0600 files (generated/hardware.nix and
// its backups, state.nix, user.config.json) that the next run as the user
// cannot read. Modes are never changed: only the owner moves.
package checkoutowner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Managed lists the repository-relative paths gjallarOS tools write into a
// checkout. Directories are handled recursively.
var Managed = []string{
	"generated",
	"user.config.json",
	".git/info/exclude",
}

var (
	geteuid = os.Geteuid
	lchown  = os.Lchown
)

// Owner returns the uid and gid of the checkout root. The root must be a real
// directory, not a symlink, so a planted link cannot redirect ownership.
func Owner(repo string) (int, int, error) {
	info, err := os.Lstat(repo)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect checkout: %w", err)
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("checkout %s is not a directory", repo)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("checkout %s has no owner information", repo)
	}
	return int(stat.Uid), int(stat.Gid), nil
}

// Foreign lists the managed paths (plus extra repository-relative paths) whose
// owner differs from the checkout's owner.
func Foreign(repo string, extra ...string) ([]string, error) {
	uid, gid, err := Owner(repo)
	if err != nil {
		return nil, err
	}
	var foreign []string
	err = walk(repo, extra, func(path string, stat *syscall.Stat_t) error {
		if stat == nil || int(stat.Uid) != uid || int(stat.Gid) != gid {
			foreign = append(foreign, path)
		}
		return nil
	})
	return foreign, err
}

// Repair gives the managed paths (plus extra repository-relative paths) the
// checkout's owner. It does nothing unless it runs as root, and nothing for
// a root-owned checkout. Symlinks are re-owned, never followed.
func Repair(repo string, extra ...string) error {
	if geteuid() != 0 {
		return nil
	}
	uid, gid, err := Owner(repo)
	if err != nil {
		return err
	}
	if uid == 0 {
		return nil
	}
	return walk(repo, extra, func(path string, stat *syscall.Stat_t) error {
		if stat != nil && int(stat.Uid) == uid && int(stat.Gid) == gid {
			return nil
		}
		if err := lchown(path, uid, gid); err != nil {
			return fmt.Errorf("hand %s back to the checkout owner: %w", path, err)
		}
		return nil
	})
}

// Ensure repairs foreign-owned managed paths before a run as the checkout
// owner reads them. As root it re-owns them directly; otherwise it asks
// privileged (sudo) to chown them, without following symlinks.
func Ensure(
	ctx context.Context,
	repo string,
	privileged func(context.Context, ...string) error,
) error {
	foreign, err := Foreign(repo)
	if err != nil || len(foreign) == 0 {
		return err
	}
	if geteuid() == 0 {
		return Repair(repo)
	}
	uid, gid, err := Owner(repo)
	if err != nil {
		return err
	}
	args := []string{"chown", "-R", "-P", "-h", fmt.Sprintf("%d:%d", uid, gid), "--"}
	for _, rel := range Managed {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); err == nil {
			args = append(args, path)
		}
	}
	if err := privileged(ctx, args...); err != nil {
		return fmt.Errorf("hand %d root-written checkout files back to their owner: %w", len(foreign), err)
	}
	return nil
}

func walk(repo string, extra []string, visit func(string, *syscall.Stat_t) error) error {
	root := filepath.Clean(repo)
	for _, rel := range append(append([]string{}, Managed...), extra...) {
		rel = filepath.Clean(filepath.FromSlash(rel))
		if filepath.IsAbs(rel) || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("path %q is outside the checkout", rel)
		}
		start := filepath.Join(root, rel)
		if err := insideRealParents(root, start); err != nil {
			return err
		}
		err := filepath.WalkDir(start, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && path == start {
					return nil
				}
				// An unreadable directory was written by someone else;
				// report it without its contents (stat is nil).
				if errors.Is(err, fs.ErrPermission) && entry != nil && entry.IsDir() {
					if err := visit(path, nil); err != nil {
						return err
					}
					return fs.SkipDir
				}
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return fmt.Errorf("%s has no owner information", path)
			}
			return visit(path, stat)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// insideRealParents refuses a path whose parent directories inside the
// checkout include a symlink, which could point the walk elsewhere.
func insideRealParents(root, path string) error {
	for dir := filepath.Dir(path); dir != root && strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink inside the checkout", dir)
		}
	}
	return nil
}

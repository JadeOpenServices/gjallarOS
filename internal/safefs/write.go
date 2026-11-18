package safefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// MaxWrite caps what WriteFile writes in one go.
const MaxWrite = 4 << 20

// ErrNotPrivate refuses a folder that someone other than its owner may
// change, or whose owner is not the one asked for.
var ErrNotPrivate = errors.New("folder is not private to its owner")

// private checks the folder behind fd: it belongs to root or to uid, and
// neither group nor others may write it. Nobody else can then rename or
// plant names in it while root works there.
func private(fd, uid int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%w: not a folder", ErrNotPlain)
	case int(st.Uid) != unix.Geteuid() && int(st.Uid) != uid:
		return fmt.Errorf("%w: it belongs to user %d", ErrNotPrivate, st.Uid)
	case st.Mode&0o022 != 0:
		return fmt.Errorf("%w: group or others may write it", ErrNotPrivate)
	}
	return nil
}

// MakeDirs makes the folders of rel below d that are missing, each with
// owner uid:gid and exactly mode, the way mkdir -p would. Every part,
// made or found, must be a real folder (no link, no mount crossed) that
// belongs to this process's user or to uid and that nobody else may write.
func (d *Dir) MakeDirs(rel string, uid, gid int, mode uint32) error {
	if !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
		return fmt.Errorf("make %s below %s: %w: not a clean path inside the folder", rel, d.name, ErrNotPlain)
	}
	if mode&0o022 != 0 {
		return fmt.Errorf("make %s below %s: mode %o lets others write", rel, d.name, mode)
	}
	cur, err := unix.Dup(d.fd)
	if err != nil {
		return err
	}
	defer func() { unix.Close(cur) }()
	if err := private(cur, uid); err != nil {
		return fmt.Errorf("make %s below %s: %w", rel, d.name, err)
	}
	for part := range strings.SplitSeq(rel, "/") {
		next, err := subFolder(cur, part, uid, gid, mode)
		if err != nil {
			return fmt.Errorf("make %s below %s: %w", rel, d.name, err)
		}
		unix.Close(cur)
		cur = next
	}
	return nil
}

// subFolder opens the folder name in dirFD, made with uid:gid and mode
// when it is missing, and checks it with private.
func subFolder(dirFD int, name string, uid, gid int, mode uint32) (int, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := openat2(dirFD, name, flags)
	if errors.Is(err, unix.ENOENT) {
		// The folder holding name is private, so nobody else makes or
		// swaps name between these calls.
		if err := unix.Mkdirat(dirFD, name, 0o700); err != nil {
			return 0, made(err)
		}
		if fd, err = openat2(dirFD, name, flags); err != nil {
			return 0, refusal(err)
		}
		err = unix.Fchown(fd, uid, gid)
		if err == nil {
			err = unix.Fchmod(fd, mode)
		}
		if err != nil {
			unix.Close(fd)
			return 0, err
		}
	} else if err != nil {
		return 0, refusal(err)
	}
	if err := private(fd, uid); err != nil {
		unix.Close(fd)
		return 0, err
	}
	return fd, nil
}

// WriteFile writes data as the plain file rel below d with owner uid:gid
// and exactly mode, in place of a plain file of that name. The data goes
// to a new file next to it first, which is renamed over rel when complete:
// a reader sees the old file or the new one, never a half. The folder
// holding rel must be private (see MakeDirs) to this process's user or to
// uid; a link, folder, pipe or device at rel is refused and stays.
func (d *Dir) WriteFile(rel string, data []byte, uid, gid int, mode uint32) error {
	if len(data) > MaxWrite {
		return fmt.Errorf("write %s below %s: %w", rel, d.name, ErrTooBig)
	}
	dirFD, base, done, err := d.parent(rel, "write")
	if err != nil {
		return err
	}
	defer done()
	if err := private(dirFD, uid); err != nil {
		return fmt.Errorf("write %s below %s: %w", rel, d.name, err)
	}
	var st unix.Stat_t
	switch err := unix.Fstatat(dirFD, base, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case errors.Is(err, unix.ENOENT):
	case err != nil:
		return fmt.Errorf("write %s below %s: %w", rel, d.name, err)
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("write %s below %s: %w: %s", rel, d.name, ErrNotPlain, kind(st.Mode))
	}
	var r [8]byte
	if _, err := rand.Read(r[:]); err != nil {
		return err
	}
	tmp := "." + base + ".new-" + hex.EncodeToString(r[:])
	if len(tmp) > 255 {
		tmp = ".new-" + hex.EncodeToString(r[:])
	}
	fd, err := openat2Mode(dirFD, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s below %s: %w", rel, d.name, made(err))
	}
	ok := false
	defer func() {
		if !ok {
			unix.Unlinkat(dirFD, tmp, 0)
		}
	}()
	err = writeAll(fd, data, 0)
	if err == nil {
		// chown first: it clears set-id bits, and the mode is set after.
		err = unix.Fchown(fd, uid, gid)
	}
	if err == nil {
		err = unix.Fchmod(fd, mode)
	}
	if err == nil {
		err = unix.Fsync(fd)
	}
	if cerr := unix.Close(fd); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(dirFD, tmp, dirFD, base)
	}
	if err != nil {
		return fmt.Errorf("write %s below %s: %w", rel, d.name, err)
	}
	ok = true
	return nil
}

// Remove removes the plain file rel below d; a missing file is no error.
// A link at rel is removed itself, never followed; a folder is refused.
func (d *Dir) Remove(rel string) error {
	dirFD, base, done, err := d.parent(rel, "remove")
	if err != nil {
		return err
	}
	defer done()
	if err := unix.Unlinkat(dirFD, base, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove %s below %s: %w", rel, d.name, err)
	}
	return nil
}

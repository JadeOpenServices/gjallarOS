package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// maxLink caps what Readlink reads: a store path is far shorter.
const maxLink = 4096

// Readlink reads the link rel below d without following it. Links on the
// way there are refused as everywhere else; a target over max bytes is
// refused with ErrTooBig.
func (d *Dir) Readlink(rel string, max int) (string, error) {
	dirFD, base, done, err := d.parent(rel, "read link")
	if err != nil {
		return "", err
	}
	defer done()
	buf := make([]byte, min(max, maxLink)+1)
	n, err := unix.Readlinkat(dirFD, base, buf)
	if err != nil {
		return "", fmt.Errorf("read link %s below %s: %w", rel, d.name, err)
	}
	if n > max {
		return "", fmt.Errorf("read link %s below %s: %w", rel, d.name, ErrTooBig)
	}
	return string(buf[:n]), nil
}

// parent opens the folder that holds rel and returns it with rel's last
// part. done closes what parent opened.
func (d *Dir) parent(rel, what string) (int, string, func(), error) {
	if !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
		return 0, "", nil, fmt.Errorf("%s %s below %s: %w: not a clean path inside the folder", what, rel, d.name, ErrNotPlain)
	}
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return d.fd, rel, func() {}, nil
	}
	fd, err := openat2(d.fd, rel[:i], unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return 0, "", nil, fmt.Errorf("%s %s below %s: %w", what, rel, d.name, refusal(err))
	}
	return fd, rel[i+1:], func() { unix.Close(fd) }, nil
}

// Mkdir makes the folder name in d with exactly mode, whatever the umask,
// and opens it. name is one part, not a path. A name that exists already is
// refused with ErrExists.
func (d *Dir) Mkdir(name string, mode uint32) (*Dir, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name {
		return nil, fmt.Errorf("make %s below %s: %w: not one name", name, d.name, ErrNotPlain)
	}
	if err := unix.Mkdirat(d.fd, name, 0o700); err != nil {
		return nil, fmt.Errorf("make %s below %s: %w", name, d.name, made(err))
	}
	sub, err := d.Sub(name)
	if err != nil {
		return nil, err
	}
	if err := unix.Fchmod(sub.fd, mode); err != nil {
		sub.Close()
		return nil, fmt.Errorf("make %s below %s: %w", name, d.name, err)
	}
	return sub, nil
}

// Own sets owner and mode of the plain file or folder rel below d through
// its own handle, so a name swapped on the way is never what gets changed.
// Anything else (a link, a pipe, a device, a socket) is refused. It returns
// what the handle holds afterwards.
func (d *Dir) Own(rel string, uid, gid int, mode uint32) (unix.Stat_t, error) {
	var st unix.Stat_t
	pathFD, err := openat2(d.fd, rel, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return st, fmt.Errorf("own %s below %s: %w", rel, d.name, refusal(err))
	}
	defer unix.Close(pathFD)
	if err := unix.Fstat(pathFD, &st); err != nil {
		return st, err
	}
	var f *os.File
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		if f, _, err = reopen(pathFD); err != nil {
			return st, fmt.Errorf("own %s below %s: %w", rel, d.name, err)
		}
	case unix.S_IFDIR:
		fd, err := openat2(d.fd, rel, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
		if err != nil {
			return st, fmt.Errorf("own %s below %s: %w", rel, d.name, refusal(err))
		}
		f = os.NewFile(uintptr(fd), "safefs")
	default:
		return st, fmt.Errorf("own %s below %s: %w: %s", rel, d.name, ErrNotPlain, kind(st.Mode))
	}
	defer f.Close()
	fd := int(f.Fd())
	var again unix.Stat_t
	if err := unix.Fstat(fd, &again); err != nil {
		return st, err
	}
	if again.Dev != st.Dev || again.Ino != st.Ino {
		return st, fmt.Errorf("own %s below %s: %w", rel, d.name, ErrChanged)
	}
	// chown first: it clears set-id bits, and the mode is set after.
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return st, fmt.Errorf("own %s below %s: %w", rel, d.name, err)
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return st, fmt.Errorf("own %s below %s: %w", rel, d.name, err)
	}
	if err := unix.Fstat(fd, &again); err != nil {
		return st, err
	}
	return again, nil
}

// RemoveTree removes rel below d and everything in it. Links are removed,
// never followed; a folder deeper than MaxDepth or with more than max
// entries is refused, and what was removed before stays removed.
func (d *Dir) RemoveTree(rel string, max int) error {
	dirFD, base, done, err := d.parent(rel, "remove")
	if err != nil {
		return err
	}
	defer done()
	if err := removeEntry(dirFD, base, max, 0); err != nil {
		return fmt.Errorf("remove %s below %s: %w", rel, d.name, err)
	}
	return nil
}

func removeEntry(dirFD int, name string, max, depth int) error {
	err := unix.Unlinkat(dirFD, name, 0)
	if !errors.Is(err, unix.EISDIR) {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if depth >= MaxDepth {
		return ErrTooDeep
	}
	fd, err := openat2(dirFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return refusal(err)
	}
	names, _, err := readNames(fd, max, MaxNameBytes)
	if err == nil {
		for _, n := range names {
			if err = removeEntry(fd, n, max, depth+1); err != nil {
				break
			}
		}
	}
	unix.Close(fd)
	if err != nil {
		return err
	}
	return unix.Unlinkat(dirFD, name, unix.AT_REMOVEDIR)
}

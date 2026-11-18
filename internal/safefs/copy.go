package safefs

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ErrNoRoom stops a copy that would use more than its budget.
var ErrNoRoom = errors.New("the copy is over its space budget")

// ErrExists refuses a name that is already in the target folder. A copy
// never writes into something it did not make itself.
var ErrExists = errors.New("the target already has this name")

// ErrTooDeep refuses folders nested deeper than MaxDepth.
var ErrTooDeep = errors.New("folders are nested too deep")

// MaxDepth is how deep a copied tree may nest.
const MaxDepth = 256

// EntryCost is what every copied file, folder and link counts against the
// budget besides its data: a tree of many empty files fills the inode and
// metadata space too.
const EntryCost = 4096

// xattrCap caps the list and each value of extended attributes.
const xattrCap = 64 << 10

// CopyOptions says who owns the copy and how much room it may take.
type CopyOptions struct {
	// SourceUID owns the files to copy. A copy moves one user's files:
	// anything in the tree owned by someone else stays behind, so a file
	// root left in a home folder never becomes the user's. It must be above
	// zero.
	SourceUID int
	// UID and GID own every copied file, folder and link.
	UID, GID int
	// Budget is the most the copy may use: data bytes written plus
	// EntryCost per entry. It must be above zero.
	Budget int64
	// KeepXattr says which extended attributes ride along. nil keeps none.
	// ACLs (system.posix_acl_*), security.* and trusted.* are never kept,
	// whatever it says.
	KeepXattr func(name string) bool
}

// Stats counts what a copy did.
type Stats struct {
	Files, Folders, Links int
	// Bytes is the data written: holes in sparse files are not written
	// and not counted.
	Bytes int64
	// Used is what counted against the budget.
	Used int64
	// HardLinks counts files with more than one name; each name is
	// copied as its own file.
	HardLinks int
	// Skipped counts pipes, sockets and devices: never copied.
	Skipped int
	// Foreign counts entries owned by someone other than SourceUID: never
	// copied, and nothing below a foreign folder either.
	Foreign int
	// SetIDStripped counts setuid, setgid and sticky bits removed.
	SetIDStripped int
	// XattrsDropped counts extended attributes left behind.
	XattrsDropped int
}

// Copy copies everything below src into dst. dst must exist; every name the
// copy makes in it must not. Links are copied as links and never followed.
// Pipes, sockets and devices are skipped and counted. setuid, setgid and
// sticky bits are dropped; file times are kept.
func Copy(src, dst *Dir, opt CopyOptions) (Stats, error) {
	var stats Stats
	if opt.Budget <= 0 {
		return stats, fmt.Errorf("copy %s: no space budget", src.name)
	}
	if opt.SourceUID <= 0 {
		return stats, fmt.Errorf("copy %s: no source owner", src.name)
	}
	var top unix.Stat_t
	if err := unix.Fstat(src.fd, &top); err != nil {
		return stats, fmt.Errorf("copy %s: %w", src.name, err)
	}
	if int(top.Uid) != opt.SourceUID {
		return stats, fmt.Errorf("copy %s: it belongs to user %d, not %d", src.name, top.Uid, opt.SourceUID)
	}
	c := &copier{opt: opt, stats: &stats, buf: make([]byte, 1<<20)}
	err := c.tree(src.fd, dst.fd, src.name, 0)
	return stats, err
}

type copier struct {
	opt   CopyOptions
	stats *Stats
	buf   []byte
}

func (c *copier) charge(n int64) error {
	if c.stats.Used+n > c.opt.Budget {
		return ErrNoRoom
	}
	c.stats.Used += n
	return nil
}

func (c *copier) tree(srcFD, dstFD int, name string, depth int) error {
	if depth >= MaxDepth {
		return fmt.Errorf("copy %s: %w", name, ErrTooDeep)
	}
	names, err := readNames(srcFD, -1)
	if err != nil {
		return fmt.Errorf("list %s: %w", name, err)
	}
	for _, n := range names {
		if err := c.entry(srcFD, dstFD, n, name+"/"+n, depth); err != nil {
			return err
		}
	}
	return nil
}

func (c *copier) entry(srcDir, dstDir int, n, path string, depth int) error {
	var st unix.Stat_t
	if err := unix.Fstatat(srcDir, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if int(st.Uid) != c.opt.SourceUID {
		c.stats.Foreign++
		return nil
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return c.folder(srcDir, dstDir, n, path, depth)
	case unix.S_IFREG:
		return c.file(srcDir, dstDir, n, path, st)
	case unix.S_IFLNK:
		return c.link(srcDir, dstDir, n, path, st)
	default:
		c.stats.Skipped++
		return nil
	}
}

func (c *copier) folder(srcDir, dstDir int, n, path string, depth int) error {
	if err := c.charge(EntryCost); err != nil {
		return fmt.Errorf("copy %s: %w", path, err)
	}
	s, err := openat2(srcDir, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, refusal(err))
	}
	defer unix.Close(s)
	var st unix.Stat_t
	if err := unix.Fstat(s, &st); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if int(st.Uid) != c.opt.SourceUID {
		return fmt.Errorf("open %s: %w", path, ErrChanged)
	}
	if err := unix.Mkdirat(dstDir, n, 0o700); err != nil {
		return fmt.Errorf("make folder for %s: %w", path, made(err))
	}
	d, err := openat2(dstDir, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return fmt.Errorf("open new folder for %s: %w", path, refusal(err))
	}
	defer unix.Close(d)
	if err := unix.Fchown(d, c.opt.UID, c.opt.GID); err != nil {
		return fmt.Errorf("own folder for %s: %w", path, err)
	}
	c.stats.Folders++
	if err := c.tree(s, d, path, depth+1); err != nil {
		return err
	}
	return c.finish(s, d, path, st)
}

func (c *copier) file(srcDir, dstDir int, n, path string, st unix.Stat_t) error {
	if err := c.charge(EntryCost); err != nil {
		return fmt.Errorf("copy %s: %w", path, err)
	}
	pathFD, err := openat2(srcDir, n, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, refusal(err))
	}
	f, st2, err := reopen(pathFD)
	unix.Close(pathFD)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if st2.Ino != st.Ino || st2.Dev != st.Dev || int(st2.Uid) != c.opt.SourceUID {
		return fmt.Errorf("open %s: %w", path, ErrChanged)
	}
	if st2.Nlink > 1 {
		c.stats.HardLinks++
	}
	d, err := openat2Mode(dstDir, n, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("make file for %s: %w", path, made(err))
	}
	defer unix.Close(d)
	if err := unix.Fchown(d, c.opt.UID, c.opt.GID); err != nil {
		return fmt.Errorf("own file for %s: %w", path, err)
	}
	if err := c.data(int(f.Fd()), d, st2.Size, path); err != nil {
		return err
	}
	c.stats.Files++
	return c.finish(int(f.Fd()), d, path, st2)
}

// data copies the data parts of a file and leaves its holes as holes.
func (c *copier) data(s, d int, size int64, path string) error {
	var off int64
	for off < size {
		start, err := unix.Seek(s, off, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			break
		}
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			start = off
		} else if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		end, err := unix.Seek(s, start, unix.SEEK_HOLE)
		if err != nil || end > size {
			end = size
		}
		for start < end {
			n := int64(len(c.buf))
			if end-start < n {
				n = end - start
			}
			if err := c.charge(n); err != nil {
				return fmt.Errorf("copy %s: %w", path, err)
			}
			r, err := unix.Pread(s, c.buf[:n], start)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			if r == 0 {
				// The file is shorter than it said.
				end = start
				break
			}
			if err := writeAll(d, c.buf[:r], start); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			c.stats.Bytes += int64(r)
			start += int64(r)
		}
		off = end
	}
	if err := unix.Ftruncate(d, size); err != nil {
		return fmt.Errorf("size %s: %w", path, err)
	}
	return nil
}

func writeAll(fd int, b []byte, off int64) error {
	for len(b) > 0 {
		n, err := unix.Pwrite(fd, b, off)
		if err != nil {
			return err
		}
		b = b[n:]
		off += int64(n)
	}
	return nil
}

func (c *copier) link(srcDir, dstDir int, n, path string, st unix.Stat_t) error {
	if err := c.charge(EntryCost); err != nil {
		return fmt.Errorf("copy %s: %w", path, err)
	}
	buf := make([]byte, unix.PathMax+1)
	r, err := unix.Readlinkat(srcDir, n, buf)
	if err != nil {
		return fmt.Errorf("read link %s: %w", path, err)
	}
	if r > unix.PathMax {
		return fmt.Errorf("read link %s: %w", path, ErrTooBig)
	}
	if err := unix.Symlinkat(string(buf[:r]), dstDir, n); err != nil {
		return fmt.Errorf("make link for %s: %w", path, made(err))
	}
	if err := unix.Fchownat(dstDir, n, c.opt.UID, c.opt.GID, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("own link for %s: %w", path, err)
	}
	ts := []unix.Timespec{st.Atim, st.Mtim}
	if err := unix.UtimesNanoAt(dstDir, n, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("time link for %s: %w", path, err)
	}
	c.stats.Links++
	return nil
}

// finish sets mode, allowed extended attributes and times on the new file
// or folder d, last, so writing it does not change them again.
func (c *copier) finish(s, d int, path string, st unix.Stat_t) error {
	if st.Mode&0o7000 != 0 {
		c.stats.SetIDStripped++
	}
	if err := c.xattrs(s, d, path); err != nil {
		return err
	}
	if err := unix.Fchmod(d, st.Mode&0o777); err != nil {
		return fmt.Errorf("mode %s: %w", path, err)
	}
	if err := futimens(d, st.Atim, st.Mtim); err != nil {
		return fmt.Errorf("time %s: %w", path, err)
	}
	return nil
}

func (c *copier) xattrs(s, d int, path string) error {
	names, err := listXattr(s)
	if err != nil {
		return fmt.Errorf("list attributes of %s: %w", path, err)
	}
	for _, name := range names {
		if !keepable(name) || c.opt.KeepXattr == nil || !c.opt.KeepXattr(name) {
			c.stats.XattrsDropped++
			continue
		}
		val := make([]byte, xattrCap)
		n, err := unix.Fgetxattr(s, name, val)
		if err != nil {
			return fmt.Errorf("read attribute of %s: %w", path, err)
		}
		if err := unix.Fsetxattr(d, name, val[:n], 0); err != nil {
			return fmt.Errorf("write attribute of %s: %w", path, err)
		}
	}
	return nil
}

// keepable is false for attributes that carry rights or labels: they are
// never copied, whatever the caller allows.
func keepable(name string) bool {
	return strings.HasPrefix(name, "user.")
}

func listXattr(fd int) ([]string, error) {
	buf := make([]byte, xattrCap)
	n, err := unix.Flistxattr(fd, buf)
	if errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if errors.Is(err, unix.ERANGE) {
		return nil, ErrTooBig
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range strings.Split(string(buf[:n]), "\x00") {
		if p != "" {
			names = append(names, p)
		}
	}
	return names, nil
}

// futimens sets the times of the open file fd: utimensat with no path
// works on fd itself.
func futimens(fd int, atime, mtime unix.Timespec) error {
	ts := [2]unix.Timespec{atime, mtime}
	_, _, errno := unix.Syscall6(unix.SYS_UTIMENSAT, uintptr(fd), 0, uintptr(unsafe.Pointer(&ts[0])), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func made(err error) error {
	if errors.Is(err, unix.EEXIST) {
		return ErrExists
	}
	return refusal(err)
}

// readNames lists a folder, sorted, without . and .. . With max >= 0 a
// folder with more than max entries is refused with ErrTooMany.
func readNames(fd, max int) ([]string, error) {
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		return nil, err
	}
	var names []string
	buf := make([]byte, 64<<10)
	for {
		n, err := unix.Getdents(fd, buf)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			break
		}
		_, _, names = unix.ParseDirent(buf[:n], -1, names)
		if max >= 0 && len(names) > max {
			return nil, ErrTooMany
		}
	}
	sort.Strings(names)
	return names, nil
}

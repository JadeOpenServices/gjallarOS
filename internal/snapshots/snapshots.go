// Package snapshots holds what gjallar-snapshotd and gjallarctl share for
// restoring a user's own files from the hourly /home snapshots (ROADMAP
// section 15, step 3).
//
// Nothing the caller sends is used as a path into the snapshots as it is:
// snapshot names must match the names btrbk makes, the file must lie inside
// the caller's home, and it is opened beneath that home without following
// any symlink. Only a regular file the caller owns is handed out, as a
// read-only descriptor; the caller writes the copy itself.
package snapshots

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	BusName    = "org.gjallaros.Snapshots1"
	ObjectPath = "/org/gjallaros/Snapshots1"
	Interface  = "org.gjallaros.Snapshots1"

	// RestoreAction asks the caller's own password, every time.
	RestoreAction = "org.gjallaros.snapshots.restore"

	// HomeRoot is where @home is mounted.
	HomeRoot = "/home"
)

// ErrRefused means the request breaks a rule; the text says which.
var ErrRefused = errors.New("refused")

// ErrNotFound means no snapshot holds the file.
var ErrNotFound = errors.New("not in any snapshot")

// btrbk's long-iso names (snapshots.nix): home.20261021T030000+0200, with
// _N when two runs share a second.
var namePattern = regexp.MustCompile(`^home\.([0-9]{8}T[0-9]{6}[+-][0-9]{4})(_[0-9]{1,4})?$`)

// ValidName reports whether name is a home snapshot name btrbk makes.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// Time is when the snapshot was taken.
func Time(name string) (time.Time, error) {
	match := namePattern.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, fmt.Errorf("%w: %q is not a snapshot name", ErrRefused, name)
	}
	return time.Parse("20060102T150405-0700", match[1])
}

// Newest sorts snapshot names newest first and drops anything that is not
// one.
func Newest(names []string) []string {
	type entry struct {
		name string
		at   time.Time
	}
	var entries []entry
	for _, name := range names {
		if at, err := Time(name); err == nil {
			entries = append(entries, entry{name, at})
		}
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return strings.Compare(b.name, a.name)
	})
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.name
	}
	return out
}

// InHome turns an absolute path into the caller's home folder and the
// path inside it. home is the caller's home from the user database.
func InHome(home, path string) (homeName, rel string, err error) {
	if filepath.Dir(home) != HomeRoot || filepath.Clean(home) != home {
		return "", "", fmt.Errorf("%w: your home %s is not directly in %s, which is what gets snapshots", ErrRefused, home, HomeRoot)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", fmt.Errorf("%w: %q is not a clean absolute path", ErrRefused, path)
	}
	rel, err = filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", fmt.Errorf("%w: %s is not inside your home %s", ErrRefused, path, home)
	}
	return filepath.Base(home), rel, nil
}

var errLink = fmt.Errorf("%w: the path goes through a link in the snapshot; give the real path", ErrRefused)

// openStep opens one name in dir without following a link and without
// leaving the device of the snapshot. It is not openat2: the service runs
// with RestrictSUIDSGID, which blocks openat2 entirely. Snapshots are
// read-only, so nothing can swap a folder for a link between the steps.
func openStep(dir int, name string, flags int, dev uint64) (int, *unix.Stat_t, error) {
	if name == "" || name == "." || name == ".." {
		return -1, nil, fmt.Errorf("%w: %q is not a plain name", ErrRefused, name)
	}
	fd, err := unix.Openat(dir, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return -1, nil, ErrNotFound
	case errors.Is(err, unix.ELOOP):
		return -1, nil, errLink
	case err != nil:
		return -1, nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, nil, err
	}
	if st.Dev != dev {
		unix.Close(fd)
		return -1, nil, fmt.Errorf("%w: the path leaves the snapshot", ErrRefused)
	}
	return fd, &st, nil
}

// openDir opens name in dir as a folder. A link is refused, anything else
// that is not a folder is not found, as the path cannot go through it.
func openDir(dir int, name string, dev uint64) (int, *unix.Stat_t, error) {
	fd, st, err := openStep(dir, name, unix.O_PATH, dev)
	if err != nil {
		return -1, nil, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return fd, st, nil
	case unix.S_IFLNK:
		unix.Close(fd)
		return -1, nil, errLink
	default:
		unix.Close(fd)
		return -1, nil, ErrNotFound
	}
}

// Open opens rel in homeName of the snapshot folder snapshot for uid. It refuses anything uid does not own and anything but a
// regular file. The returned file is read-only.
func Open(snapshot, homeName, rel string, uid int) (*os.File, error) {
	root, err := unix.Open(snapshot, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer unix.Close(root)
	var rootSt unix.Stat_t
	if err := unix.Fstat(root, &rootSt); err != nil {
		return nil, err
	}

	dir, st, err := openDir(root, homeName, rootSt.Dev)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrRefused) {
			return nil, err
		}
		return nil, fmt.Errorf("open your home in the snapshot: %w", err)
	}
	if int(st.Uid) != uid {
		unix.Close(dir)
		return nil, fmt.Errorf("%w: in this snapshot, %s belonged to another user", ErrRefused, homeName)
	}

	parts := strings.Split(rel, "/")
	for _, name := range parts[:len(parts)-1] {
		next, _, err := openDir(dir, name, rootSt.Dev)
		unix.Close(dir)
		if err != nil {
			return nil, err
		}
		dir = next
	}
	fd, st, err := openStep(dir, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, rootSt.Dev)
	unix.Close(dir)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrRefused) {
			return nil, err
		}
		return nil, fmt.Errorf("open the file in the snapshot: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, fmt.Errorf("%w: only files can be restored, and this is not a plain file", ErrRefused)
	}
	if int(st.Uid) != uid {
		unix.Close(fd)
		return nil, fmt.Errorf("%w: in this snapshot, the file belonged to another user", ErrRefused)
	}
	return os.NewFile(uintptr(fd), filepath.Join(homeName, rel)), nil
}

// RestoredName is the new name next to the original: notes.txt restored
// from home.20261021T030000+0200 becomes notes.restored-20261021T030000.txt.
func RestoredName(base, snapshot string) string {
	stamp := strings.TrimPrefix(snapshot, "home.")
	if i := strings.IndexAny(stamp, "+-"); i > 0 {
		stamp = stamp[:i] + stamp[i+5:]
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" || stem == "." {
		return base + ".restored-" + stamp
	}
	return stem + ".restored-" + stamp + ext
}

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

// beneath opens without leaving dir, without symlinks and without
// crossing into another mount.
const beneath = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

// Open opens rel in homeName of the snapshot folder snapshot for uid. It refuses anything uid does not own and anything but a
// regular file. The returned file is read-only.
func Open(snapshot, homeName, rel string, uid int) (*os.File, error) {
	root, err := unix.Open(snapshot, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer unix.Close(root)

	homeFD, err := unix.Openat2(root, homeName, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: beneath,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open your home in the snapshot: %w", err)
	}
	defer unix.Close(homeFD)
	var st unix.Stat_t
	if err := unix.Fstat(homeFD, &st); err != nil {
		return nil, err
	}
	if int(st.Uid) != uid {
		return nil, fmt.Errorf("%w: in this snapshot, %s belonged to another user", ErrRefused, homeName)
	}

	fd, err := unix.Openat2(homeFD, rel, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOCTTY | unix.O_NONBLOCK,
		Resolve: beneath,
	})
	switch {
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return nil, ErrNotFound
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		return nil, fmt.Errorf("%w: the path goes through a link in the snapshot; give the real path", ErrRefused)
	case err != nil:
		return nil, fmt.Errorf("open the file in the snapshot: %w", err)
	}
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, err
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

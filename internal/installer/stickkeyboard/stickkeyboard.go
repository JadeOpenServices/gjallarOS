// Package stickkeyboard hands the keyboard layout picked in the installer
// stick's menu to the installer. The menu runs as root and writes one
// small file; the installer, also root, reads it back. The user session on
// the stick must not be able to change it, so both sides open every part
// of the path without following links and check that root owns it and
// nobody else may write it.
package stickkeyboard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// Dir and Name are where the layout lives: RAM, gone at the next boot.
const (
	Dir  = "/run/gjallar/stick"
	Name = "keyboard"
	// maxSize caps the read; a layout name is a few letters.
	maxSize = 64
)

var layoutName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Valid reports whether layout may be stored: a plain layout name.
func Valid(layout string) bool { return layoutName.MatchString(layout) }

// ErrNone means no layout was handed over: not on the stick, or nothing
// picked yet.
var ErrNone = errors.New("no keyboard layout from the stick menu")

// Write stores layout for the installer. It creates /run/gjallar and
// /run/gjallar/stick owned by root, mode 0755, and replaces the file
// through a new temporary file and a rename inside that folder.
func Write(layout string) error {
	return write("/", Dir, layout)
}

// Read returns the layout the stick menu stored, or ErrNone.
func Read() (string, error) {
	return read("/", Dir)
}

// write and read walk dir starting at base: "/" in real use, a private
// test folder in tests.
func write(base, dir, layout string) error {
	if !layoutName.MatchString(layout) {
		return fmt.Errorf("keyboard layout %q is not a layout name", layout)
	}
	fd, err := openDir(base, dir, true)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	tmp := Name + ".new"
	// A leftover from a crash is ours (the folder is root's alone);
	// removing it never follows a link.
	if err := unix.Unlinkat(fd, tmp, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("clear %s/%s: %w", dir, tmp, err)
	}
	f, err := unix.Openat(fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s/%s: %w", dir, tmp, err)
	}
	file := os.NewFile(uintptr(f), dir+"/"+tmp)
	_, werr := file.WriteString(layout + "\n")
	if werr == nil {
		werr = file.Sync()
	}
	if cerr := file.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = unix.Renameat(fd, tmp, fd, Name)
	}
	if werr != nil {
		_ = unix.Unlinkat(fd, tmp, 0)
		return fmt.Errorf("write %s/%s: %w", dir, Name, werr)
	}
	return nil
}

func read(base, dir string) (string, error) {
	fd, err := openDir(base, dir, false)
	if errors.Is(err, unix.ENOENT) {
		return "", ErrNone
	}
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	// O_NONBLOCK: a FIFO planted at the name must not hang the installer;
	// the fstat below refuses it.
	f, err := unix.Openat(fd, Name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return "", ErrNone
	}
	if err != nil {
		return "", fmt.Errorf("open %s/%s: %w", dir, Name, err)
	}
	file := os.NewFile(uintptr(f), dir+"/"+Name)
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(f, &st); err != nil {
		return "", fmt.Errorf("check %s/%s: %w", dir, Name, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != owner || st.Mode&0o022 != 0 || st.Size > maxSize {
		return "", fmt.Errorf("%s/%s is not a small file only root may change", dir, Name)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return "", fmt.Errorf("read %s/%s: %w", dir, Name, err)
	}
	layout := strings.TrimSuffix(string(data), "\n")
	if len(data) > maxSize || !layoutName.MatchString(layout) {
		return "", fmt.Errorf("%s/%s holds no layout name", dir, Name)
	}
	return layout, nil
}

// openDir opens dir one part at a time from base, never following a
// link, and checks that root owns base and every part and nobody else may
// write them. With create, missing parts are made 0755 (owned by root,
// who runs this).
func openDir(base, dir string, create bool) (int, error) {
	if !strings.HasPrefix(dir, "/") {
		return -1, fmt.Errorf("%s is not absolute", dir)
	}
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := rootOnly(fd, base); err != nil {
		unix.Close(fd)
		return -1, err
	}
	at := ""
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return -1, fmt.Errorf("%s is not a plain path", dir)
		}
		at += "/" + part
		if create {
			if err := unix.Mkdirat(fd, part, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return -1, fmt.Errorf("create %s: %w", at, err)
			}
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return -1, fmt.Errorf("%s is not a plain folder", at)
			}
			return -1, err
		}
		fd = next
		if err := rootOnly(fd, at); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

// owner is the uid the folders and the file must have: root. Tests set
// their own uid.
var owner uint32 = 0

func rootOnly(fd int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("check %s: %w", name, err)
	}
	// Group or other write on any part (a /tmp-like folder, a loose
	// mode) would let someone swap what comes below it.
	if st.Uid != owner || st.Mode&0o022 != 0 {
		return fmt.Errorf("%s belongs to someone else or others may change it", name)
	}
	return nil
}

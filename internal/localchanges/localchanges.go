// Package localchanges checks the developer-mode folder: Nix files the
// owner adds to their own system.
//
// The folder is data until root copies it into Path. A rebuild reads it from
// there, checks it again and stages it next to the official source; the
// flake imports system/*.nix as NixOS modules and home/*.nix as
// home-manager modules. Names, file types and sizes are fixed here so the
// copy, the rebuild and the status listing agree on what a folder may hold.
package localchanges

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Path is root's copy of the folder, the one a rebuild builds. It has a
// parent of its own, so the settings service can swap it without write
// access to the rest of /etc/gjallar.
const Path = "/etc/gjallar/developer/local"

const (
	MaxFiles      = 256
	MaxFileBytes  = 1 << 20
	MaxTotalBytes = 4 << 20
	MaxDepth      = 4
)

// Extensions are the file types a folder may hold: Nix code and the few
// asset types a module reads with a relative path.
var Extensions = []string{".nix", ".json", ".toml", ".yaml", ".yml", ".conf", ".txt", ".md", ".png", ".jpg", ".jpeg", ".svg"}

var (
	// Test hooks: tests run as an ordinary user in a temporary directory.
	rootUID      uint32 = 0
	checkParents        = true
)

var componentPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)

// File is one file of a folder.
type File struct {
	Path   string
	Data   []byte
	SHA256 string
}

// Tree is a checked folder, sorted by path.
type Tree struct {
	Files []File
}

// CheckName fails unless rel is a slash-separated path the folder may hold:
// README.md, or a file below system/ or home/ with a known extension, no
// hidden or odd component and at most MaxDepth levels deep.
func CheckName(rel string) error {
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("%q is not a plain relative path", rel)
	}
	parts := strings.Split(rel, "/")
	if len(parts) > MaxDepth {
		return fmt.Errorf("%s is more than %d levels deep", rel, MaxDepth)
	}
	for _, part := range parts {
		if !componentPattern.MatchString(part) {
			return fmt.Errorf("%s: %q is hidden or holds characters other than letters, digits and . _ + -", rel, part)
		}
	}
	if len(parts) == 1 {
		if rel != "README.md" {
			return fmt.Errorf("%s: only README.md, system/ and home/ belong at the top", rel)
		}
		return nil
	}
	if parts[0] != "system" && parts[0] != "home" {
		return fmt.Errorf("%s: only README.md, system/ and home/ belong at the top", rel)
	}
	ext := strings.ToLower(path.Ext(rel))
	for _, allowed := range Extensions {
		if ext == allowed {
			return nil
		}
	}
	return fmt.Errorf("%s: only %s files are allowed", rel, strings.Join(Extensions, " "))
}

// Check validates files (keyed by relative path) and returns them as a tree.
func Check(files map[string][]byte) (Tree, error) {
	if len(files) > MaxFiles {
		return Tree{}, fmt.Errorf("the folder holds %d files; at most %d are allowed", len(files), MaxFiles)
	}
	tree := Tree{}
	total := 0
	for rel, data := range files {
		if err := CheckName(rel); err != nil {
			return Tree{}, err
		}
		if len(data) > MaxFileBytes {
			return Tree{}, fmt.Errorf("%s holds %d bytes; at most %d are allowed", rel, len(data), MaxFileBytes)
		}
		total += len(data)
		sum := sha256.Sum256(data)
		tree.Files = append(tree.Files, File{Path: rel, Data: data, SHA256: hex.EncodeToString(sum[:])})
	}
	if total > MaxTotalBytes {
		return Tree{}, fmt.Errorf("the folder holds %d bytes; at most %d are allowed", total, MaxTotalBytes)
	}
	sort.Slice(tree.Files, func(i, j int) bool { return tree.Files[i].Path < tree.Files[j].Path })
	return tree, nil
}

// Empty reports whether the tree holds no Nix file to build.
func (t Tree) Empty() bool {
	for _, file := range t.Files {
		if strings.HasSuffix(file.Path, ".nix") {
			return false
		}
	}
	return true
}

// Paths lists the files of the tree.
func (t Tree) Paths() []string {
	paths := make([]string, len(t.Files))
	for i, file := range t.Files {
		paths[i] = file.Path
	}
	return paths
}

// Hash is the SHA-256 of the tree's manifest: one "<sha256>  <path>" line
// per file, sorted by path, as sha256sum prints it. The flake computes the
// same value from the staged copy.
func (t Tree) Hash() string {
	var manifest strings.Builder
	for _, file := range t.Files {
		fmt.Fprintf(&manifest, "%s  %s\n", file.SHA256, file.Path)
	}
	sum := sha256.Sum256([]byte(manifest.String()))
	return hex.EncodeToString(sum[:])
}

// ReadRoot reads root's copy at dir. A missing copy is an empty tree. It
// fails on anything not owned by root or writable by group or others, on a
// directory above it that someone else could change, on symlinks and on
// anything but plain files and directories.
func ReadRoot(dir string) (Tree, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Tree{}, nil
	}
	if err != nil {
		return Tree{}, err
	}
	if !info.IsDir() {
		return Tree{}, fmt.Errorf("%s is not a directory", dir)
	}
	for parent := filepath.Dir(dir); checkParents; parent = filepath.Dir(parent) {
		if err := checkOwned(parent); err != nil {
			return Tree{}, err
		}
		if parent == "/" {
			break
		}
	}
	return read(dir, checkOwned)
}

// ReadDir reads the owner's own folder at dir, as that user, so the folder
// can be sent to root as data. It takes the same files as ReadRoot but does
// not ask who owns them; symlinks and anything but plain files and
// directories still fail.
func ReadDir(dir string) (Tree, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return Tree{}, err
	}
	if !info.IsDir() {
		return Tree{}, fmt.Errorf("%s is not a directory", dir)
	}
	return read(dir, func(name string) error {
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", name)
		}
		return nil
	})
}

func read(dir string, check func(string) error) (Tree, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := check(name); err != nil {
			return err
		}
		if name == dir || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is not a plain file", name)
		}
		if len(files) >= MaxFiles {
			return fmt.Errorf("%s holds more than %d files", dir, MaxFiles)
		}
		if info, err := entry.Info(); err != nil {
			return err
		} else if info.Size() > MaxFileBytes {
			return fmt.Errorf("%s holds %d bytes; at most %d are allowed", name, info.Size(), MaxFileBytes)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return Tree{}, err
	}
	tree, err := Check(files)
	if err != nil {
		return Tree{}, fmt.Errorf("%s: %w", dir, err)
	}
	return tree, nil
}

// Write creates the tree under dir, which must not exist yet. Directories
// get mode 0755 and files 0644 whatever the umask, so a rebuild can read a
// copy the settings service wrote with umask 0077.
func Write(dir string, tree Tree) error {
	if err := mkdir(dir); err != nil {
		return err
	}
	for _, file := range tree.Files {
		target := dir
		parts := strings.Split(file.Path, "/")
		for _, part := range parts[:len(parts)-1] {
			target = filepath.Join(target, part)
			if err := mkdir(target); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		}
		target = filepath.Join(target, parts[len(parts)-1])
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
		if err != nil {
			return err
		}
		if err := out.Chmod(0o644); err != nil {
			out.Close()
			return err
		}
		if _, err := out.Write(file.Data); err != nil {
			out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

func mkdir(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	return os.Chmod(dir, 0o755)
}

// Replace makes tree the folder at dir in one step: it writes the tree next
// to dir and swaps the two, so a rebuild reads the old folder or the new
// one, never a mix. A tree without files removes the folder.
func Replace(dir string, tree Tree) error {
	next := dir + ".new"
	if err := os.RemoveAll(next); err != nil {
		return err
	}
	if len(tree.Files) == 0 {
		return os.RemoveAll(dir)
	}
	if err := Write(next, tree); err != nil {
		os.RemoveAll(next)
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.Rename(next, dir); err != nil {
			os.RemoveAll(next)
			return err
		}
		return nil
	}
	if err := unix.Renameat2(unix.AT_FDCWD, next, unix.AT_FDCWD, dir, unix.RENAME_EXCHANGE); err != nil {
		os.RemoveAll(next)
		return fmt.Errorf("swap %s: %w", dir, err)
	}
	// next now holds the old folder.
	return os.RemoveAll(next)
}

func checkOwned(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", name)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has no owner information", name)
	}
	if stat.Uid != rootUID {
		return fmt.Errorf("%s is owned by uid %d, not root", name, stat.Uid)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others", name)
	}
	return nil
}

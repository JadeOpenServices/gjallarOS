package winesandbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf16"
)

// An installer says which program it installed through the shortcuts it
// makes. These are read from the prefix, which the app can write, so the
// reader is bounded and trusts nothing in them but a path it checks.

const (
	maxShortcuts    = 200
	maxShortcutSize = 64 << 10
)

// shortcutDirs are where installers put shortcuts, below drive_c. "*"
// stands for each user folder.
var shortcutDirs = []string{
	"ProgramData/Microsoft/Windows/Start Menu/Programs",
	"users/*/AppData/Roaming/Microsoft/Windows/Start Menu/Programs",
	"users/*/Desktop",
}

// ShortcutTarget reads the local path a Shell Link (.lnk) file points at,
// from its LinkInfo part ([MS-SHLLINK] 2.3).
func ShortcutTarget(data []byte) (string, error) {
	errBad := errors.New("not a shortcut with a local path")
	u32 := func(off int) (int, bool) {
		if off < 0 || off+4 > len(data) {
			return 0, false
		}
		return int(binary.LittleEndian.Uint32(data[off:])), true
	}
	if size, ok := u32(0); !ok || size != 0x4c || len(data) < 0x4c {
		return "", errBad
	}
	flags, _ := u32(0x14)
	off := 0x4c
	if flags&0x1 != 0 { // HasLinkTargetIDList
		if off+2 > len(data) {
			return "", errBad
		}
		off += 2 + int(binary.LittleEndian.Uint16(data[off:]))
	}
	if flags&0x2 == 0 { // HasLinkInfo
		return "", errBad
	}
	info := off
	size, ok := u32(info)
	if !ok || size < 0x1c || info+size > len(data) {
		return "", errBad
	}
	block := data[info : info+size]
	field := func(at int) int {
		v, ok := u32(info + at)
		if !ok || at+4 > len(block) {
			return 0
		}
		return v
	}
	headerSize, infoFlags := field(4), field(8)
	if infoFlags&0x1 == 0 { // VolumeIDAndLocalBasePath
		return "", errBad
	}
	ansi := func(at int) (string, bool) {
		if at <= 0 || at >= len(block) {
			return "", false
		}
		end := slices.Index(block[at:], 0)
		if end < 0 {
			return "", false
		}
		s := block[at : at+end]
		for _, c := range s {
			if c >= 0x80 {
				return "", false
			}
		}
		return string(s), true
	}
	wide := func(at int) (string, bool) {
		if at <= 0 || at >= len(block) {
			return "", false
		}
		var units []uint16
		for i := at; i+1 < len(block); i += 2 {
			u := binary.LittleEndian.Uint16(block[i:])
			if u == 0 {
				return string(utf16.Decode(units)), true
			}
			units = append(units, u)
		}
		return "", false
	}
	var base, suffix string
	if headerSize >= 0x24 {
		base, ok = wide(field(0x1c))
		if ok {
			suffix, ok = wide(field(0x20))
		}
	}
	if !ok || headerSize < 0x24 {
		if base, ok = ansi(field(0x10)); !ok {
			return "", errBad
		}
		if suffix, ok = ansi(field(0x18)); !ok {
			return "", errBad
		}
	}
	if base == "" {
		return "", errBad
	}
	return base + suffix, nil
}

// Shortcuts lists the shortcut files in a prefix, with their size and
// time, so a later call shows which ones an installer made.
func Shortcuts(prefix string) (map[string]fs.FileInfo, error) {
	found := map[string]fs.FileInfo{}
	driveC := filepath.Join(prefix, "drive_c")
	for _, pattern := range shortcutDirs {
		dirs, err := filepath.Glob(filepath.Join(driveC, pattern))
		if err != nil {
			return nil, err
		}
		for _, dir := range dirs {
			// A link would lead the walk out of the prefix.
			if realDir(dir) != nil {
				continue
			}
			err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					// A folder the app made unreadable hides only its own shortcuts.
					if path == dir || d != nil && d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				if !d.Type().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".lnk") {
					return nil
				}
				if len(found) >= maxShortcuts {
					return fs.SkipAll
				}
				info, err := d.Info()
				if err == nil {
					found[path] = info
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return found, nil
}

// Programs gives the programs the shortcuts in a prefix start: .exe files
// on C: that exist, without uninstallers, sorted and each once. With
// before, only shortcuts that are new or changed since count.
func Programs(paths Paths, before map[string]fs.FileInfo) ([]string, error) {
	now, err := Shortcuts(paths.Prefix)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var programs []string
	for path, info := range now {
		if old, ok := before[path]; ok && old.Size() == info.Size() && old.ModTime().Equal(info.ModTime()) {
			continue
		}
		target, err := readShortcut(path)
		if err != nil || !strings.EqualFold(filepath.Ext(target), ".exe") {
			continue
		}
		name := strings.ToLower(target[strings.LastIndexByte(target, '\\')+1:])
		if strings.Contains(name, "unins") || strings.Contains(name, "uninst") {
			continue
		}
		host, err := HostPath(paths, target)
		if err != nil || !strings.EqualFold(target[:1], "c") {
			continue
		}
		if win, err := WinPath(paths, host); err != nil || !strings.EqualFold(win, target) {
			continue
		}
		if info, err := os.Stat(host); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if key := strings.ToLower(target); !seen[key] {
			seen[key] = true
			programs = append(programs, target)
		}
	}
	slices.SortFunc(programs, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return programs, nil
}

// readShortcut reads a shortcut that must still be a plain file: the app
// could have swapped it for a link or a pipe since the walk.
func readShortcut(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a plain file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxShortcutSize))
	if err != nil {
		return "", err
	}
	return ShortcutTarget(data)
}

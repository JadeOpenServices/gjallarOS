package winesandbox

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// shortcut builds a SYNTHETIC Shell Link that points at target, with the
// path as ANSI text or, when wide, as UTF-16 too.
func shortcut(target string, wide, idList bool) []byte {
	le := binary.LittleEndian
	data := make([]byte, 0x4c)
	le.PutUint32(data, 0x4c)
	flags := uint32(0x2)
	if idList {
		flags |= 0x1
		data = le.AppendUint16(data, 4)
		data = append(data, 0, 0, 0, 0)
	}
	le.PutUint32(data[0x14:], flags)

	headerSize := 0x1c
	if wide {
		headerSize = 0x24
	}
	var strs []byte
	at := func(b []byte) uint32 {
		off := uint32(headerSize + len(strs))
		strs = append(strs, b...)
		return off
	}
	info := make([]byte, headerSize)
	le.PutUint32(info[4:], uint32(headerSize))
	le.PutUint32(info[8:], 1)
	le.PutUint32(info[0x10:], at(append([]byte(target), 0)))
	le.PutUint32(info[0x18:], at([]byte{0}))
	if wide {
		var w []byte
		for _, u := range utf16.Encode([]rune(target)) {
			w = le.AppendUint16(w, u)
		}
		le.PutUint32(info[0x1c:], at(append(w, 0, 0)))
		le.PutUint32(info[0x20:], at([]byte{0, 0}))
	}
	info = append(info, strs...)
	le.PutUint32(info, uint32(len(info)))
	return append(data, info...)
}

func TestShortcutTarget(t *testing.T) {
	const target = `C:\Program Files\Synthetic\app.exe`
	for _, c := range []struct {
		name         string
		wide, idList bool
	}{{"ansi", false, false}, {"wide", true, false}, {"id list", true, true}} {
		got, err := ShortcutTarget(shortcut(target, c.wide, c.idList))
		if err != nil || got != target {
			t.Errorf("%s: %q %v", c.name, got, err)
		}
	}
	got, err := ShortcutTarget(shortcut(`C:\Synthetic\Ünï.exe`, true, false))
	if err != nil || got != `C:\Synthetic\Ünï.exe` {
		t.Errorf("wide text: %q %v", got, err)
	}
	if _, err := ShortcutTarget(shortcut(`C:\Synthetic\Ünï.exe`, false, false)); err == nil {
		t.Error("ANSI text past ASCII accepted")
	}

	good := shortcut(target, true, false)
	for i := range len(good) {
		ShortcutTarget(good[:i]) // must not panic
	}
	noInfo := slices.Clone(good)
	binary.LittleEndian.PutUint32(noInfo[0x14:], 0)
	huge := slices.Clone(good)
	binary.LittleEndian.PutUint32(huge[0x4c:], 1<<31)
	for name, data := range map[string][]byte{
		"empty":     nil,
		"cut":       good[:0x60],
		"no info":   noInfo,
		"too large": huge,
		"no header": append([]byte{0x4d}, good[1:]...),
	} {
		if _, err := ShortcutTarget(data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPrograms(t *testing.T) {
	home := t.TempDir()
	paths := PathsFor(home, "synthetic", "Synthetic App")
	driveC := filepath.Join(paths.Prefix, "drive_c")
	menu := filepath.Join(driveC, "ProgramData", "Microsoft", "Windows", "Start Menu", "Programs", "Synthetic")
	desktop := filepath.Join(driveC, "users", "tester", "Desktop")
	for _, dir := range []string{menu, desktop, paths.Files} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(driveC, "Program Files", "Synthetic", "app.exe"), []byte("SYNTHETIC"))
	write(filepath.Join(driveC, "Program Files", "Synthetic", "unins000.exe"), []byte("SYNTHETIC"))
	write(filepath.Join(driveC, "Program Files", "Old", "old.exe"), []byte("SYNTHETIC"))
	write(filepath.Join(paths.Files, "files.exe"), []byte("SYNTHETIC"))

	write(filepath.Join(menu, "Old.lnk"), shortcut(`C:\Program Files\Old\old.exe`, false, false))
	before, err := Shortcuts(paths.Prefix)
	if err != nil || len(before) != 1 {
		t.Fatalf("before: %v %v", before, err)
	}

	write(filepath.Join(menu, "App.lnk"), shortcut(`C:\Program Files\Synthetic\app.exe`, true, false))
	write(filepath.Join(desktop, "App.LNK"), shortcut(`c:\program files\synthetic\APP.exe`, false, false))
	write(filepath.Join(menu, "Uninstall.lnk"), shortcut(`C:\Program Files\Synthetic\unins000.exe`, true, false))
	write(filepath.Join(menu, "Readme.lnk"), shortcut(`C:\Program Files\Synthetic\readme.txt`, true, false))
	write(filepath.Join(menu, "Gone.lnk"), shortcut(`C:\Program Files\Synthetic\gone.exe`, true, false))
	write(filepath.Join(menu, "Files.lnk"), shortcut(`D:\files.exe`, true, false))
	write(filepath.Join(menu, "Climb.lnk"), shortcut(`C:\..\..\..\files\files.exe`, true, false))
	write(filepath.Join(menu, "Junk.lnk"), []byte("SYNTHETIC"))
	if err := os.Symlink(filepath.Join(menu, "App.lnk"), filepath.Join(menu, "Link.lnk")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere.exe"), filepath.Join(driveC, "Program Files", "Synthetic", "out.exe")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(home, "elsewhere.exe"), []byte("SYNTHETIC"))
	write(filepath.Join(menu, "Out.lnk"), shortcut(`C:\Program Files\Synthetic\out.exe`, true, false))

	got, err := Programs(paths, before)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`C:\Program Files\Synthetic\app.exe`}; !slices.EqualFunc(got, want, strings.EqualFold) {
		t.Errorf("new programs %q, want %q", got, want)
	}
	all, err := Programs(paths, nil)
	if err != nil || len(all) != 2 || all[0] != `C:\Program Files\Old\old.exe` {
		t.Errorf("all programs %q %v", all, err)
	}

	// A shortcut changed since counts again.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(menu, "Old.lnk"), later, later); err != nil {
		t.Fatal(err)
	}
	if got, err := Programs(paths, before); err != nil || len(got) != 2 {
		t.Errorf("changed shortcut: %q %v", got, err)
	}
}

func TestShortcutsStayInPrefix(t *testing.T) {
	prefix := t.TempDir()
	menu := filepath.Join(prefix, "drive_c", "ProgramData", "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(menu, 0o700); err != nil {
		t.Fatal(err)
	}
	// A user folder that leads out of the prefix is not walked.
	users := filepath.Join(prefix, "drive_c", "users")
	if err := os.MkdirAll(users, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "Desktop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "Desktop", "outside.lnk"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(users, "tester")); err != nil {
		t.Fatal(err)
	}
	if found, err := Shortcuts(prefix); err != nil || len(found) != 0 {
		t.Errorf("walked out of the prefix: %v %v", found, err)
	}
}

func TestShortcutsLimit(t *testing.T) {
	prefix := t.TempDir()
	menu := filepath.Join(prefix, "drive_c", "ProgramData", "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(menu, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxShortcuts + 20 {
		if err := os.WriteFile(filepath.Join(menu, fmt.Sprintf("%03d.lnk", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := Shortcuts(prefix)
	if err != nil || len(found) != maxShortcuts {
		t.Errorf("found %d %v", len(found), err)
	}
}

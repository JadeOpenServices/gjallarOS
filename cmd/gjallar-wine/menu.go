package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// Each app with a program has an entry in the app menu that starts it
// through gjallar-wine run. Wine's own menu builder is off in the sandbox
// (WINEDLLOVERRIDES winemenubuilder.exe=d): its entries would start the
// program outside it. The entries carry menuMarker, so the sync below only
// ever removes files it wrote.

// menuMarker is the key every entry gjallar-wine writes has.
const menuMarker = "X-Gjallar-Wine-App"

// dataDir is the user's own data folder, which holds their menu entries
// and icons.
func dataDir(home string) string {
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local", "share")
	}
	return data
}

// menuDir is the folder of the user's own menu entries.
func menuDir(home string) string {
	return filepath.Join(dataDir(home), "applications")
}

// menuFile is app id's menu entry.
func menuFile(home, id string) string {
	return filepath.Join(menuDir(home), "gjallar-wine-"+id+".desktop")
}

// menuEntry is app id's menu entry, with icon from the icon theme. TryExec
// hides it once Wine is turned off and gjallar-wine is gone. The name has
// no control characters or backslashes (ValidName), so it needs no escapes.
func menuEntry(id string, a winesandbox.App, icon string) string {
	entry := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + a.Name + "\n" +
		"Comment=Wine app\n" +
		"Exec=gjallar-wine run " + id + "\n" +
		"TryExec=gjallar-wine\n" +
		"Icon=" + icon + "\n" +
		"Terminal=false\n"
	if class := windowClass(a.Exe); class != "" {
		entry += "StartupWMClass=" + class + "\n"
	}
	return entry + menuMarker + "=" + id + "\n"
}

// windowClass is the window class Wine gives the program's windows, so the
// dock and the task switcher put them with the menu entry: the program's
// file name, lowercased (setup_options in dlls/winex11.drv/x11drv_main.c).
// A name with a character an entry value cannot hold as it is, or that is
// not UTF-8, gets no class.
func windowClass(exe string) string {
	name := exe[strings.LastIndexAny(exe, `\/`)+1:]
	if name == "" || len(name) > 255 {
		return ""
	}
	for _, r := range name {
		if !unicode.IsPrint(r) || r == utf8.RuneError {
			return ""
		}
	}
	return strings.ToLower(name)
}

// setMenu writes app id's menu entry and icon when it has a program and
// removes them when not. The file is replaced in one step. An icon that
// cannot be read is only a warning: the entry shows Wine's.
func setMenu(paths winesandbox.Paths, id string, a winesandbox.App, stderr io.Writer) error {
	home := paths.Home
	path := menuFile(home, id)
	if a.Exe == "" {
		return removeMenu(home, id)
	}
	icon, err := setIcon(paths, id, a)
	if err != nil {
		fmt.Fprintf(stderr, "WARNING: app %s shows Wine's icon in the app menu: %v\n", id, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gjallar-wine-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(menuEntry(id, a, icon)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// removeMenu removes app id's icon, and its menu entry if gjallar-wine
// wrote it.
func removeMenu(home, id string) error {
	removeIcons(home, id)
	path := menuFile(home, id)
	ours, err := isMenuEntry(path)
	if err != nil || !ours {
		return err
	}
	return os.Remove(path)
}

// isMenuEntry says whether path is a plain file with menuMarker. A file
// that is not there is not one.
func isMenuEntry(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	lines := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for lines.Scan() {
		if strings.HasPrefix(lines.Text(), menuMarker+"=") {
			return true, nil
		}
	}
	return false, lines.Err()
}

// saveApp writes app id's record and then its menu entry. A menu entry
// that cannot be written is only a warning: the app still starts with
// gjallar-wine run.
func saveApp(paths winesandbox.Paths, id string, a winesandbox.App, stderr io.Writer) error {
	if err := winesandbox.WriteRecord(paths, a); err != nil {
		return err
	}
	if err := setMenu(paths, id, a, stderr); err != nil {
		fmt.Fprintf(stderr, "WARNING: app %s has no entry in the app menu: %v\n", id, err)
	}
	return nil
}

// menuCmd brings the menu in line with the records: an entry for each app
// with a program, none for the others or for apps that are gone. It runs
// after each rebuild, for apps set up before the menu entries came.
func menuCmd(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		return errUsage
	}
	_, home, err := currentUser()
	if err != nil {
		return err
	}
	apps, err := winesandbox.Apps(home)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, app := range apps {
		known[app.ID] = true
		a := app.App
		if app.Err != nil {
			a.Exe = ""
		}
		paths := winesandbox.PathsFor(home, app.ID, a.Name)
		if err := setMenu(paths, app.ID, a, stderr); err != nil {
			fmt.Fprintf(stderr, "WARNING: menu entry of app %s: %v\n", app.ID, err)
		}
	}
	for _, id := range iconIDs(home) {
		if !known[id] {
			removeIcons(home, id)
		}
	}
	entries, err := os.ReadDir(menuDir(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		rest, ok := strings.CutPrefix(entry.Name(), "gjallar-wine-")
		id, ok2 := strings.CutSuffix(rest, ".desktop")
		if !ok || !ok2 || !winesandbox.ValidID(id) || known[id] {
			continue
		}
		if err := removeMenu(home, id); err != nil {
			fmt.Fprintf(stderr, "WARNING: menu entry of removed app %s: %v\n", id, err)
		}
	}
	return nil
}

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// Each app with a program has an entry in the app menu that starts it
// through gjallar-wine run. Wine's own menu builder is off in the sandbox
// (WINEDLLOVERRIDES winemenubuilder.exe=d): its entries would start the
// program outside it. The entries carry menuMarker, so the sync below only
// ever removes files it wrote.

// menuMarker is the key every entry gjallar-wine writes has.
const menuMarker = "X-Gjallar-Wine-App"

// menuDir is the folder of the user's own menu entries.
func menuDir(home string) string {
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "applications")
}

// menuFile is app id's menu entry.
func menuFile(home, id string) string {
	return filepath.Join(menuDir(home), "gjallar-wine-"+id+".desktop")
}

// menuEntry is the menu entry of app id named name. TryExec hides it once
// Wine is turned off and gjallar-wine is gone. The name has no control
// characters or backslashes (ValidName), so it needs no escapes.
func menuEntry(id, name string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + name + "\n" +
		"Comment=Wine app\n" +
		"Exec=gjallar-wine run " + id + "\n" +
		"TryExec=gjallar-wine\n" +
		"Icon=wine\n" +
		"Terminal=false\n" +
		menuMarker + "=" + id + "\n"
}

// setMenu writes app id's menu entry when it has a program and removes it
// when not. The file is replaced in one step.
func setMenu(home, id string, a winesandbox.App) error {
	path := menuFile(home, id)
	if a.Exe == "" {
		return removeMenu(home, id)
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
	if _, err := tmp.WriteString(menuEntry(id, a.Name)); err != nil {
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

// removeMenu removes app id's menu entry if gjallar-wine wrote it.
func removeMenu(home, id string) error {
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
	if err := setMenu(paths.Home, id, a); err != nil {
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
		if err := setMenu(home, app.ID, a); err != nil {
			fmt.Fprintf(stderr, "WARNING: menu entry of app %s: %v\n", app.ID, err)
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

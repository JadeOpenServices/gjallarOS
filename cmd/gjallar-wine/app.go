package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// An app's parts on disk. Its record sits outside the prefix, so the app
// cannot rewrite what starts it.
//
//	~/.local/share/gjallar-wine/apps/<id>.json   record
//	~/.local/share/gjallar-wine/prefixes/<id>    Wine prefix
//	~/WineApps/<Name>                            files folder

const maxNameLen = 64

type app struct {
	// Name is the files folder's name and what the user sees.
	Name string `json:"name"`
	// Exe is the program Wine starts, as a path inside the prefix
	// (C:\...) or in the files folder.
	Exe string `json:"exe"`
}

// appPaths are one app's folders, below a home without symbolic links.
type appPaths struct {
	Home, Record, Prefix, Files string
}

func pathsFor(home, id, name string) appPaths {
	data := filepath.Join(home, ".local", "share", "gjallar-wine")
	return appPaths{
		Home:   home,
		Record: filepath.Join(data, "apps", id+".json"),
		Prefix: filepath.Join(data, "prefixes", id),
		Files:  filepath.Join(home, "WineApps", name),
	}
}

// validName accepts a folder name a person would type: no slashes, no
// control characters, not hidden.
func validName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxNameLen || !utf8.ValidString(name) ||
		strings.HasPrefix(name, ".") || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// loadApp reads app id's record and checks its folders.
func loadApp(home, id string) (app, appPaths, error) {
	if !winesandbox.ValidID(id) {
		return app{}, appPaths{}, fmt.Errorf("%q is not an app id", id)
	}
	record := pathsFor(home, id, "").Record
	data, err := os.ReadFile(record)
	if errors.Is(err, os.ErrNotExist) {
		return app{}, appPaths{}, fmt.Errorf("no app %s; gjallarctl wine list shows the apps", id)
	}
	if err != nil {
		return app{}, appPaths{}, err
	}
	var a app
	if err := json.Unmarshal(data, &a); err != nil {
		return app{}, appPaths{}, fmt.Errorf("read %s: %w", record, err)
	}
	if !validName(a.Name) {
		return app{}, appPaths{}, fmt.Errorf("%s: %q is not a folder name", record, a.Name)
	}
	if a.Exe == "" || strings.ContainsRune(a.Exe, 0) {
		return app{}, appPaths{}, fmt.Errorf("%s names no program", record)
	}
	paths := pathsFor(home, id, a.Name)
	for _, dir := range []string{paths.Prefix, paths.Files} {
		if err := realDir(dir); err != nil {
			return app{}, appPaths{}, err
		}
	}
	return a, paths, nil
}

// realDir checks that dir is a folder reached without symbolic links:
// bwrap would follow one and give the app whatever it points at.
func realDir(dir string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if resolved != dir {
		return fmt.Errorf("%s leads through a symbolic link to %s", dir, resolved)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	return nil
}

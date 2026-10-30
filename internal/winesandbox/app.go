package winesandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// An app's parts on disk. Its record sits outside the prefix, so the app
// cannot rewrite what starts it.
//
//	~/.local/share/gjallar-wine/apps/<id>.json   record
//	~/.local/share/gjallar-wine/prefixes/<id>    Wine prefix
//	~/WineApps/<Name>                            files folder

// MaxNameLen is the longest app name, in characters.
const MaxNameLen = 64

// App is an app's record.
type App struct {
	// Name is the files folder's name and what the user sees.
	Name string `json:"name"`
	// Exe is the program Wine starts, as a path inside the prefix
	// (C:\...) or in the files folder.
	Exe string `json:"exe"`
}

// Paths are one app's folders, below a home without symbolic links.
type Paths struct {
	Home, Record, Prefix, Files string
}

// PathsFor gives app id's folders below home.
func PathsFor(home, id, name string) Paths {
	data := filepath.Join(home, ".local", "share", "gjallar-wine")
	return Paths{
		Home:   home,
		Record: filepath.Join(data, "apps", id+".json"),
		Prefix: filepath.Join(data, "prefixes", id),
		Files:  filepath.Join(home, "WineApps", name),
	}
}

// ValidName accepts a folder name a person would type: no slashes, no
// control characters, not hidden.
func ValidName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen || !utf8.ValidString(name) ||
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

// LoadApp reads app id's record and checks its folders.
func LoadApp(home, id string) (App, Paths, error) {
	if !ValidID(id) {
		return App{}, Paths{}, fmt.Errorf("%q is not an app id", id)
	}
	a, err := readRecord(PathsFor(home, id, "").Record)
	if errors.Is(err, os.ErrNotExist) {
		return App{}, Paths{}, fmt.Errorf("no app %s; gjallarctl wine list shows the apps", id)
	}
	if err != nil {
		return App{}, Paths{}, err
	}
	paths := PathsFor(home, id, a.Name)
	for _, dir := range []string{paths.Prefix, paths.Files} {
		if err := realDir(dir); err != nil {
			return App{}, Paths{}, err
		}
	}
	return a, paths, nil
}

// readRecord reads and checks one app record, which must be a plain file.
func readRecord(record string) (App, error) {
	info, err := os.Lstat(record)
	if err != nil {
		return App{}, err
	}
	if !info.Mode().IsRegular() {
		return App{}, fmt.Errorf("%s is not a plain file", record)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		return App{}, err
	}
	var a App
	if err := json.Unmarshal(data, &a); err != nil {
		return App{}, fmt.Errorf("read %s: %w", record, err)
	}
	if !ValidName(a.Name) {
		return App{}, fmt.Errorf("%s: %q is not a folder name", record, a.Name)
	}
	if a.Exe == "" || strings.ContainsRune(a.Exe, 0) {
		return App{}, fmt.Errorf("%s names no program", record)
	}
	return a, nil
}

// Listed is one app record as Apps found it. Err says why the record
// cannot start its app.
type Listed struct {
	ID  string
	App App
	Err error
}

// Apps lists the app records in home, by id. No records folder means no
// apps.
func Apps(home string) ([]Listed, error) {
	dir := filepath.Dir(PathsFor(home, "x", "").Record)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var apps []Listed
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !ValidID(id) || !entry.Type().IsRegular() {
			continue
		}
		a, err := readRecord(filepath.Join(dir, entry.Name()))
		apps = append(apps, Listed{ID: id, App: a, Err: err})
	}
	return apps, nil
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

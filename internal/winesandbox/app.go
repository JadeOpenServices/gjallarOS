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
	// (C:\...) or in the files folder (D:\...). Empty until one is chosen.
	Exe string `json:"exe"`
}

// Ready says whether app id has a program to start.
func (a App) Ready(id string) error {
	if a.Exe == "" {
		return fmt.Errorf("no program chosen yet; gjallar-wine program %s shows the choices", id)
	}
	return nil
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
		if err := RealDir(dir); err != nil {
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
	if strings.ContainsRune(a.Exe, 0) {
		return App{}, fmt.Errorf("%s: the program's path is broken", record)
	}
	return a, nil
}

// WriteRecord saves app a at paths.Record in one step, so a reader sees
// the old record or the new one.
func WriteRecord(paths Paths, a App) error {
	if !ValidName(a.Name) || strings.ContainsRune(a.Exe, 0) {
		return fmt.Errorf("%q is not a folder name", a.Name)
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(paths.Record)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), paths.Record)
}

// IDFromName makes an app id from a name: letters and digits kept in
// lowercase, everything else one dash.
func IDFromName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	id := b.String()
	if id == "" || id[0] < 'a' {
		id = "app-" + id
	}
	if len(id) > MaxID {
		id = id[:MaxID]
	}
	return strings.Trim(id, "-")
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
		if err == nil {
			err = a.Ready(id)
		}
		apps = append(apps, Listed{ID: id, App: a, Err: err})
	}
	return apps, nil
}

// RemoveApp removes app id's record and prefix, and with files its files
// folder too. The record goes first, so a half-removed app no longer
// starts. A record that is gone leaves a prefix to remove, but no files
// folder to find.
func RemoveApp(home, id string, files bool) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not an app id", id)
	}
	paths := PathsFor(home, id, "")
	a, err := readRecord(paths.Record)
	known := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if files && !known {
		return fmt.Errorf("app %s has no record, so its files folder is not known; remove it from ~/WineApps yourself", id)
	}
	if known {
		if err := os.Remove(paths.Record); err != nil {
			return err
		}
	}
	// RemoveAll does not follow links, but the folders above must not lead
	// elsewhere either.
	prefixes := filepath.Dir(paths.Prefix)
	switch err := RealDir(prefixes); {
	case errors.Is(err, os.ErrNotExist):
		if !known {
			return fmt.Errorf("no app %s; gjallarctl wine list shows the apps", id)
		}
	case err != nil:
		return err
	default:
		if _, err := os.Lstat(paths.Prefix); errors.Is(err, os.ErrNotExist) && !known {
			return fmt.Errorf("no app %s; gjallarctl wine list shows the apps", id)
		}
		if err := os.RemoveAll(paths.Prefix); err != nil {
			return err
		}
	}
	if files {
		paths = PathsFor(home, id, a.Name)
		if err := RealDir(filepath.Dir(paths.Files)); err != nil {
			return err
		}
		if err := os.RemoveAll(paths.Files); err != nil {
			return err
		}
	}
	return nil
}

// RealDir checks that dir is a folder reached without symbolic links:
// bwrap would follow one and give the app whatever it points at.
func RealDir(dir string) error {
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

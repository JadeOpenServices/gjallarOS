package winesandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, name := range []string{"Notepad", "Synthetic App 2", "Grüße", strings.Repeat("a", MaxNameLen)} {
		if !ValidName(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{"", ".", "..", ".hidden", "a/b", `a\b`, "a\x00b", "a\nb", " lead", "trail ",
		strings.Repeat("a", MaxNameLen+1), "\xff"} {
		if ValidName(name) {
			t.Errorf("%q accepted", name)
		}
	}
}

// appHome makes a home with one app record and its folders.
func appHome(t *testing.T, record string) (home string, paths Paths) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths = PathsFor(home, "synthetic", "Synthetic App")
	for _, dir := range []string{filepath.Dir(paths.Record), paths.Prefix, paths.Files} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.Record, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, paths
}

func TestLoadApp(t *testing.T) {
	home, want := appHome(t, `{"name":"Synthetic App","exe":"C:\\Program Files\\Synthetic\\app.exe"}`)
	a, paths, err := LoadApp(home, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if a.Exe != `C:\Program Files\Synthetic\app.exe` || paths != want {
		t.Fatalf("app %+v, paths %+v", a, paths)
	}
	if _, _, err := LoadApp(home, "other"); err == nil || !strings.Contains(err.Error(), "gjallarctl wine list") {
		t.Errorf("missing app: %v", err)
	}
	if _, _, err := LoadApp(home, "../synthetic"); err == nil {
		t.Error("bad id accepted")
	}
}

func TestLoadAppRefusesBadRecords(t *testing.T) {
	for _, record := range []string{
		`{"name":"../Synthetic App","exe":"app.exe"}`,
		`{"name":"Synthetic App","exe":"a\u0000b"}`,
		`{"name":"Synthetic App"`,
	} {
		home, _ := appHome(t, record)
		if _, _, err := LoadApp(home, "synthetic"); err == nil {
			t.Errorf("record %s accepted", record)
		}
	}
}

func TestLoadAppRefusesLinkedFolders(t *testing.T) {
	home, paths := appHome(t, `{"name":"Synthetic App","exe":"app.exe"}`)
	elsewhere := t.TempDir()
	if err := os.Remove(paths.Files); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, paths.Files); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadApp(home, "synthetic"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("linked files folder: %v", err)
	}

	// A link further up counts too.
	home, paths = appHome(t, `{"name":"Synthetic App","exe":"app.exe"}`)
	wineApps := filepath.Dir(paths.Files)
	moved := filepath.Join(home, "moved")
	if err := os.Rename(wineApps, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, wineApps); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadApp(home, "synthetic"); err == nil {
		t.Error("linked WineApps accepted")
	}

	home, paths = appHome(t, `{"name":"Synthetic App","exe":"app.exe"}`)
	if err := os.RemoveAll(paths.Prefix); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Prefix, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadApp(home, "synthetic"); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Errorf("file as prefix: %v", err)
	}
}

func TestApps(t *testing.T) {
	home, paths := appHome(t, `{"name":"Synthetic App","exe":"app.exe"}`)
	dir := filepath.Dir(paths.Record)
	for name, data := range map[string]string{
		"broken.json":   `{"name":"../x","exe":"app.exe"}`,
		"empty.json":    `{"name":"Empty","exe":""}`,
		"Bad Id.json":   `{"name":"Other","exe":"app.exe"}`,
		"notes.txt":     "SYNTHETIC",
		"another.json~": "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(paths.Record, filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	apps, err := Apps(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 || apps[0].ID != "broken" || apps[0].Err == nil ||
		apps[1].ID != "empty" || apps[1].Err == nil || !strings.Contains(apps[1].Err.Error(), "gjallar-wine program empty") ||
		apps[2].ID != "synthetic" || apps[2].Err != nil || apps[2].App.Name != "Synthetic App" {
		t.Fatalf("apps %+v", apps)
	}
	if apps, err := Apps(t.TempDir()); err != nil || apps != nil {
		t.Errorf("no records folder: %v %v", apps, err)
	}
}

func TestLoadAppWithoutProgram(t *testing.T) {
	home, _ := appHome(t, `{"name":"Synthetic App","exe":""}`)
	a, _, err := LoadApp(home, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if a.Ready("synthetic") == nil {
		t.Error("app without a program is ready")
	}
}

func TestWriteRecord(t *testing.T) {
	home := t.TempDir()
	paths := PathsFor(home, "synthetic", "Synthetic App")
	want := App{Name: "Synthetic App", Exe: `C:\Program Files\Synthetic\app.exe`}
	if err := WriteRecord(paths, want); err != nil {
		t.Fatal(err)
	}
	got, err := readRecord(paths.Record)
	if err != nil || got != want {
		t.Fatalf("read back %+v %v", got, err)
	}
	if info, err := os.Stat(paths.Record); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("record mode %v %v", info, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(paths.Record))
	if len(entries) != 1 {
		t.Errorf("left over files: %v", entries)
	}
	if err := WriteRecord(paths, App{Name: "../x"}); err == nil {
		t.Error("bad name written")
	}
}

func TestIDFromName(t *testing.T) {
	for name, want := range map[string]string{
		"Synthetic App":          "synthetic-app",
		"  Tool 2000 (x) ":       "tool-2000-x",
		"7 Zip":                  "app-7-zip",
		"Ünïcode":                "n-code",
		"!!!":                    "app",
		strings.Repeat("ab", 40): strings.Repeat("ab", 24),
	} {
		got := IDFromName(name)
		if got != want || !ValidID(got) {
			t.Errorf("IDFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

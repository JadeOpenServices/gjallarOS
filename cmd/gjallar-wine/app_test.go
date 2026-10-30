package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, name := range []string{"Notepad", "Synthetic App 2", "Grüße", strings.Repeat("a", maxNameLen)} {
		if !validName(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{"", ".", "..", ".hidden", "a/b", `a\b`, "a\x00b", "a\nb", " lead", "trail ",
		strings.Repeat("a", maxNameLen+1), "\xff"} {
		if validName(name) {
			t.Errorf("%q accepted", name)
		}
	}
}

// appHome makes a home with one app record and its folders.
func appHome(t *testing.T, record string) (home string, paths appPaths) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths = pathsFor(home, "synthetic", "Synthetic App")
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
	a, paths, err := loadApp(home, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if a.Exe != `C:\Program Files\Synthetic\app.exe` || paths != want {
		t.Fatalf("app %+v, paths %+v", a, paths)
	}
	if _, _, err := loadApp(home, "other"); err == nil || !strings.Contains(err.Error(), "gjallarctl wine list") {
		t.Errorf("missing app: %v", err)
	}
	if _, _, err := loadApp(home, "../synthetic"); err == nil {
		t.Error("bad id accepted")
	}
}

func TestLoadAppRefusesBadRecords(t *testing.T) {
	for _, record := range []string{
		`{"name":"../Synthetic App","exe":"app.exe"}`,
		`{"name":"Synthetic App","exe":""}`,
		`{"name":"Synthetic App"`,
	} {
		home, _ := appHome(t, record)
		if _, _, err := loadApp(home, "synthetic"); err == nil {
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
	if _, _, err := loadApp(home, "synthetic"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
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
	if _, _, err := loadApp(home, "synthetic"); err == nil {
		t.Error("linked WineApps accepted")
	}

	home, paths = appHome(t, `{"name":"Synthetic App","exe":"app.exe"}`)
	if err := os.RemoveAll(paths.Prefix); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Prefix, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadApp(home, "synthetic"); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Errorf("file as prefix: %v", err)
	}
}

func TestMakeRunDir(t *testing.T) {
	runtimeDir := t.TempDir()
	dir, err := makeRunDir(runtimeDir, "synthetic-1", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("run dir %v %v", info, err)
	}
	if _, err := makeRunDir(runtimeDir, "synthetic-1", os.Getuid()); err == nil {
		t.Error("existing run dir reused")
	}
	if _, err := makeRunDir(runtimeDir, "synthetic-2", os.Getuid()+1); err == nil {
		t.Error("folder of another uid accepted")
	}

	open := t.TempDir()
	if err := os.Mkdir(filepath.Join(open, "gjallar-wine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := makeRunDir(open, "synthetic-1", os.Getuid()); err == nil {
		t.Error("open parent accepted")
	}

	linked := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linked, "gjallar-wine")); err != nil {
		t.Fatal(err)
	}
	if _, err := makeRunDir(linked, "synthetic-1", os.Getuid()); err == nil {
		t.Error("linked parent accepted")
	}
}

func TestParseLock(t *testing.T) {
	sym, refer, argv, err := parseLock([]string{"--sym", "/p", "--sym", "/tmp", "--refer", "/", "--", "/bin/wine", "app.exe", "--sym"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sym, []string{"/p", "/tmp"}) || !slices.Equal(refer, []string{"/"}) ||
		!slices.Equal(argv, []string{"/bin/wine", "app.exe", "--sym"}) {
		t.Fatalf("sym %v refer %v argv %v", sym, refer, argv)
	}
	for _, args := range [][]string{
		nil,
		{"--sym"},
		{"--sym", "rel", "--", "/bin/wine"},
		{"--refer", "/", "--"},
		{"--", "wine"},
		{"--other", "/x", "--", "/bin/wine"},
		{"--sym", "/p"},
	} {
		if _, _, _, err := parseLock(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
}

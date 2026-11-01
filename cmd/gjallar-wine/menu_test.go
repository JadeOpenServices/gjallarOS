package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

func menuOf(t *testing.T, f *fakeSetup, id string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".local/share/applications", "gjallar-wine-"+id+".desktop"))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

func TestMenuFollowsApps(t *testing.T) {
	f := newFakeSetup(t)
	installer := filepath.Join(t.TempDir(), "setup.exe")
	writeFile(t, installer, []byte("SYNTHETIC installer"))
	f.installs(t, "app")
	if err := addApp([]string{"Synthetic App", installer}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	entry, ok := menuOf(t, f, "synthetic-app")
	if !ok || !strings.Contains(entry, "\nName=Synthetic App\n") || !strings.Contains(entry, "\nExec=gjallar-wine run synthetic-app\n") {
		t.Fatalf("menu entry %q", entry)
	}
	// An app without a program has no entry.
	f.launch = nil
	if err := addApp([]string{"Empty"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := menuOf(t, f, "empty"); ok {
		t.Error("entry for an app without a program")
	}

	if err := removeCmd([]string{"synthetic-app"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := menuOf(t, f, "synthetic-app"); ok {
		t.Error("entry left after remove")
	}
}

func TestMenuCmd(t *testing.T) {
	f := newFakeSetup(t)
	if err := addApp([]string{"Old"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	// An app set up before the entries came.
	paths := winesandbox.PathsFor(f.home, "old", "Old")
	if err := winesandbox.WriteRecord(paths, winesandbox.App{Name: "Old", Exe: `C:\old.exe`}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.home, ".local/share/applications")
	stale := filepath.Join(dir, "gjallar-wine-gone.desktop")
	foreign := filepath.Join(dir, "gjallar-wine-mine.desktop")
	writeFile(t, stale, []byte(menuEntry("gone", "Gone")))
	writeFile(t, foreign, []byte("[Desktop Entry]\nName=SYNTHETIC own entry\n"))
	if err := os.Symlink(stale, filepath.Join(dir, "gjallar-wine-link.desktop")); err != nil {
		t.Fatal(err)
	}

	if err := menuCmd(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if entry, ok := menuOf(t, f, "old"); !ok || !strings.Contains(entry, "Exec=gjallar-wine run old\n") {
		t.Errorf("old app's entry %q", entry)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Error("entry of a removed app left")
	}
	for _, keep := range []string{foreign, filepath.Join(dir, "gjallar-wine-link.desktop")} {
		if _, err := os.Lstat(keep); err != nil {
			t.Errorf("%s removed: %v", keep, err)
		}
	}
	if err := menuCmd([]string{"x"}, io.Discard); err == nil {
		t.Error("menu took an argument")
	}
}

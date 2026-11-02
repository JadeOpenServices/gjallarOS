package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// doctorSetup adds the SYNTHETIC app "Synthetic App" with a program on C:
// that it starts, and a file in its files folder.
func doctorSetup(t *testing.T) (*fakeSetup, winesandbox.Paths) {
	t.Helper()
	f := newFakeSetup(t)
	if err := addApp([]string{"Synthetic App"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	paths := winesandbox.PathsFor(f.home, "synthetic-app", "Synthetic App")
	writeFile(t, filepath.Join(paths.Prefix, "drive_c/Program Files/Synthetic/tool.exe"), []byte("SYNTHETIC"))
	writeFile(t, filepath.Join(paths.Files, "work.txt"), []byte("SYNTHETIC"))
	a := winesandbox.App{Name: "Synthetic App", Exe: `C:\Program Files\Synthetic\tool.exe`}
	if err := saveApp(paths, "synthetic-app", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	return f, paths
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func snapshots(t *testing.T, f *fakeSetup) []winesandbox.Snapshot {
	t.Helper()
	list, err := winesandbox.Snapshots(f.home, "synthetic-app")
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestResetAndUndo(t *testing.T) {
	f, paths := doctorSetup(t)
	tool := filepath.Join(paths.Prefix, "drive_c/Program Files/Synthetic/tool.exe")
	var out strings.Builder
	if err := resetCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if exists(tool) {
		t.Error("program on C: kept after reset")
	}
	if !exists(filepath.Join(paths.Files, "work.txt")) {
		t.Error("files folder changed by reset")
	}
	if !exists(filepath.Join(paths.Prefix, winesandbox.InitMarker)) {
		t.Error("no new prefix after reset")
	}
	if f.runs(t) != 1 {
		t.Errorf("Wine's setup ran %d times", f.runs(t))
	}
	a, _, err := winesandbox.LoadApp(f.home, "synthetic-app")
	if err != nil || a.Exe != "" {
		t.Errorf("program kept after reset: %+v %v", a, err)
	}
	if !strings.Contains(out.String(), "gjallarctl wine undo synthetic-app") {
		t.Errorf("no undo hint: %q", out.String())
	}
	if len(snapshots(t, f)) != 1 {
		t.Fatalf("snapshots %v", snapshots(t, f))
	}

	if err := undoCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !exists(tool) {
		t.Error("program not back after undo")
	}
	a, _, err = winesandbox.LoadApp(f.home, "synthetic-app")
	if err != nil || a.Exe != `C:\Program Files\Synthetic\tool.exe` {
		t.Errorf("record not back after undo: %+v %v", a, err)
	}
	if len(snapshots(t, f)) != 0 {
		t.Error("snapshot kept after undo")
	}
	if err := undoCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "nothing to undo") {
		t.Errorf("second undo: %v", err)
	}
}

func TestResetKeepsProgramOnD(t *testing.T) {
	f, paths := doctorSetup(t)
	writeFile(t, filepath.Join(paths.Files, "Tool/tool.exe"), []byte("SYNTHETIC"))
	a := winesandbox.App{Name: "Synthetic App", Exe: `D:\Tool\tool.exe`}
	if err := saveApp(paths, "synthetic-app", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := resetCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, _, err := winesandbox.LoadApp(f.home, "synthetic-app"); err != nil || got.Exe != a.Exe {
		t.Errorf("program on D: dropped: %+v %v", got, err)
	}
}

func TestRepair(t *testing.T) {
	f, paths := doctorSetup(t)
	out := filepath.Join(paths.Prefix, "drive_c/outward")
	if err := os.Symlink("/", out); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(paths.Prefix, "dosdevices/z:")
	if err := os.Symlink("/", extra); err != nil {
		t.Fatal(err)
	}
	if err := repairCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if exists(out) || exists(extra) {
		t.Error("links that lead out kept after repair")
	}
	if !exists(filepath.Join(paths.Prefix, "drive_c/Program Files/Synthetic/tool.exe")) {
		t.Error("installed program gone after repair")
	}
	if last := f.launched[len(f.launched)-1]; !slices.Equal(last, []string{"wineboot", "-u"}) {
		t.Errorf("ran %q", last)
	}
	list := snapshots(t, f)
	if len(list) != 1 || !exists(filepath.Join(list[0].Dir, "prefix/drive_c/outward")) {
		t.Fatalf("snapshot before repair missing: %v", list)
	}

	f.launch = func(string, []string) int { return 3 }
	err := repairCmd([]string{"synthetic-app"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "gjallarctl wine undo synthetic-app") {
		t.Errorf("failed update: %v", err)
	}
	f.launch = nil
	for range 3 {
		if err := repairCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(snapshots(t, f)); n != winesandbox.KeepSnapshots {
		t.Errorf("%d snapshots kept", n)
	}
}

func TestRepairSetsCLR32(t *testing.T) {
	f, paths := doctorSetup(t)
	// SYNTHETIC: what winetricks' .NET step left before PR #2533.
	writeFile(t, filepath.Join(paths.Prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n\n"+
		"[Software\\\\Microsoft\\\\.NETFramework] 1793326395\n\"OnlyUseLatestCLR\"=dword:00000001\n\n"+
		"[Software\\\\Wow6432Node\\\\.NETFramework] 1793326159\n\"OnlyUseLatestCLR\"=dword:00000001\n\n"))
	f.launched = nil
	if err := repairCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"reg", "add", winesandbox.CLRKey32, "/v", "OnlyUseLatestCLR", "/t", "REG_DWORD", "/d", "1", "/f"},
		{"wineboot", "-u"},
	}
	if !slices.EqualFunc(f.launched, want, slices.Equal) {
		t.Errorf("ran %q", f.launched)
	}

	f.launched = nil
	f.launch = func(_ string, program []string) int {
		if program[0] == "reg" {
			return 1
		}
		return 0
	}
	err := repairCmd([]string{"synthetic-app"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "gjallarctl wine undo synthetic-app") || len(f.launched) != 1 {
		t.Errorf("failed .NET setting: %v, ran %q", err, f.launched)
	}
}

func TestUndoRefusesOtherApp(t *testing.T) {
	f, paths := doctorSetup(t)
	if err := repairCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	list := snapshots(t, f)
	writeFile(t, filepath.Join(list[0].Dir, "app.json"), []byte(`{"name":"Other"}`))
	if err := undoCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err == nil {
		t.Error("snapshot of another app put back")
	}
	if !exists(filepath.Join(paths.Prefix, winesandbox.InitMarker)) {
		t.Error("prefix gone after a refused undo")
	}
}

func TestRemoveDropsSnapshots(t *testing.T) {
	f, _ := doctorSetup(t)
	if err := resetCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := removeCmd([]string{"synthetic-app"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if exists(winesandbox.SnapshotsDir(f.home, "synthetic-app")) {
		t.Error("snapshots kept after remove")
	}
	if err := addApp([]string{"Synthetic App"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := undoCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err == nil {
		t.Error("new app of the same name undid into the old one")
	}
	for _, bad := range [][]string{{}, {"synthetic-app", "x"}, {"Synthetic App"}} {
		for _, cmd := range []func([]string, io.Writer, io.Writer) error{resetCmd, repairCmd, undoCmd} {
			if err := cmd(bad, io.Discard, io.Discard); err == nil {
				t.Errorf("%q accepted", bad)
			}
		}
	}
}

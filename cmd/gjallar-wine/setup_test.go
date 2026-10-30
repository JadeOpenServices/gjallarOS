package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// fakeSetup is the SYNTHETIC world the setup commands run in: a home, a
// Wine setup step that lays out a tiny base prefix and counts its runs,
// and stand-ins for the settings service and the sandbox.
type fakeSetup struct {
	home, count string
	places      string
	switches    []string
	setErr      error
	launched    [][]string
	launch      func(id string, program []string) int
}

func newFakeSetup(t *testing.T) *fakeSetup {
	t.Helper()
	cp, err := exec.LookPath("cp")
	if err != nil {
		t.Skip("no cp to copy prefixes with")
	}
	f := &fakeSetup{home: t.TempDir()}
	tools := t.TempDir()
	f.count = filepath.Join(tools, "runs")
	f.places = filepath.Join(tools, "places")
	script := func(name, body string) string {
		path := filepath.Join(tools, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	init := script("init", `set -e
echo run >> "`+f.count+`"
mkdir -p "$WINEPREFIX/dosdevices" "$WINEPREFIX/drive_c/users/tester/Music"
ln -s ../drive_c "$WINEPREFIX/dosdevices/c:"
ln -s / "$WINEPREFIX/dosdevices/z:"
ln -s "$HOME" "$WINEPREFIX/drive_c/users/tester/Desktop"
ln -s "$HOME" "$WINEPREFIX/drive_c/users/tester/Documents"
touch "$WINEPREFIX/`+winesandbox.InitMarker+`"
`)
	script("wineserver", "exit 0\n")
	place := script("place", `printf '%s|' "$@" >> "`+f.places+`"
echo >> "`+f.places+`"
`)

	oldInit, oldCp, oldWine, oldPlace := initPath, cpPath, winePath, placePath
	oldUser, oldLaunch, oldSet := currentUser, launchApp, setWine
	t.Cleanup(func() {
		initPath, cpPath, winePath, placePath = oldInit, oldCp, oldWine, oldPlace
		currentUser, launchApp, setWine = oldUser, oldLaunch, oldSet
	})
	initPath, cpPath, winePath, placePath = init, cp, filepath.Join(tools, "wine"), place
	currentUser = func() (string, string, error) { return "tester", f.home, nil }
	setWine = func(id string, internet, home bool) error {
		if internet || home {
			t.Errorf("switch of %s turned on", id)
		}
		f.switches = append(f.switches, id)
		return f.setErr
	}
	launchApp = func(id string, program, args []string, stderr io.Writer) (int, error) {
		f.launched = append(f.launched, program)
		if f.launch == nil {
			return 0, nil
		}
		return f.launch(id, program), nil
	}
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")
	return f
}

// placeCalls gives the Places tool's runs, one line each.
func (f *fakeSetup) placeCalls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.places)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func (f *fakeSetup) runs(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.count)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run")
}

// lnk builds a SYNTHETIC shortcut with an ANSI local path.
func lnk(target string) []byte {
	le := binary.LittleEndian
	data := make([]byte, 0x4c)
	le.PutUint32(data, 0x4c)
	le.PutUint32(data[0x14:], 0x2)
	info := make([]byte, 0x1c)
	le.PutUint32(info[4:], 0x1c)
	le.PutUint32(info[8:], 1)
	le.PutUint32(info[0x10:], 0x1c)
	le.PutUint32(info[0x18:], uint32(0x1c+len(target)+1))
	info = append(append(info, target...), 0, 0)
	le.PutUint32(info, uint32(len(info)))
	return append(data, info...)
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// installs makes the fake sandbox act as an installer that puts the
// programs on C: and a start menu shortcut to each.
func (f *fakeSetup) installs(t *testing.T, programs ...string) {
	f.launch = func(id string, program []string) int {
		driveC := filepath.Join(f.home, ".local/share/gjallar-wine/prefixes", id, "drive_c")
		for _, p := range programs {
			writeFile(t, filepath.Join(driveC, "Program Files", "Synthetic", p+".exe"), []byte("SYNTHETIC"))
			writeFile(t, filepath.Join(driveC, "ProgramData/Microsoft/Windows/Start Menu/Programs", p+".lnk"),
				lnk(`C:\Program Files\Synthetic\`+p+`.exe`))
		}
		return 0
	}
}

func TestAddApp(t *testing.T) {
	f := newFakeSetup(t)
	if err := addApp([]string{"Synthetic App"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, paths, err := winesandbox.LoadApp(f.home, "synthetic-app")
	if err != nil || a.Name != "Synthetic App" || a.Exe != "" {
		t.Fatalf("record %+v %v", a, err)
	}
	if paths.Files != filepath.Join(f.home, "WineApps", "Synthetic App") {
		t.Errorf("files folder %s", paths.Files)
	}
	if got, err := os.Readlink(filepath.Join(paths.Prefix, "dosdevices", "d:")); err != nil || got != paths.Files {
		t.Errorf("D: -> %q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(paths.Prefix, "dosdevices", "z:")); err == nil {
		t.Error("the app's prefix kept Z:")
	}
	if info, err := os.Lstat(filepath.Join(paths.Prefix, "drive_c/users/tester/Desktop")); err != nil || !info.IsDir() {
		t.Errorf("Desktop is not a folder: %v %v", info, err)
	}
	if got, err := os.Readlink(filepath.Join(winesandbox.BasePrefix(f.home), "dosdevices", "z:")); err != nil || got != "/" {
		t.Errorf("base prefix changed: %q %v", got, err)
	}
	if !slices.Equal(f.switches, []string{"synthetic-app"}) {
		t.Errorf("switches cleared for %v", f.switches)
	}
	if len(f.launched) != 0 {
		t.Errorf("started %v without an installer", f.launched)
	}
	if got, want := f.placeCalls(t), []string{"add|" + paths.Files + "|Synthetic App|folder-wine|"}; !slices.Equal(got, want) {
		t.Errorf("Places %q, want %q", got, want)
	}

	if err := addApp([]string{"Synthetic App"}, io.Discard, io.Discard); err == nil {
		t.Error("same app added twice")
	}
	// The base is set up once for every app.
	if err := addApp([]string{"Second"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := f.runs(t); n != 1 {
		t.Errorf("Wine's setup ran %d times", n)
	}
	for _, bad := range [][]string{{".hidden"}, {"a/b"}, {}, {"a", "b", "c"}} {
		if err := addApp(bad, io.Discard, io.Discard); err == nil {
			t.Errorf("add %q accepted", bad)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(paths.Prefix))
	if err != nil || len(entries) != 2 {
		t.Errorf("prefixes %v %v", entries, err)
	}
}

func TestAddAppSetupFails(t *testing.T) {
	f := newFakeSetup(t)
	if err := os.WriteFile(initPath, []byte("#!/bin/sh\necho run >> \""+f.count+"\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := addApp([]string{"Synthetic"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "setup.log") {
		t.Fatalf("failed setup: %v", err)
	}
	if _, err := os.Lstat(winesandbox.PathsFor(f.home, "synthetic", "Synthetic").Record); err == nil {
		t.Error("record written after a failed setup")
	}
	// The next add tries again.
	addApp([]string{"Synthetic"}, io.Discard, io.Discard)
	if n := f.runs(t); n != 2 {
		t.Errorf("setup ran %d times", n)
	}
}

func TestAddAppSettingsMissing(t *testing.T) {
	f := newFakeSetup(t)
	f.setErr = errSettingsMissing
	if err := addApp([]string{"Synthetic"}, io.Discard, io.Discard); err != nil {
		t.Errorf("without the settings service: %v", err)
	}
	f.setErr = errors.New("SYNTHETIC bus failure")
	if err := addApp([]string{"Other"}, io.Discard, io.Discard); err == nil {
		t.Error("add went on with switches that may be on")
	}
}

func TestInstallPicksProgram(t *testing.T) {
	f := newFakeSetup(t)
	installer := filepath.Join(t.TempDir(), "Setup.EXE")
	writeFile(t, installer, []byte("SYNTHETIC installer"))
	f.installs(t, "app")
	var copied []byte
	inner := f.launch
	f.launch = func(id string, program []string) int {
		copied, _ = os.ReadFile(filepath.Join(f.home, `.local/share/gjallar-wine/prefixes/synthetic/drive_c/gjallar-setup/Setup.EXE`))
		inner(id, program)
		return 3
	}
	if err := addApp([]string{"Synthetic", installer}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{`C:\gjallar-setup\Setup.EXE`}}; !slices.EqualFunc(f.launched, want, slices.Equal) {
		t.Errorf("started %q", f.launched)
	}
	if string(copied) != "SYNTHETIC installer" {
		t.Errorf("the sandbox saw %q", copied)
	}
	a, paths, err := winesandbox.LoadApp(f.home, "synthetic")
	if err != nil || a.Exe != `C:\Program Files\Synthetic\app.exe` {
		t.Errorf("program %q %v", a.Exe, err)
	}
	if _, err := os.Lstat(filepath.Join(paths.Prefix, "drive_c", "gjallar-setup")); err == nil {
		t.Error("installer copy left in the app")
	}

	// A second installer with two programs leaves the choice to the user.
	f.installs(t, "tool", "viewer")
	f.launched = nil
	msi := filepath.Join(paths.Files, "extra.msi")
	writeFile(t, msi, []byte("SYNTHETIC"))
	if err := installCmd([]string{"synthetic", msi}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"msiexec", "/i", `D:\extra.msi`}}; !slices.EqualFunc(f.launched, want, slices.Equal) {
		t.Errorf("started %q", f.launched)
	}
	if a, _, _ := winesandbox.LoadApp(f.home, "synthetic"); a.Exe != `C:\Program Files\Synthetic\app.exe` {
		t.Errorf("chosen program replaced by %q", a.Exe)
	}

	for _, bad := range []string{filepath.Join(paths.Files, "missing.exe"), filepath.Join(paths.Files, "notes.txt"), paths.Files} {
		writeFile(t, filepath.Join(paths.Files, "notes.txt"), nil)
		if err := installCmd([]string{"synthetic", bad}, io.Discard, io.Discard); err == nil {
			t.Errorf("installer %s accepted", bad)
		}
	}
	if err := installCmd([]string{"missing", msi}, io.Discard, io.Discard); err == nil {
		t.Error("install into a missing app accepted")
	}
}

func TestChooseProgram(t *testing.T) {
	f := newFakeSetup(t)
	if err := addApp([]string{"Synthetic"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.installs(t, "tool", "viewer")
	f.launch("synthetic", nil)
	_, paths, _ := winesandbox.LoadApp(f.home, "synthetic")
	writeFile(t, filepath.Join(paths.Files, "Portable", "run.exe"), []byte("SYNTHETIC"))
	writeFile(t, filepath.Join(paths.Files, "readme.txt"), []byte("SYNTHETIC"))
	outside := filepath.Join(t.TempDir(), "outside.exe")
	writeFile(t, outside, []byte("SYNTHETIC"))
	if err := os.Symlink(outside, filepath.Join(paths.Files, "out.exe")); err != nil {
		t.Fatal(err)
	}
	exe := func() string {
		a, _, err := winesandbox.LoadApp(f.home, "synthetic")
		if err != nil {
			t.Fatal(err)
		}
		return a.Exe
	}

	var out strings.Builder
	if err := chooseProgram("synthetic", "", strings.NewReader(""), false, &out); err != nil || !strings.Contains(out.String(), `2  C:\Program Files\Synthetic\viewer.exe`) {
		t.Errorf("list %q %v", out.String(), err)
	}
	if err := chooseProgram("synthetic", "", strings.NewReader("2\n"), true, io.Discard); err != nil || exe() != `C:\Program Files\Synthetic\viewer.exe` {
		t.Errorf("asked: %q %v", exe(), err)
	}
	out.Reset()
	if err := chooseProgram("synthetic", "", strings.NewReader("\n"), true, &out); err != nil || exe() != `C:\Program Files\Synthetic\viewer.exe` || !strings.Contains(out.String(), "* 2") {
		t.Errorf("kept: %q %q %v", exe(), out.String(), err)
	}
	if err := chooseProgram("synthetic", "1", nil, false, io.Discard); err != nil || exe() != `C:\Program Files\Synthetic\tool.exe` {
		t.Errorf("number: %q %v", exe(), err)
	}
	if err := chooseProgram("synthetic", `d:\Portable\run.exe`, nil, false, io.Discard); err != nil || exe() != `d:\Portable\run.exe` {
		t.Errorf("D: path: %q %v", exe(), err)
	}
	if err := chooseProgram("synthetic", filepath.Join(paths.Files, "Portable", "run.exe"), nil, false, io.Discard); err != nil || exe() != `D:\Portable\run.exe` {
		t.Errorf("host path: %q %v", exe(), err)
	}
	for _, bad := range []string{"0", "3", `D:\readme.txt`, `D:\out.exe`, outside, `Z:\nix\x.exe`, `D:\missing.exe`, `D:\Portable`, "run.exe"} {
		if err := chooseProgram("synthetic", bad, nil, false, io.Discard); err == nil {
			t.Errorf("choice %q accepted", bad)
		}
	}
	if exe() != `D:\Portable\run.exe` {
		t.Errorf("a refused choice changed the program to %q", exe())
	}
}

func TestRemoveCmd(t *testing.T) {
	f := newFakeSetup(t)
	for _, name := range []string{"Keep", "Gone"} {
		if err := addApp([]string{name}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	keep := winesandbox.PathsFor(f.home, "keep", "Keep")
	gone := winesandbox.PathsFor(f.home, "gone", "Gone")
	writeFile(t, filepath.Join(keep.Files, "work.txt"), []byte("SYNTHETIC"))

	f.switches, f.setErr = nil, errors.New("SYNTHETIC bus failure")
	if err := removeCmd([]string{"keep"}, io.Discard); err == nil {
		t.Error("removed while the switches may stay on")
	}
	if _, err := os.Lstat(keep.Record); err != nil {
		t.Errorf("record gone after a refused remove: %v", err)
	}

	f.setErr = nil
	var out strings.Builder
	if err := removeCmd([]string{"keep"}, &out); err != nil || !strings.Contains(out.String(), keep.Files) {
		t.Fatalf("remove: %q %v", out.String(), err)
	}
	if _, err := os.ReadFile(filepath.Join(keep.Files, "work.txt")); err != nil {
		t.Errorf("files folder not kept: %v", err)
	}
	for _, path := range []string{keep.Record, keep.Prefix} {
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("%s left", path)
		}
	}
	if err := removeCmd([]string{"gone", "--delete-files"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(gone.Files); err == nil {
		t.Error("files folder left with --delete-files")
	}
	if !slices.Equal(f.switches, []string{"keep", "keep", "gone"}) {
		t.Errorf("switches cleared for %v", f.switches)
	}
	if got := f.placeCalls(t)[2:]; !slices.Equal(got, []string{"remove|" + keep.Files + "|", "remove|" + gone.Files + "|"}) {
		t.Errorf("Places %q", got)
	}
	for _, bad := range [][]string{{}, {"keep", "--files"}, {"keep", "--delete-files", "x"}, {"Keep"}} {
		if err := removeCmd(bad, io.Discard); err == nil {
			t.Errorf("remove %q accepted", bad)
		}
	}
}

func TestLockApp(t *testing.T) {
	newFakeSetup(t)
	first, err := lockApp("synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockApp("synthetic"); err == nil || !strings.Contains(err.Error(), "close it first") {
		t.Errorf("second lock: %v", err)
	}
	if err := addApp([]string{"Synthetic"}, io.Discard, io.Discard); err == nil {
		t.Error("add went on while the app was open")
	}
	other, err := lockApp("other")
	if err != nil {
		t.Errorf("other app: %v", err)
	} else {
		other.Close()
	}
	first.Close()
	again, err := lockApp("synthetic")
	if err != nil {
		t.Errorf("after close: %v", err)
	} else {
		again.Close()
	}
	t.Setenv("XDG_RUNTIME_DIR", "relative")
	if _, err := lockApp("synthetic"); err == nil {
		t.Error("relative runtime dir accepted")
	}
}

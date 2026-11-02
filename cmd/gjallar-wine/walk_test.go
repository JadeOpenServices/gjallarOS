package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
	"github.com/JadeOpenServices/gjallarOS/internal/winpe"
)

// walkSetup gives the SYNTHETIC app of doctorSetup a system folder, a Wine
// library folder and a fix table, and makes the scan answer from reports
// by file name. Every file here is SYNTHETIC.
func walkSetup(t *testing.T, reports map[string]winpe.Report) (winesandbox.Paths, string) {
	t.Helper()
	_, paths := doctorSetup(t)
	bin := filepath.Join(t.TempDir(), "bin")
	winePath = filepath.Join(bin, "wine")
	lib := filepath.Join(filepath.Dir(bin), "lib", "wine", "x86_64-windows")
	writeFile(t, filepath.Join(lib, "msvcp100.dll"), []byte("SYNTHETIC"))
	writeFile(t, filepath.Join(lib, "user32.dll"), []byte("SYNTHETIC"))
	sys := filepath.Join(paths.Prefix, "drive_c", "windows", "system32")
	stub := func(mark string) []byte { return append(make([]byte, winpe.WineMarkAt), mark...) }
	writeFile(t, filepath.Join(sys, "KERNEL32.dll"), stub("Wine builtin DLL"))
	writeFile(t, filepath.Join(sys, "msvcp140.dll"), stub("This program can"))
	writeFile(t, filepath.Join(sys, "short.dll"), []byte("SYNTHETIC"))
	if err := os.Mkdir(filepath.Join(sys, "odd.dll"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.dll")
	writeFile(t, outside, stub("Wine builtin DLL"))
	if err := os.Symlink(outside, filepath.Join(sys, "away.dll")); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(paths.Prefix, "drive_c", "Program Files", "Synthetic")
	for name := range reports {
		if name != "tool.exe" {
			writeFile(t, filepath.Join(appDir, name), []byte("SYNTHETIC"))
		}
	}
	tab := &winefix.Table{Fixes: []winefix.Fix{
		{ID: "vcrun2010", Title: "SYNTHETIC runtime", Kind: winefix.KindOffer, Why: "SYNTHETIC",
			DLLs: []string{"msvcp100.dll", "msvcr100.dll"}},
	}}
	oldLoad, oldScan := loadFixes, scanFile
	t.Cleanup(func() { loadFixes, scanFile = oldLoad, oldScan })
	loadFixes = func() (*winefix.Table, error) { return tab, nil }
	scanFile = func(f *os.File) (winpe.Report, error) {
		r, ok := reports[filepath.Base(f.Name())]
		if !ok {
			return winpe.Report{}, fmt.Errorf("SYNTHETIC scan failure")
		}
		return r, nil
	}
	return paths, appDir
}

func x64(imports, delay []string) winpe.Report {
	return winpe.Report{Kind: "pe", Machine: "x64", Subsystem: "gui", Imports: imports, DelayImports: delay}
}

func TestDoctorWalk(t *testing.T) {
	walkSetup(t, map[string]winpe.Report{
		"tool.exe": x64([]string{"kernel32.dll", "mine.dll", "msvcp100.dll", "api-ms-win-crt-runtime-l1-1-0.dll",
			"msvcp140.dll", "gone.dll", "old32.dll", "user32", "short.dll", "odd.dll", "away.dll"}, []string{"later.dll"}),
		"Mine.DLL":  x64([]string{"msvcr100.dll", "kernel32.dll"}, []string{"deep.dll"}),
		"old32.dll": {Kind: "pe", Machine: "x86", Subsystem: "gui"},
		"later.dll": x64(nil, nil),
		"bad.dll":   {},
	})
	var out strings.Builder
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	start, later, ok := strings.Cut(got, "May be loaded later")
	if !ok {
		t.Fatalf("no part for later libraries:\n%s", got)
	}
	for _, want := range []string{
		"Checked tool.exe of app synthetic-app in a sandbox: 64-bit program with a window",
		"1 library, from the program's folder: Mine.DLL",
		"3 libraries, Wine's own: kernel32.dll, msvcp100.dll, user32.dll",
		`2 libraries, from the publisher, not Wine: C:\windows\system32\msvcp140.dll, C:\windows\system32\short.dll`,
		"MISSING odd.dll, named by tool.exe",
		"MISSING away.dll, named by tool.exe",
		"1 library, system sets",
		"MISSING msvcr100.dll, named by Mine.DLL: gjallarctl wine fix synthetic-app vcrun2010 brings it",
		"MISSING gone.dll, named by tool.exe: no fix brings it",
		"MISSING old32.dll, named by tool.exe",
	} {
		if !strings.Contains(start, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, want := range []string{
		"1 library, from the program's folder: later.dll",
		"MISSING deep.dll, named by Mine.DLL",
		"built for another processor, so Wine looks further: old32.dll",
		"Could not check odd.dll in ",
		"Could not check away.dll in ",
		"Wine brings its own msvcp100.dll; if the program fails over it, gjallarctl wine fix synthetic-app vcrun2010 puts in the publisher's.",
	} {
		if !strings.Contains(later, want) {
			t.Errorf("missing %q after the start part in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Nothing it names is missing") {
		t.Errorf("says nothing is missing:\n%s", got)
	}
}

func TestDoctorWalkRecordedFix(t *testing.T) {
	paths, _ := walkSetup(t, map[string]winpe.Report{
		"tool.exe": x64([]string{"msvcp100.dll", "msvcr100.dll"}, nil),
	})
	a := winesandbox.App{Name: "Synthetic App", Exe: `C:\Program Files\Synthetic\tool.exe`, Fixes: []string{"vcrun2010"}}
	if err := saveApp(paths, "synthetic-app", a, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "MISSING msvcr100.dll, named by tool.exe: the app has vcrun2010, which brings it; gjallarctl wine repair synthetic-app") {
		t.Errorf("no repair hint:\n%s", got)
	}
	if strings.Contains(got, "Wine brings its own") {
		t.Errorf("offers a fix the app has:\n%s", got)
	}
}

func TestDoctorWalkClean(t *testing.T) {
	walkSetup(t, map[string]winpe.Report{"tool.exe": x64([]string{"user32.dll"}, nil)})
	var out strings.Builder
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "Nothing it names is missing.") || strings.Contains(got, "May be loaded later") {
		t.Errorf("clean program:\n%s", got)
	}
}

func TestDoctorWalkStops(t *testing.T) {
	reports := map[string]winpe.Report{"tool.exe": x64([]string{"lib0.dll"}, nil)}
	for i := range maxWalkScans + 3 {
		reports[fmt.Sprintf("lib%d.dll", i)] = x64([]string{fmt.Sprintf("lib%d.dll", i+1)}, nil)
	}
	walkSetup(t, reports)
	var out strings.Builder
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, fmt.Sprintf("Stopped after %d of the program's own libraries; not followed further: lib%d.dll", maxWalkScans, maxWalkScans)) {
		t.Errorf("walk did not stop at the cap:\n%s", got)
	}
}

func TestDoctorWalkRefuses(t *testing.T) {
	paths, _ := walkSetup(t, map[string]winpe.Report{"tool.exe": {Kind: "pe", Machine: "arm64", Subsystem: "gui"}})
	if err := doctorCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "64-bit ARM") {
		t.Errorf("arm64 program: %v", err)
	}
	if err := saveApp(paths, "synthetic-app", winesandbox.App{Name: "Synthetic App"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := doctorCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "has no program yet") {
		t.Errorf("app without a program: %v", err)
	}
	if err := doctorCmd(nil, io.Discard, io.Discard); err != errUsage {
		t.Errorf("no id: %v", err)
	}
}

func TestDoctorWalkDotNet(t *testing.T) {
	r := x64([]string{"mscoree.dll"}, nil)
	r.DotNet = &winpe.DotNet{}
	walkSetup(t, map[string]winpe.Report{"tool.exe": r})
	var out strings.Builder
	if err := doctorCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "a .NET program (from its .NET header)") {
		t.Errorf("no .NET note:\n%s", got)
	}
}

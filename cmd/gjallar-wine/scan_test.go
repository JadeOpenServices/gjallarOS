package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winpe"
)

// fakeScan stands in for the sandboxed scan in the setup tests, whose
// installers are SYNTHETIC text: an .msi file is a package, any other a
// 64-bit program with a window.
func fakeScan(f *os.File) (winpe.Report, error) {
	if strings.EqualFold(filepath.Ext(f.Name()), ".msi") {
		return winpe.Report{Kind: "msi"}, nil
	}
	return winpe.Report{Kind: "pe", Machine: "x64", Subsystem: "gui", DPI: "permonitorv2"}, nil
}

func TestTellStops(t *testing.T) {
	for name, c := range map[string]struct {
		ext  string
		r    winpe.Report
		want string
	}{
		"other":    {".exe", winpe.Report{Kind: "other"}, "neither a program"},
		"dos":      {".exe", winpe.Report{Kind: "dos"}, "Wine does not run DOS programs"},
		"msi.exe":  {".exe", winpe.Report{Kind: "msi"}, "rename it to end in .msi"},
		"exe.msi":  {".msi", winpe.Report{Kind: "pe", Machine: "x64", Subsystem: "gui"}, "rename it to end in .exe"},
		"ne.msi":   {".msi", winpe.Report{Kind: "ne"}, "rename it to end in .exe"},
		"arm64":    {".exe", winpe.Report{Kind: "pe", Machine: "arm64", Subsystem: "gui"}, "64-bit ARM"},
		"arm":      {".exe", winpe.Report{Kind: "pe", Machine: "arm", Subsystem: "gui"}, "32-bit ARM"},
		"mmachine": {".exe", winpe.Report{Kind: "pe", Machine: "other", Subsystem: "gui"}, "x86 and x64 programs only"},
	} {
		var out, errOut bytes.Buffer
		err := tell("setup"+c.ext, c.ext, c.r, "", &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		if out.Len() != 0 {
			t.Errorf("%s: printed %q", name, out.String())
		}
	}
}

func TestTellFindings(t *testing.T) {
	names := make([]string, 25)
	for i := range names {
		names[i] = fmt.Sprintf("lib%d.dll", i)
	}
	r := winpe.Report{
		Kind: "pe", Machine: "x86", Subsystem: "gui",
		DotNet:       &winpe.DotNet{Version: "v2.0.50727", Needs32: true},
		Imports:      names,
		DelayImports: []string{"comdlg32.dll"},
		Skipped:      2,
		Elevation:    "requireAdministrator",
		Packer:       "NSIS",
	}
	var out, errOut bytes.Buffer
	if err := tell("setup.exe", ".exe", r, "", &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Checked setup.exe in a sandbox:",
		"32-bit program with a window (from its file header)",
		"at most 2 GB",
		"runtime v2.0.50727 (from its .NET header); it needs .NET 2.0 to 3.5",
		"runs as 32-bit only",
		"made with NSIS (from its section names)",
		"administrator rights (from its manifest)",
		"says nothing about scaling",
		"uses 25 libraries (from its import table): lib0.dll, ",
		"lib19.dll and 5 more",
		"may load 1 library more later (from its delay-load table): comdlg32.dll",
		"2 libraries left out",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "lib20.dll") || errOut.Len() != 0 {
		t.Errorf("output:\n%s\n%s", out.String(), errOut.String())
	}

	// A 64-bit program that scales, and an .msi package: no warnings.
	out.Reset()
	if err := tell("tool.exe", ".exe", winpe.Report{Kind: "pe", Machine: "x64", Subsystem: "console"}, "", &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "64-bit program for the terminal") || strings.Contains(s, "2 GB") || strings.Contains(s, "scaled") {
		t.Errorf("x64 console:\n%s", s)
	}
	out.Reset()
	if err := tell("extra.msi", ".msi", winpe.Report{Kind: "msi"}, "", &out, &errOut); err != nil || !strings.Contains(out.String(), "msiexec installs it") {
		t.Errorf("msi: %v\n%s", err, out.String())
	}
	// A 16-bit program goes on with a warning.
	if err := tell("old.exe", ".exe", winpe.Report{Kind: "ne"}, "", &out, &errOut); err != nil || !strings.Contains(errOut.String(), "16-bit program") {
		t.Errorf("ne: %v\n%s", err, errOut.String())
	}
}

func TestParseScanOut(t *testing.T) {
	if r, err := parseScanOut([]byte(`{"kind":"pe","machine":"x64","subsystem":"gui","imports":["kernel32.dll"]}`)); err != nil || r.Imports[0] != "kernel32.dll" {
		t.Errorf("good answer: %+v %v", r, err)
	}
	for _, bad := range []string{
		``,
		`{"kind":"pe","machine":"x64","subsystem":"gui","extra":1}`,
		`{"kind":"msi"}{"kind":"msi"}`,
		`{"kind":"pe","machine":"x64","subsystem":"gui","imports":["../../etc"]}`,
		`{"kind":"exe"}`,
		`[1]`,
	} {
		if _, err := parseScanOut([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPreflight(t *testing.T) {
	old := scanFile
	t.Cleanup(func() { scanFile = old })
	dir := t.TempDir()
	installer := filepath.Join(dir, "setup.exe")
	writeFile(t, installer, []byte("SYNTHETIC installer"))

	// A scan that fails warns and lets the installer run.
	scanFile = func(*os.File) (winpe.Report, error) { return winpe.Report{}, errors.New("SYNTHETIC failure") }
	var out, errOut bytes.Buffer
	if err := preflight(installer, &out, &errOut); err != nil || !strings.Contains(errOut.String(), "WARNING: could not check setup.exe") {
		t.Errorf("failed scan: %v %q", err, errOut.String())
	}
	// A link is not followed.
	link := filepath.Join(dir, "link.exe")
	if err := os.Symlink(installer, link); err != nil {
		t.Fatal(err)
	}
	scanFile = fakeScan
	if _, err := scanPath(link); err == nil {
		t.Error("scanPath followed a link")
	}
	if _, err := scanPath(dir); err == nil {
		t.Error("scanPath took a folder")
	}
	// The real scan of a SYNTHETIC text file stops it.
	scanFile = func(f *os.File) (winpe.Report, error) {
		info, _ := f.Stat()
		return winpe.Scan(f, info.Size())
	}
	if err := preflight(installer, &out, &errOut); err == nil || !strings.Contains(err.Error(), "neither a program") {
		t.Errorf("text file: %v", err)
	}
	if err := checkCmd([]string{installer}, &out, &errOut); err == nil {
		t.Error("check passed a text file")
	}
	if err := checkCmd(nil, &out, &errOut); !errors.Is(err, errUsage) {
		t.Errorf("check without a file: %v", err)
	}
}

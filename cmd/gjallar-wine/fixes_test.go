package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
)

// The package build runs only this command's tests, so the pin check of
// internal/winefix runs here too, on the winetricks the package names.
func TestWinetricksPins(t *testing.T) {
	p := os.Getenv("WINETRICKS_SCRIPT")
	if p == "" {
		if os.Getenv("NIX_BUILD_TOP") != "" {
			t.Fatal("WINETRICKS_SCRIPT is not set in the package build")
		}
		t.Skip("WINETRICKS_SCRIPT is not set; the package build sets it")
	}
	script, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := winefix.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := winefix.CheckWinetricks(script, tab); err != nil {
		t.Fatal(err)
	}
}

func TestFixesCmd(t *testing.T) {
	for args, want := range map[string][]string{
		"":          {"Fixes this version knows:", "vcrun2010", "2 files, 10.8 MB", "Not offered:", "vcrun2019"},
		"--ids":     {"vcrun2005\n", "physx\n"},
		"vcrun2013": {"vcrun2013: Visual C++ 2013 runtime", "replaces some of Wine's own", "vcredist_x64.exe from download.microsoft.com, 7.2 MB", "sha256 20e2645b"},
		"VCRUN2008": {"vcrun2008: Visual C++ 2008 runtime"},
		"webview2":  {"webview2 is not offered: its publisher offers no versioned download"},
		"MSVCP100":  {"msvcp100.dll comes with:", "vcrun2010"},
	} {
		var out bytes.Buffer
		var a []string
		if args != "" {
			a = []string{args}
		}
		if err := fixesCmd(a, &out); err != nil {
			t.Errorf("%q: %v", args, err)
			continue
		}
		for _, w := range want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%q: no %q in\n%s", args, w, out.String())
			}
		}
	}
	var out bytes.Buffer
	if err := fixesCmd([]string{"--ids"}, &out); err != nil || strings.Contains(out.String(), "vcrun2019") {
		t.Errorf("--ids lists a refused row: %v %q", err, out.String())
	}
	if err := fixesCmd([]string{"nothing.dll"}, &out); err == nil || !strings.Contains(err.Error(), "neither a fix nor a library") {
		t.Errorf("unknown: %v", err)
	}
	for _, a := range [][]string{{"a", "b"}, {"--json"}} {
		if err := fixesCmd(a, &out); !errors.Is(err, errUsage) {
			t.Errorf("%q: %v", a, err)
		}
	}
}

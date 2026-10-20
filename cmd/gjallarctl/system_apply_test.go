package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// useLocalFolder gives the test a developer-mode folder in its home and no
// root copy yet. The folder's contents are SYNTHETIC.
func useLocalFolder(t *testing.T, bus *fakeSettingsBus, developerMode bool, files map[string]string) {
	t.Helper()
	useFakeSettings(t, bus)
	value, _ := json.Marshal(developerMode)
	bus.values["developerMode"] = settings.KeyValue{Key: "developerMode", Tier: config.TierCritical, Value: value}
	old := localChangesPath
	localChangesPath = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { localChangesPath = old })
	dir := filepath.Join(os.Getenv("HOME"), "GjallarOS-local")
	for rel, data := range files {
		name := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSystemApplySendsFolderAndShowsDiff(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, true, map[string]string{
		"home/editor.nix": "{\n  programs.example.enable = true; \x1b[2K\n}\n",
		"system/logo.png": "SYNTHETIC",
		"home/_skip.nix":  "{ }\n",
		"README.md":       "SYNTHETIC notes\n",
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if len(bus.folders) != 1 || len(bus.folders[0]) != 4 {
		t.Fatalf("sent folders %v", bus.folders)
	}
	if len(bus.applied) != 1 {
		t.Fatalf("applied %q", bus.applied)
	}
	out := stdout.String()
	for _, want := range []string{
		"home/editor.nix: added",
		"+++ new/home/editor.nix",
		`+  programs.example.enable = true; \x1b[2K`,
		"system/logo.png: added\n  (binary file, not shown)",
		"New folder hash: ",
		"local home/editor.nix: added",
		"the change is live",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("stdout holds a raw escape:\n%q", out)
	}
}

func TestSystemApplyNeedsDeveloperMode(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, false, map[string]string{"home/editor.nix": "{ }\n"})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "apply"}, &stdout, &stderr); code != 1 || len(bus.folders) != 0 {
		t.Fatalf("code %d, sent %d", code, len(bus.folders))
	}
	if !strings.Contains(stderr.String(), "developer mode is off") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestSystemApplyRefusesBadFolder(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, true, map[string]string{"home/run.sh": "SYNTHETIC\n"})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "apply"}, &stdout, &stderr); code != 1 || len(bus.folders) != 0 {
		t.Fatalf("code %d, sent %d", code, len(bus.folders))
	}
}

func TestSystemApplyRefusesRoot(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, true, map[string]string{"home/editor.nix": "{ }\n"})
	settingsEUID = func() int { return 0 }
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "apply"}, &stdout, &stderr); code != 1 || len(bus.folders) != 0 {
		t.Fatalf("code %d, sent %d", code, len(bus.folders))
	}
}

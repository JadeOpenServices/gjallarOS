package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

// useDeveloperOff sets up a recovery entry with a system checkout whose
// configuration has developer mode on. The fake rebuild writes nextBoot as
// the next boot's build source. It returns the configuration path.
func useDeveloperOff(t *testing.T, developerMode bool, nextBoot string) string {
	t.Helper()
	oldEUID, oldEntry, oldDest := settingsEUID, recoveryEntryPath, systemCloneDest
	oldRebuild, oldOwnership, oldNext := developerOffRebuild, developerOffOwnership, nextBootBuildSourcePath
	t.Cleanup(func() {
		settingsEUID, recoveryEntryPath, systemCloneDest = oldEUID, oldEntry, oldDest
		developerOffRebuild, developerOffOwnership, nextBootBuildSourcePath = oldRebuild, oldOwnership, oldNext
	})
	settingsEUID = func() int { return 0 }
	dir := t.TempDir()
	recoveryEntryPath = filepath.Join(dir, "recovery")
	writeTestFile(t, recoveryEntryPath, "")
	systemCloneDest = filepath.Join(dir, "system")
	if err := os.MkdirAll(filepath.Join(systemCloneDest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	developerOffOwnership = func(string) error { return nil }

	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	user.DeveloperMode = developerMode
	data, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(systemCloneDest, "user.config.json")
	writeTestFile(t, path, string(data)+"\n")

	nextBootBuildSourcePath = filepath.Join(dir, "next", "build-source.json")
	script := filepath.Join(dir, "rebuild")
	writeTestFile(t, script, "#!/bin/sh\nmkdir -p "+filepath.Dir(nextBootBuildSourcePath)+
		"\nprintf '%s' '"+nextBoot+"' > "+nextBootBuildSourcePath+"\necho rebuilt\n")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	developerOffRebuild = []string{script}
	return path
}

func TestSystemDeveloperOffTurnsItOffAndChecksTheNextBoot(t *testing.T) {
	path := useDeveloperOff(t, true, `{"revision":"0123456789abcdef"}`)
	var stdout, stderr bytes.Buffer
	if status := systemDeveloperOff(nil, &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	if on, err := developerModeIn(path); err != nil || on {
		t.Fatalf("developer mode after: %v %v", on, err)
	}
	for _, want := range []string{"Developer mode: off in " + path, "rebuilt", "PASS: the next boot is the official system 0123456"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestSystemDeveloperOffRebuildsWhenAlreadyOff(t *testing.T) {
	useDeveloperOff(t, false, `{"revision":"0123456789abcdef"}`)
	var stdout, stderr bytes.Buffer
	if status := systemDeveloperOff(nil, &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already off") || !strings.Contains(stdout.String(), "PASS") {
		t.Fatalf("output:\n%s", stdout.String())
	}
}

// The bug this command replaces: a rebuild that still built local files
// was reported as the official system.
func TestSystemDeveloperOffFailsWhileTheNextBootHoldsLocalFiles(t *testing.T) {
	useDeveloperOff(t, true, `{"revision":"0123456789abcdef","local":{"files":["system/test.nix"],"hash":"f70e5ecb1c1ddca27e1df6747d0419d9a13ec18650785fea8629aab7f09a1e3c"}}`)
	var stdout, stderr bytes.Buffer
	if status := systemDeveloperOff(nil, &stdout, &stderr); status != 1 {
		t.Fatalf("status %d", status)
	}
	if strings.Contains(stdout.String(), "PASS") || !strings.Contains(stderr.String(), "still holds local files f70e5ecb1c1d") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestSystemDeveloperOffFailsWhenTheRebuildFails(t *testing.T) {
	useDeveloperOff(t, true, `{}`)
	developerOffRebuild = []string{"false"}
	var stdout, stderr bytes.Buffer
	if status := systemDeveloperOff(nil, &stdout, &stderr); status != 1 || strings.Contains(stdout.String(), "PASS") {
		t.Fatalf("status %d:\n%s", status, stdout.String())
	}
	if !strings.Contains(stderr.String(), "the rebuild failed") {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
}

func TestSystemDeveloperOffRefusesOutsideRecoveryAndAsUser(t *testing.T) {
	path := useDeveloperOff(t, true, `{}`)
	recoveryEntryPath = filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	if status := systemDeveloperOff(nil, &stdout, &stderr); status != 1 || !strings.Contains(stderr.String(), "trusted recovery boot entry only") {
		t.Fatalf("outside recovery: status %d: %s", status, stderr.String())
	}
	settingsEUID = func() int { return 1000 }
	stderr.Reset()
	if status := systemDeveloperOff(nil, &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "runs as root") {
		t.Fatalf("as user: status %d: %s", status, stderr.String())
	}
	if on, err := developerModeIn(path); err != nil || !on {
		t.Fatalf("a refusal changed the configuration: %v %v", on, err)
	}
}

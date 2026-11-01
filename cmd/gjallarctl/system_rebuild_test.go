package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
)

func TestSystemRebuildArgsRoundTrip(t *testing.T) {
	rev := strings.Repeat("ab", 20)
	opts := rebuildOptions{
		update: true, hardwareUpdate: true, channel: "staging", switchNow: "1",
		debug: true, cleanup: false,
		rebuildArgs: []string{"--override-input", "oddc", "github:JadeOpenServices/oddc/" + rev},
	}
	args, err := systemRebuildArgs(opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--update", "--hardware-update", "--channel", "staging", "--switch", "1", "--debug", "--no-cleanup", "--oddc-commit", rev}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
	parsed, err := parseSystemRebuildArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.repo != "" || parsed.host != "" || !parsed.update || !parsed.hardwareUpdate ||
		parsed.channel != "staging" || parsed.switchNow != "1" || !parsed.debug || parsed.cleanup ||
		!slices.Equal(parsed.rebuildArgs, opts.rebuildArgs) {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestSystemRebuildArgsRefuseUserChoices(t *testing.T) {
	for name, opts := range map[string]rebuildOptions{
		"repo":          {repo: "/home/someone/gjallarOS", cleanup: true},
		"host":          {host: "other", cleanup: true},
		"update inputs": {updateInputs: true, cleanup: true},
		"nix argument":  {rebuildArgs: []string{"--override-input", "nixpkgs", "path:/tmp/x"}, cleanup: true},
		"impure":        {rebuildArgs: []string{"--impure"}, cleanup: true},
		"oddc branch":   {rebuildArgs: []string{"--override-input", "oddc", "github:JadeOpenServices/oddc/stable"}, cleanup: true},
	} {
		if args, err := systemRebuildArgs(opts); err == nil {
			t.Errorf("%s: accepted as %q", name, args)
		}
	}
}

func TestParseSystemRebuildArgsIsStrict(t *testing.T) {
	for _, args := range [][]string{
		{"--repo", "/tmp/x"},
		{"--host", "other"},
		{"--", "--impure"},
		{"--channel", "main"},
		{"--switch"},
		{"--oddc-commit"},
	} {
		if _, err := parseSystemRebuildArgs(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	opts, err := parseSystemRebuildArgs(nil)
	if err != nil || !opts.cleanup || opts.rebuildArgs != nil {
		t.Fatalf("no arguments: %+v, %v", opts, err)
	}
}

func TestStageLocalChanges(t *testing.T) {
	saved := localChangesPath
	t.Cleanup(func() { localChangesPath = saved })
	localChangesPath = filepath.Join(t.TempDir(), "local")
	if err := os.MkdirAll(filepath.Join(localChangesPath, "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localChangesPath, "system", "a.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	facts := &flakesource.BuildSource{}
	if _, status := stageLocalChanges(false, true, facts, &stdout, &stderr); status != 0 || facts.Local != nil {
		t.Fatalf("development checkout staged the folder: %d %v", status, facts.Local)
	}
	if _, status := stageLocalChanges(true, false, facts, &stdout, &stderr); status != 0 || facts.Local != nil {
		t.Fatalf("developer mode off staged the folder: %d %v", status, facts.Local)
	}
	if !strings.Contains(stdout.String(), "Developer mode is off") {
		t.Fatalf("no note with developer mode off: %q", stdout.String())
	}
	// The test user owns the folder, so root's copy check refuses it.
	if _, status := stageLocalChanges(true, true, facts, &stdout, &stderr); status != 1 || facts.Local != nil {
		t.Fatalf("a folder not owned by root was staged: %d %v", status, facts.Local)
	}
	localChangesPath = filepath.Join(t.TempDir(), "missing")
	stdout.Reset()
	if _, status := stageLocalChanges(true, true, facts, &stdout, &stderr); status != 0 || facts.Local != nil ||
		!strings.Contains(stdout.String(), "holds no .nix file") {
		t.Fatalf("missing folder: %d %v %q", status, facts.Local, stdout.String())
	}
}

func TestSystemRebuildNeedsDeveloperGrant(t *testing.T) {
	saved := developerGrantPath
	t.Cleanup(func() { developerGrantPath = saved })
	developerGrantPath = filepath.Join(t.TempDir(), "developer-grant.json")

	upstream, checkout := channelCheckout(t, "dev", "dev")
	gitRun(t, upstream, "commit", "-q", "--allow-empty", "-m", "two")
	before := gitOut(t, checkout, "rev-parse", "refs/remotes/origin/dev")
	var stdout, stderr bytes.Buffer
	if code := rebuildCheckout(checkout, rebuildOptions{update: true}, true, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "confirm-developer") {
		t.Fatalf("dev without a grant: %d %q", code, stderr.String())
	}
	if after := gitOut(t, checkout, "rev-parse", "refs/remotes/origin/dev"); after != before {
		t.Fatal("the dev channel was fetched without a grant")
	}

	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if code := checkDeveloperGrant(true, user, &stderr); code != 0 {
		t.Fatalf("stable without a grant: %d", code)
	}
	user.ReleaseChannel = "dev"
	if code := checkDeveloperGrant(false, user, &stderr); code != 0 {
		t.Fatal("a development checkout asked for the grant")
	}
	if err := devgate.Save(developerGrantPath, devgate.Grant{Channels: true, Proof: devgate.ProofPIN}); err != nil {
		t.Fatal(err)
	}
	if code := checkDeveloperGrant(true, user, &stderr); code != 0 {
		t.Fatalf("dev with a channel grant: %d %q", code, stderr.String())
	}
	user.DeveloperMode = true
	stderr.Reset()
	if code := checkDeveloperGrant(true, user, &stderr); code != 1 || !strings.Contains(stderr.String(), "developer mode needs") {
		t.Fatalf("developer mode on a channel grant: %d %q", code, stderr.String())
	}
	if err := os.WriteFile(developerGrantPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	user.DeveloperMode = false
	if code := checkDeveloperGrant(true, user, &stderr); code != 1 {
		t.Fatal("a broken grant let dev build")
	}

	// A configuration changed outside the settings service narrows the
	// grant at the next rebuild.
	if err := devgate.Save(developerGrantPath, devgate.Grant{Channels: true, DeveloperMode: true, Proof: devgate.ProofPIN}); err != nil {
		t.Fatal(err)
	}
	if code := checkDeveloperGrant(true, user, &stderr); code != 0 {
		t.Fatalf("dev with a wide grant: %d %q", code, stderr.String())
	}
	if g, err := devgate.Load(developerGrantPath); err != nil || !g.Channels || g.DeveloperMode {
		t.Fatalf("grant after a rebuild without developer mode: %+v %v", g, err)
	}
	user.ReleaseChannel = "stable"
	if code := checkDeveloperGrant(true, user, &stderr); code != 0 {
		t.Fatalf("stable with a grant: %d %q", code, stderr.String())
	}
	if _, err := os.Stat(developerGrantPath); !os.IsNotExist(err) {
		t.Fatalf("grant left after a stable rebuild: %v", err)
	}

	// A running system from before the gate cannot take the proof, so it
	// builds the version that can; the gate holds again once it is there.
	savedSocket := devproofSocketPath
	t.Cleanup(func() { devproofSocketPath = savedSocket })
	devproofSocketPath = filepath.Join(t.TempDir(), "missing.socket")
	user.ReleaseChannel = "staging"
	stderr.Reset()
	if code := checkDeveloperGrant(true, user, &stderr); code != 0 || !strings.Contains(stderr.String(), "confirm-developer once") {
		t.Fatalf("migration from a system without the gate: %d %q", code, stderr.String())
	}
	if _, err := os.Stat(developerGrantPath); !os.IsNotExist(err) {
		t.Fatalf("the migration wrote a grant: %v", err)
	}
	devproofSocketPath = savedSocket
	if code := checkDeveloperGrant(true, user, &stderr); code != 1 {
		t.Fatal("staging without a grant built on a system with the gate")
	}
}

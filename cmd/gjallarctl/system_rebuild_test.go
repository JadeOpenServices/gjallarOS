package main

import (
	"slices"
	"strings"
	"testing"
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

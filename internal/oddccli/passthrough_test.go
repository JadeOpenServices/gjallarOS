package oddccli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeODDC stands in for the oddc command: it echoes its arguments and
// exits 3.
func fakeODDC(t *testing.T) {
	t.Helper()

	script := filepath.Join(t.TempDir(), "oddc")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\"\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	old := Path
	Path = script
	t.Cleanup(func() { Path = old })
}

func TestRunPassesCommandsUnchanged(t *testing.T) {
	fakeODDC(t)

	for _, args := range [][]string{
		{"list", "--kind", "DeviceModel"},
		{"resolve"},
		{"detect"},
		{"no-such-command", "--flag"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 3 {
			t.Fatalf("%v: exit code %d, want oddc's 3; stderr %q", args, code, stderr.String())
		}
		if got, want := strings.TrimSpace(stdout.String()), strings.Join(args, " "); got != want {
			t.Errorf("oddc argv = %q, want %q", got, want)
		}
	}
}

func TestRunRequiresSubcommand(t *testing.T) {
	fakeODDC(t)

	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("oddc ran: %q", stdout.String())
	}
}

func TestRunReportsMissingODDC(t *testing.T) {
	old := Path
	Path = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { Path = old })

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"list"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "ERROR: run oddc") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestRunRefusesUpdate(t *testing.T) {
	fakeODDC(t)

	for _, args := range [][]string{
		{"update"},
		{"update", "--channel", "staging"},
		{"update", "--flake", "/etc/gjallar/system"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("%v: exit code %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%v: oddc ran: %q", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "gjallarctl rebuild --hardware-update") {
			t.Fatalf("%v: stderr %q", args, stderr.String())
		}
	}
}

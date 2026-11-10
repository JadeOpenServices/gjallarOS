package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInAISession(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// EXAMPLE cgroup lines in the cgroup v2 format.
	session := write("session", "0::/system.slice/system-ai\\x2dsession.slice/ai-session@-workspace-example.service\n")
	desktop := write("desktop", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-example.scope\n")
	if !inAISession(session) {
		t.Error("process in ai-session@ may approve")
	}
	if inAISession(desktop) {
		t.Error("process in the user's own session refused")
	}
	if !inAISession(filepath.Join(dir, "missing")) {
		t.Error("unreadable cgroup treated as outside the AI session")
	}
}

func TestAIApproveRefusesWithoutTerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	saved := os.Stdin
	os.Stdin = devNull
	defer func() { os.Stdin = saved }()
	grant := filepath.Join(t.TempDir(), "approval.json")
	var stdout, stderr bytes.Buffer
	if code := runAIApprove([]string{"--grant", grant, "nixos-deploy", "switch"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "trusted interactive terminal") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if _, err := os.Stat(grant); !os.IsNotExist(err) {
		t.Fatal("grant written without a terminal")
	}
}

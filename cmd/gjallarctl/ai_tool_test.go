package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// The gjallar-agent-tool wrapper ends its own flags with "--". Flags the
// agent adds after that are tool arguments, not a new --mode or --approval
// (loophole 44).
func TestAIToolAgentCannotOverrideWrapperFlags(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runAITool([]string{
		"--workspace", dir,
		"--mode", "workspace",
		"--approval", filepath.Join(dir, "approval.json"),
		"--audit", filepath.Join(dir, "audit.jsonl"),
		"--",
		"--mode", "owner-full-local", "local-command", "true",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("injected --mode ran a command")
	}
	if !strings.Contains(stderr.String(), "--mode") {
		t.Fatalf("stderr %q, want the injected flag treated as the tool name", stderr.String())
	}
}

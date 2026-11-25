package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandExitStatusLikeAShell(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   int
	}{
		{"exit 0", 0},
		{"exit 3", 3},
		{"kill -TERM $$", 128 + int(syscall.SIGTERM)},
		{"kill -INT $$", 130},
	} {
		cmd := exec.Command("sh", "-c", tc.script)
		var stderr bytes.Buffer
		if got := commandExitStatus(cmd, cmd.Run(), &stderr); got != tc.want {
			t.Errorf("%q: status %d, want %d", tc.script, got, tc.want)
		}
	}

	cmd := exec.Command(filepath.Join(t.TempDir(), "missing"))
	var stderr bytes.Buffer
	if got := commandExitStatus(cmd, cmd.Run(), &stderr); got != 1 {
		t.Errorf("missing command: status %d, want 1", got)
	}
	if !strings.HasPrefix(stderr.String(), "ERROR: missing: ") {
		t.Errorf("missing command: stderr %q", stderr.String())
	}
}

// Plane 312: sudo has to be in the terminal's foreground job to get Ctrl+C,
// and gjallarctl must not die of a SIGINT while the root command still runs.
func TestForegroundCommandStaysInProcessGroupAndOutlivesCtrlC(t *testing.T) {
	// The child prints its process group, sends SIGINT to this test process
	// and exits 7. Uncaught, the SIGINT would end the test binary.
	script := `read -r pid comm state ppid pgrp rest < /proc/$$/stat; echo "$pgrp"; kill -INT "$PPID"; exit 7`
	var stdout, stderr bytes.Buffer
	status := runForegroundCommand(&stdout, &stderr, "sh", "-c", script)
	if status != 7 {
		t.Fatalf("status %d, want 7 (stderr %q)", status, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), strconv.Itoa(syscall.Getpgrp()); got != want {
		t.Fatalf("child process group %s, want this process's group %s", got, want)
	}
}

func TestRebuildPlanRunsNothingOnceCancelled(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	touch := []string{"sh", "-c", "echo ran > " + marker}
	plan := rebuildPlan{eval: touch, build: touch, activate: touch}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	status, activated := plan.run(ctx, &stdout, &stderr)
	if status != 130 || activated {
		t.Fatalf("status %d activated %v, want 130 false", status, activated)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a cancelled rebuild ran a command")
	}
}

// A cancelled command gets SIGTERM in its whole process group, so nix can
// stop its builders, instead of a SIGKILL for the command alone.
func TestRunCommandCancelSendsSIGTERMToTheGroup(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	child := filepath.Join(dir, "child")
	// The trap shows the command got SIGTERM; the second sh, a grandchild in
	// the same group, must get it as well. The grandchild gives up on its
	// own after a while and keeps off the output pipe, so a broken cancel
	// fails the test instead of hanging it.
	script := `trap 'echo term >> ` + child + `; exit 0' TERM
sh -c 'trap "echo term >> ` + child + `; exit 0" TERM; i=0; while [ $i -lt 2000000 ]; do i=$((i+1)); done' >/dev/null 2>&1 &
echo > ` + ready + `
while :; do :; done`

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(ready); err == nil {
				time.Sleep(100 * time.Millisecond)
				cancel()
				return
			}
		}
		cancel()
	}()

	var stdout, stderr bytes.Buffer
	if status := runCommand(ctx, &stdout, &stderr, "sh", "-c", script); status != 130 {
		t.Fatalf("status %d, want 130 (stderr %q)", status, stderr.String())
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		data, _ := os.ReadFile(child)
		if strings.Count(string(data), "term") == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SIGTERM reached %d of 2 processes", strings.Count(string(data), "term"))
		}
	}
}

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivilegeFingerprintCommandHasNoPasswordInput(t *testing.T) {
	tty, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()

	const authHelper = "/nix/store/test-gjallarctl/bin/gjallar-sudo-auth"

	cmd := privilegeFingerprintCommand(
		context.Background(),
		tty,
		authHelper,
	)

	if cmd.Stdin != nil {
		t.Fatal("fingerprint authentication must not receive password-capable stdin")
	}
	if cmd.Stdout != tty || cmd.Stderr != tty {
		t.Fatal("fingerprint authentication messages must use the controlling TTY")
	}

	if len(cmd.Args) != 2 ||
		cmd.Args[0] != "sudo" ||
		cmd.Args[1] != authHelper {
		t.Fatalf("unexpected fingerprint command: %#v", cmd.Args)
	}

	for _, arg := range cmd.Args {
		if arg == "-A" {
			t.Fatal("fingerprint phase must not use askpass")
		}
	}

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("fingerprint authentication process group isolation missing")
	}
}

func TestPrivilegePasswordCommandUsesAskpassWithoutStdin(t *testing.T) {
	tty, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()

	const askpass = "/nix/store/test-gjallarctl/bin/gjallar-sudo-askpass"
	const authHelper = "/nix/store/test-gjallarctl/bin/gjallar-sudo-auth"

	t.Setenv("SUDO_ASKPASS", "/tmp/untrusted-askpass")

	cmd := privilegePasswordCommand(
		context.Background(),
		tty,
		askpass,
		authHelper,
	)

	if cmd.Stdin != nil {
		t.Fatal("password authentication must not receive password-capable stdin")
	}
	if cmd.Stdout != tty || cmd.Stderr != tty {
		t.Fatal("password authentication messages must use the controlling TTY")
	}

	if len(cmd.Args) != 3 ||
		cmd.Args[0] != "sudo" ||
		cmd.Args[1] != "-A" ||
		cmd.Args[2] != authHelper {
		t.Fatalf("unexpected password command: %#v", cmd.Args)
	}

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("password authentication process group isolation missing")
	}
	// The askpass prompt needs the terminal; a background job would stop on
	// SIGTTOU/SIGTTIN and the fallback would hang silently.
	if !cmd.SysProcAttr.Foreground || cmd.SysProcAttr.Ctty != int(tty.Fd()) {
		t.Fatal("password authentication must run as the terminal foreground job")
	}

	var values []string
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "SUDO_ASKPASS=") {
			values = append(values, entry)
		}
	}

	if len(values) != 1 {
		t.Fatalf("expected exactly one SUDO_ASKPASS entry, got %#v", values)
	}

	if values[0] != "SUDO_ASKPASS="+askpass {
		t.Fatalf("unexpected SUDO_ASKPASS: %q", values[0])
	}
}

func TestEnvironmentWithOverrideReplacesExistingValue(t *testing.T) {
	t.Setenv("SUDO_ASKPASS", "/tmp/old")

	env := environmentWithOverride(
		"SUDO_ASKPASS",
		"/nix/store/new/bin/gjallar-sudo-askpass",
	)

	var matches []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "SUDO_ASKPASS=") {
			matches = append(matches, entry)
		}
	}

	if len(matches) != 1 {
		t.Fatalf("expected one SUDO_ASKPASS entry, got %#v", matches)
	}

	if matches[0] !=
		"SUDO_ASKPASS=/nix/store/new/bin/gjallar-sudo-askpass" {
		t.Fatalf("unexpected override: %q", matches[0])
	}
}

func TestSudoAskpassPromptsOnControllingTerminal(t *testing.T) {
	data, err := os.ReadFile("../../pkgs/gjallarctl/default.nix")
	if errors.Is(err, os.ErrNotExist) {
		// The package build only receives Go sources.
		t.Skip("package definition not in the Go build source")
	}
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(data), "<<'ASKPASS'")
	end := strings.Index(string(data), "\nASKPASS\n")
	if start < 0 || end < start {
		t.Fatal("gjallar-sudo-askpass script not found")
	}
	script := string(data)[start:end]

	// sudo hands askpass the detached stdin. Without the terminal redirect,
	// systemd-ask-password waits forever for a user agent.
	if !strings.Contains(script, `"$@" </dev/tty`) {
		t.Fatal("askpass must read from the controlling terminal")
	}
	if strings.Contains(script, "--user") {
		t.Fatal("askpass must not wait for a user password agent")
	}
	// An unattended prompt waited all night after a rebuild.
	if strings.Contains(script, "--timeout=0") || !strings.Contains(script, "--timeout=") {
		t.Fatal("askpass must give up after a timeout")
	}
}

func TestAuthRejectsArguments(t *testing.T) {
	var stderr strings.Builder
	if status := run([]string{"auth", "ls"}, io.Discard, &stderr); status != 2 {
		t.Fatalf("auth with arguments returned %d", status)
	}
	if !strings.Contains(stderr.String(), "Usage: gjallarctl auth") {
		t.Fatalf("missing usage: %q", stderr.String())
	}
}

// gjallarctl operations ask every time and leave no sudo timestamp; plain
// sudo keeps its own timeout.
func TestPrivilegeSessionDropsTimestampBeforeAndAfter(t *testing.T) {
	calls := 0
	saved := invalidateSudoTimestamp
	invalidateSudoTimestamp = func() { calls++ }
	t.Cleanup(func() { invalidateSudoTimestamp = saved; privilegeSessionStarted = false })

	endPrivilegeSession()
	if calls != 0 {
		t.Fatalf("no privileged operation ran, but the timestamp was dropped %d times", calls)
	}
	beginPrivilegeSession()
	beginPrivilegeSession()
	if calls != 1 {
		t.Fatalf("a second privileged step in one operation must reuse the fresh timestamp, dropped %d times", calls)
	}
	endPrivilegeSession()
	if calls != 2 {
		t.Fatalf("timestamp not dropped at exit: %d calls", calls)
	}
}

func TestHelperExecutablePrefersSystemCopy(t *testing.T) {
	store := t.TempDir()
	helper := filepath.Join(store, "gjallar-sudo-auth")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o555); err != nil {
		t.Fatal(err)
	}
	system := t.TempDir()
	if err := os.Symlink(helper, filepath.Join(system, "gjallar-sudo-auth")); err != nil {
		t.Fatal(err)
	}

	old := systemHelperDir
	systemHelperDir = system
	t.Cleanup(func() { systemHelperDir = old })

	// sudoers names the store path, so the symlink must be resolved.
	got, err := helperExecutable("gjallar-sudo-auth")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(helper)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("helperExecutable = %q, want %q", got, want)
	}
}

func TestHelperExecutableMissingNamesSystemDir(t *testing.T) {
	old := systemHelperDir
	systemHelperDir = t.TempDir()
	t.Cleanup(func() { systemHelperDir = old })

	// The test binary has no helpers next to it, like go run.
	_, err := helperExecutable("gjallar-sudo-auth")
	if err == nil || !strings.Contains(err.Error(), systemHelperDir) {
		t.Fatalf("expected error naming %s, got %v", systemHelperDir, err)
	}
}

// A rebuild outlasts sudo's 5 minute timestamp; the session keeps it fresh
// until exit so the cleanup step does not ask again, then stops refreshing.
func TestPrivilegeSessionKeepsTimestampAliveUntilExit(t *testing.T) {
	savedRefresh, savedInvalidate, savedInterval := refreshSudoTimestamp, invalidateSudoTimestamp, privilegeKeepaliveInterval
	refreshed := make(chan struct{}, 16)
	refreshSudoTimestamp = func() bool { refreshed <- struct{}{}; return true }
	invalidateSudoTimestamp = func() {}
	privilegeKeepaliveInterval = time.Millisecond
	t.Cleanup(func() {
		refreshSudoTimestamp, invalidateSudoTimestamp, privilegeKeepaliveInterval = savedRefresh, savedInvalidate, savedInterval
		privilegeSessionStarted, stopPrivilegeKeepalive = false, nil
	})

	keepPrivilegeAlive()
	if stopPrivilegeKeepalive != nil {
		t.Fatal("keepalive started without a privilege session")
	}
	beginPrivilegeSession()
	keepPrivilegeAlive()
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("timestamp never refreshed")
	}
	endPrivilegeSession()
	if stopPrivilegeKeepalive != nil {
		t.Fatal("keepalive still set after exit")
	}
	for len(refreshed) > 0 {
		<-refreshed
	}
	time.Sleep(20 * time.Millisecond)
	if len(refreshed) != 0 {
		t.Fatal("timestamp refreshed after the session ended")
	}
}

func TestPrivilegeAuthenticationReusesLiveSessionQuietly(t *testing.T) {
	savedRefresh, savedInvalidate := refreshSudoTimestamp, invalidateSudoTimestamp
	refreshes := 0
	refreshSudoTimestamp = func() bool { refreshes++; return true }
	invalidateSudoTimestamp = func() {}
	t.Cleanup(func() {
		refreshSudoTimestamp, invalidateSudoTimestamp = savedRefresh, savedInvalidate
		privilegeSessionStarted, stopPrivilegeKeepalive = false, nil
	})

	privilegeSessionStarted = true
	stopPrivilegeKeepalive = func() {}
	var stderr strings.Builder
	if status := runPrivilegeAuthentication(context.Background(), &stderr); status != 0 {
		t.Fatalf("status = %d, want 0", status)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
	if stderr.Len() != 0 {
		t.Fatalf("live session printed %q", stderr.String())
	}
}

func TestAuthenticationResultClosesThePrompt(t *testing.T) {
	for _, c := range []struct {
		status int
		want   string
	}{
		{0, "[GjallarOS] Authenticated.\n"},
		{1, "[GjallarOS] Authentication failed.\n"},
		{130, ""},
	} {
		var tty strings.Builder
		if got := reportAuthentication(&tty, c.status); got != c.status {
			t.Fatalf("status %d came back as %d", c.status, got)
		}
		if tty.String() != c.want {
			t.Fatalf("status %d printed %q, want %q", c.status, tty.String(), c.want)
		}
	}
}

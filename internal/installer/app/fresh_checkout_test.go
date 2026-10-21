package app

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// SYNTHETIC repository contents, not taken from any machine.
var officialFiles = map[string]string{
	".gitignore":     "generated/\nuser.config.json\n*.log\n",
	"flake.nix":      "{ }\n",
	"scripts/run.sh": "#!/bin/sh\n",
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Tester", "-c", "user.email=tester@example.invalid"}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

// cloneFixture makes a local "official origin" with one commit on dev, staging and stable, the
// installer's tree at that commit with the files the installer writes, and
// an empty target root. It returns the commit.
func cloneFixture(t *testing.T) (repo, target, revision string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	origin := t.TempDir()
	repo, target = t.TempDir(), t.TempDir()
	runGit(t, origin, "init", "--quiet", "--initial-branch=dev")
	for _, dir := range []string{origin, repo} {
		for rel, data := range officialFiles {
			path := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o644)
			if strings.HasSuffix(rel, ".sh") {
				mode = 0o755
			}
			if err := os.WriteFile(path, []byte(data), mode); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink("flake.nix", filepath.Join(dir, "default.nix")); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, origin, "add", ".")
	runGit(t, origin, "commit", "--quiet", "-m", "first")
	revision = runGit(t, origin, "rev-parse", "HEAD")
	runGit(t, origin, "branch", "staging")
	runGit(t, origin, "branch", "stable")

	local := map[string]string{
		"generated/hardware.nix": "{ }\n",
		"user.config.json":       "{}\n",
		".vm/e2e/disk.qcow2":     "disk",
		"build.log":              "log",
	}
	for rel, data := range local {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previous := officialOrigin
	officialOrigin = origin
	t.Cleanup(func() { officialOrigin = previous })
	withPrivilegedCommand(t, runUnprivileged)
	return repo, target, revision
}

func TestCloneSystemCheckoutClonesInstalledRevision(t *testing.T) {
	repo, target, revision := cloneFixture(t)
	// The origin moved on after the installer was built.
	if err := os.WriteFile(filepath.Join(officialOrigin, "later.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, officialOrigin, "add", ".")
	runGit(t, officialOrigin, "commit", "--quiet", "-m", "later")

	var out strings.Builder
	if err := cloneSystemCheckout(context.Background(), repo, target, "git:"+revision, "stable", &out); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(target, "etc", "gjallar", "system")
	if got := runGit(t, dest, "rev-parse", "HEAD"); got != revision {
		t.Fatalf("HEAD = %s, want %s", got, revision)
	}
	if got := runGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD@{upstream}"); got != "origin/stable" {
		t.Fatalf("upstream = %s", got)
	}
	if got := runGit(t, dest, "remote", "get-url", "origin"); got != officialOrigin {
		t.Fatalf("origin = %s", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "later.nix")); !os.IsNotExist(err) {
		t.Fatalf("later commit checked out: %v", err)
	}
	for rel, mode := range map[string]os.FileMode{
		"generated":              0o755,
		"generated/hardware.nix": 0o644,
		"user.config.json":       0o644,
	} {
		info, err := os.Stat(filepath.Join(dest, rel))
		if err != nil {
			t.Fatalf("%s not copied: %v", rel, err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, want %v", rel, info.Mode().Perm(), mode)
		}
	}
	for _, rel := range []string{".vm", "build.log"} {
		if _, err := os.Lstat(filepath.Join(dest, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s copied into the clone: %v", rel, err)
		}
	}
	if status := runGit(t, dest, "status", "--porcelain"); status != "" {
		t.Fatalf("clone not clean:\n%s", status)
	}
	if !strings.Contains(out.String(), "PASS: /etc/gjallar/system cloned at "+revision[:12]+" (stable)") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCloneSystemCheckoutRefusesRevisionsThatAreNotCommits(t *testing.T) {
	repo, target, revision := cloneFixture(t)
	calls := withPrivilegedCommand(t, nil)
	for _, bad := range []string{"", "git:" + revision + "-dirty", "nar:sha256-AAAA", "git:" + strings.ToUpper(revision)} {
		err := cloneSystemCheckout(context.Background(), repo, target, bad, "stable", io.Discard)
		if err == nil || !strings.Contains(err.Error(), "pushed commit without local changes") {
			t.Fatalf("revision %q: %v", bad, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("privileged calls for a bad revision: %v", *calls)
	}
}

func TestCloneSystemCheckoutRefusesCommitOffTheChannels(t *testing.T) {
	repo, target, _ := cloneFixture(t)
	runGit(t, officialOrigin, "checkout", "--quiet", "-b", "side")
	runGit(t, officialOrigin, "commit", "--quiet", "--allow-empty", "-m", "side")
	side := runGit(t, officialOrigin, "rev-parse", "HEAD")
	runGit(t, officialOrigin, "checkout", "--quiet", "dev")

	err := cloneSystemCheckout(context.Background(), repo, target, side, "stable", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is on no release channel") {
		t.Fatalf("commit off the channels: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, "etc", "gjallar", "system")); !os.IsNotExist(err) {
		t.Fatalf("refused clone left behind: %v", err)
	}
}

func TestCloneSystemCheckoutTracksChannelBehindRevision(t *testing.T) {
	// The installer was built from dev; the user picked stable, which is
	// older. The clone keeps the installed commit and tracks stable.
	repo, target, _ := cloneFixture(t)
	runGit(t, officialOrigin, "checkout", "--quiet", "dev")
	runGit(t, officialOrigin, "commit", "--quiet", "--allow-empty", "-m", "dev only")
	revision := runGit(t, officialOrigin, "rev-parse", "HEAD")

	if err := cloneSystemCheckout(context.Background(), repo, target, revision, "stable", io.Discard); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(target, "etc", "gjallar", "system")
	if got := runGit(t, dest, "rev-parse", "HEAD"); got != revision {
		t.Fatalf("HEAD = %s, want %s", got, revision)
	}
	if got := runGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD@{upstream}"); got != "origin/stable" {
		t.Fatalf("upstream = %s", got)
	}
}

func TestCloneSystemCheckoutRefusesTreeThatDiffers(t *testing.T) {
	cases := map[string]func(repo string) error{
		"flake.nix": func(repo string) error {
			return os.WriteFile(filepath.Join(repo, "flake.nix"), []byte("{ changed = true; }\n"), 0o644)
		},
		"scripts/run.sh": func(repo string) error {
			return os.Chmod(filepath.Join(repo, "scripts", "run.sh"), 0o644)
		},
		"default.nix": func(repo string) error {
			path := filepath.Join(repo, "default.nix")
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink("scripts/run.sh", path)
		},
		"extra.nix": func(repo string) error {
			return os.WriteFile(filepath.Join(repo, "extra.nix"), []byte("{ }\n"), 0o644)
		},
	}
	for rel, change := range cases {
		t.Run(rel, func(t *testing.T) {
			repo, target, revision := cloneFixture(t)
			if err := change(repo); err != nil {
				t.Fatal(err)
			}
			err := cloneSystemCheckout(context.Background(), repo, target, revision, "stable", io.Discard)
			if err == nil || !strings.Contains(err.Error(), "differs from the official commit at "+rel+";") {
				t.Fatalf("changed %s: %v", rel, err)
			}
			if _, err := os.Lstat(filepath.Join(target, "etc", "gjallar", "system")); !os.IsNotExist(err) {
				t.Fatalf("refused clone left behind: %v", err)
			}
		})
	}
}

func TestCloneSystemCheckoutNeverMergesIntoExistingCheckout(t *testing.T) {
	repo, target, revision := cloneFixture(t)
	old := filepath.Join(target, "etc", "gjallar", "system", "keep")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cloneSystemCheckout(context.Background(), repo, target, revision, "stable", io.Discard); err == nil {
		t.Fatal("cloned into an existing checkout")
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "old" {
		t.Fatalf("existing checkout touched: %q, %v", data, err)
	}
}

func TestCloneSystemCheckoutRefusesUnpushedCommit(t *testing.T) {
	// The installer was built from a local commit origin never got.
	repo, target, _ := cloneFixture(t)
	runGit(t, repo, "init", "--quiet", "--initial-branch=dev")
	runGit(t, repo, "commit", "--quiet", "--allow-empty", "-m", "local only")
	local := runGit(t, repo, "rev-parse", "HEAD")

	err := cloneSystemCheckout(context.Background(), repo, target, local, "stable", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "build from a pushed commit") {
		t.Fatalf("unpushed commit: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, "etc", "gjallar", "system")); !os.IsNotExist(err) {
		t.Fatalf("refused clone left behind: %v", err)
	}
}

func TestCheckReleasedRevision(t *testing.T) {
	repo, _, revision := cloneFixture(t)
	if err := checkReleasedRevision(context.Background(), revision); err != nil {
		t.Fatalf("pushed commit: %v", err)
	}

	runGit(t, officialOrigin, "checkout", "--quiet", "-b", "side")
	runGit(t, officialOrigin, "commit", "--quiet", "--allow-empty", "-m", "side")
	side := runGit(t, officialOrigin, "rev-parse", "HEAD")
	runGit(t, officialOrigin, "checkout", "--quiet", "dev")
	runGit(t, repo, "init", "--quiet", "--initial-branch=dev")
	runGit(t, repo, "commit", "--quiet", "--allow-empty", "-m", "local only")
	local := runGit(t, repo, "rev-parse", "HEAD")
	for name, bad := range map[string]string{"side branch": side, "unpushed": local, "dirty": "git:" + revision + "-dirty"} {
		err := checkReleasedRevision(context.Background(), bad)
		if err == nil || !strings.Contains(err.Error(), "pushed commit") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

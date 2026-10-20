package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

func cloneGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Tester", "-c", "user.email=tester@example.invalid"}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

// cloneFromFixture makes a local "official origin" with one commit on dev, staging and stable
// and a user checkout with the machine-local files. All contents are
// SYNTHETIC. It returns the user checkout, a destination that does not
// exist yet, the origin and the commit.
func cloneFromFixture(t *testing.T) (from, dest, origin, revision string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	origin, from = t.TempDir(), t.TempDir()
	dest = filepath.Join(t.TempDir(), "etc", "gjallar", "system")
	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	user.DotfilesDir = "/home/tester/gjallarOS"
	userConfig, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for rel, data := range map[string]string{
		".gitignore":                      "generated/\nuser.config.json\n",
		"flake.nix":                       "{ }\n",
		"scripts/installation/install.sh": "#!/bin/sh\n",
	} {
		for _, dir := range []string{origin, from} {
			writeTestFile(t, filepath.Join(dir, rel), data)
		}
	}
	cloneGit(t, origin, "init", "--quiet", "--initial-branch=dev")
	cloneGit(t, origin, "add", ".")
	cloneGit(t, origin, "commit", "--quiet", "-m", "first")
	revision = cloneGit(t, origin, "rev-parse", "HEAD")
	cloneGit(t, origin, "branch", "staging")
	cloneGit(t, origin, "branch", "stable")
	for rel, data := range map[string]string{
		"generated/hardware.nix":         "{ }\n",
		"generated/oddc/answer.json":     "{}\n",
		"user.config.json":               string(userConfig) + "\n",
		".vm/e2e/disk.qcow2":             "disk",
		"scripts/installation/extra.nix": "{ }\n",
	} {
		writeTestFile(t, filepath.Join(from, rel), data)
	}
	return from, dest, origin, revision
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCloneFromClonesRunningCommit(t *testing.T) {
	from, dest, origin, revision := cloneFromFixture(t)
	writeTestFile(t, filepath.Join(origin, "later.nix"), "{ }\n")
	cloneGit(t, origin, "add", ".")
	cloneGit(t, origin, "commit", "--quiet", "-m", "later")

	copied, err := cloneFrom(context.Background(), from, dest, origin, revision)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"generated/hardware.nix", "generated/oddc/answer.json", "user.config.json"}; !slices.Equal(copied, want) {
		t.Fatalf("copied = %q, want %q", copied, want)
	}
	if got := cloneGit(t, dest, "rev-parse", "HEAD"); got != revision {
		t.Fatalf("HEAD = %s, want %s", got, revision)
	}
	if got := cloneGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD@{upstream}"); got != "origin/stable" {
		t.Fatalf("upstream = %s", got)
	}
	if status := cloneGit(t, dest, "status", "--porcelain"); status != "" {
		t.Fatalf("clone not clean:\n%s", status)
	}
	for rel, mode := range map[string]os.FileMode{
		"generated":                  0o755,
		"generated/oddc":             0o755,
		"generated/hardware.nix":     0o644,
		"generated/oddc/answer.json": 0o644,
		"user.config.json":           0o644,
	} {
		info, err := os.Stat(filepath.Join(dest, rel))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v, %v", rel, info, err)
		}
	}
	for _, rel := range []string{"later.nix", ".vm", "scripts/installation/extra.nix"} {
		if _, err := os.Lstat(filepath.Join(dest, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s in the clone: %v", rel, err)
		}
	}
	user, err := config.Load(filepath.Join(dest, "user.config.json"))
	if err != nil || user.DotfilesDir != "/etc/gjallar/system" {
		t.Fatalf("dotfilesDir = %q, %v", user.DotfilesDir, err)
	}
	if mine, _ := config.Load(filepath.Join(from, "user.config.json")); mine.DotfilesDir != "/home/tester/gjallarOS" {
		t.Fatalf("the user's checkout changed: %q", mine.DotfilesDir)
	}
}

func TestCloneFromRefuses(t *testing.T) {
	cases := map[string]struct {
		change  func(t *testing.T, from, dest, origin string) string
		message string
		keep    bool
	}{
		"dirty revision": {
			change:  func(t *testing.T, _, _, _ string) string { return "-dirty" },
			message: "pushed commit without local changes",
		},
		"existing checkout": {
			change: func(t *testing.T, _, dest, _ string) string {
				writeTestFile(t, filepath.Join(dest, "keep"), "old")
				return ""
			},
			message: "exists already",
			keep:    true,
		},
		"symlinked config": {
			change: func(t *testing.T, from, _, _ string) string {
				path := filepath.Join(from, "user.config.json")
				if err := os.Rename(path, path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("user.config.json.real", path); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			message: "user.config.json is not a regular file",
		},
		"broken config": {
			change: func(t *testing.T, from, _, _ string) string {
				writeTestFile(t, filepath.Join(from, "user.config.json"), `{"username": 3}`)
				return ""
			},
			message: "user.config.json",
		},
		"symlink in generated": {
			change: func(t *testing.T, from, _, _ string) string {
				if err := os.Symlink("/etc/passwd", filepath.Join(from, "generated", "state.nix")); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			message: "generated/state.nix is not a regular file or directory",
		},
		"commit off the channels": {
			change: func(t *testing.T, _, _, origin string) string {
				cloneGit(t, origin, "checkout", "--quiet", "-b", "side")
				cloneGit(t, origin, "commit", "--quiet", "--allow-empty", "-m", "side")
				side := cloneGit(t, origin, "rev-parse", "HEAD")
				cloneGit(t, origin, "checkout", "--quiet", "dev")
				return "=" + side
			},
			message: "is on no release channel",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			from, dest, origin, revision := cloneFromFixture(t)
			switch suffix := c.change(t, from, dest, origin); {
			case strings.HasPrefix(suffix, "="):
				revision = suffix[1:]
			default:
				revision += suffix
			}
			_, err := cloneFrom(context.Background(), from, dest, origin, revision)
			if err == nil || !strings.Contains(err.Error(), c.message) {
				t.Fatalf("err = %v, want %q", err, c.message)
			}
			_, statErr := os.Lstat(dest)
			if c.keep {
				if data, err := os.ReadFile(filepath.Join(dest, "keep")); err != nil || string(data) != "old" {
					t.Fatalf("existing checkout touched: %q, %v", data, err)
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("refused clone left behind: %v", statErr)
			}
		})
	}
}

func TestSystemCloneRefusesUsageAndUsers(t *testing.T) {
	for _, args := range [][]string{nil, {"--from"}, {"--from", ""}, {"/tmp/x"}, {"--from", "/tmp/x", "extra"}} {
		var stderr bytes.Buffer
		if status := runSystem(append([]string{"clone"}, args...), &bytes.Buffer{}, &stderr); status != 2 {
			t.Fatalf("clone %q: status %d", args, status)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("runs as root")
	}
	var stderr bytes.Buffer
	if status := runSystem([]string{"clone", "--from", t.TempDir()}, &bytes.Buffer{}, &stderr); status != 2 ||
		!strings.Contains(stderr.String(), "runs as root") {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
}

func TestSystemMoveListsWhatStaysBehind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as root")
	}
	_, _, origin, revision := cloneFromFixture(t)
	checkout := filepath.Join(t.TempDir(), "gjallarOS")
	cloneGit(t, filepath.Dir(checkout), "clone", "--quiet", origin, checkout)
	writeTestFile(t, filepath.Join(checkout, "mine.nix"), "{ }\n")
	cloneGit(t, checkout, "add", "mine.nix")
	cloneGit(t, checkout, "commit", "--quiet", "-m", "my change")
	writeTestFile(t, filepath.Join(checkout, "flake.nix"), "{ changed = true; }\n")

	pointer := filepath.Join(t.TempDir(), "repository")
	writeTestFile(t, pointer, checkout+"\n")
	oldPointer := installercheck.SystemRepositoryFile
	installercheck.SystemRepositoryFile = pointer
	t.Cleanup(func() { installercheck.SystemRepositoryFile = oldPointer })
	oldSource := buildSourcePath
	buildSourcePath = filepath.Join(t.TempDir(), "build-source.json")
	t.Cleanup(func() { buildSourcePath = oldSource })
	oldFolder := localFolder
	localFolder = func() (string, error) { return "/home/tester/GjallarOS-local", nil }
	t.Cleanup(func() { localFolder = oldFolder })

	writeTestFile(t, buildSourcePath, `{"revision":"`+revision+`-dirty"}`)
	var stdout, stderr bytes.Buffer
	if status := systemMove(nil, &stdout, &stderr); status != 1 || !strings.Contains(stderr.String(), "without local changes") {
		t.Fatalf("dirty build: status %d: %s", status, stderr.String())
	}

	writeTestFile(t, buildSourcePath, `{"revision":"`+revision+`"}`)
	stdout.Reset()
	stderr.Reset()
	if status := systemMove(nil, &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	for _, want := range []string{
		"commit " + cloneGit(t, checkout, "rev-parse", "--short", "HEAD") + " my change",
		"change  M flake.nix",
		"/home/tester/GjallarOS-local",
		"sudo gjallarctl system clone --from " + checkout,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, stdout.String())
		}
	}

	writeTestFile(t, pointer, "/etc/gjallar/system\n")
	stdout.Reset()
	if status := systemMove(nil, &stdout, &stderr); status != 0 || !strings.Contains(stdout.String(), "already builds") {
		t.Fatalf("status %d: %s", status, stdout.String())
	}
}

func TestSystemRebuildNamesMissingCheckout(t *testing.T) {
	old := systemCloneDest
	t.Cleanup(func() { systemCloneDest = old })
	for _, dest := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		systemCloneDest = dest
		err := systemCheckoutMissing()
		if err == nil || !strings.Contains(err.Error(), "is empty or missing") ||
			!strings.Contains(err.Error(), "sudo gjallarctl system clone --from DIR") {
			t.Fatalf("%s: %v", dest, err)
		}
	}
	writeTestFile(t, filepath.Join(systemCloneDest, ".git", "HEAD"), "ref: refs/heads/main\n")
	if err := systemCheckoutMissing(); err != nil {
		t.Fatal(err)
	}
}

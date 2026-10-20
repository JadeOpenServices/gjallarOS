package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// channelCheckout clones a new upstream whose branches dev, staging and
// stable start at one commit, with the checkout on branch and
// releaseChannel set to channel.
func channelCheckout(t *testing.T, branch, channel string) (upstream, checkout string) {
	t.Helper()
	root := t.TempDir()
	upstream, checkout = filepath.Join(root, "up"), filepath.Join(root, "co")
	gitRun(t, root, "init", "-q", "-b", "dev", upstream)
	gitRun(t, upstream, "commit", "-q", "--allow-empty", "-m", "one")
	gitRun(t, upstream, "branch", "staging")
	gitRun(t, upstream, "branch", "stable")
	gitRun(t, upstream, "branch", "main")
	gitRun(t, root, "clone", "-q", "-b", branch, upstream, checkout)
	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	user.ReleaseChannel = channel
	if err := config.WriteAtomic(filepath.Join(checkout, "user.config.json"), user); err != nil {
		t.Fatal(err)
	}
	return upstream, checkout
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestPullCheckoutFastForwards(t *testing.T) {
	upstream, checkout := channelCheckout(t, "stable", "stable")
	gitRun(t, upstream, "checkout", "-q", "stable")
	gitRun(t, upstream, "commit", "-q", "--allow-empty", "-m", "two")

	var stdout, stderr bytes.Buffer
	if code := pullCheckout(checkout, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "->") {
		t.Fatalf("head did not move: %q", stdout.String())
	}

	// A local commit makes the update non-fast-forward: stop, rebuild nothing.
	gitRun(t, checkout, "commit", "-q", "--allow-empty", "-m", "local")
	gitRun(t, upstream, "commit", "-q", "--allow-empty", "-m", "three")
	stdout.Reset()
	stderr.Reset()
	if code := pullCheckout(checkout, &stdout, &stderr); code == 0 {
		t.Fatal("diverged checkout pulled")
	}
	if !strings.Contains(stderr.String(), "nothing rebuilt") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestPullCheckoutFollowsOnlyItsChannel(t *testing.T) {
	upstream, checkout := channelCheckout(t, "stable", "stable")
	gitRun(t, upstream, "commit", "-q", "--allow-empty", "-m", "dev only")
	head := gitOut(t, checkout, "rev-parse", "HEAD")

	var stdout, stderr bytes.Buffer
	if code := pullCheckout(checkout, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := gitOut(t, checkout, "rev-parse", "HEAD"); got != head {
		t.Fatalf("stable checkout took a dev commit: %s", stdout.String())
	}
}

func TestPullCheckoutMovesMainToChannel(t *testing.T) {
	upstream, checkout := channelCheckout(t, "main", "staging")
	gitRun(t, upstream, "checkout", "-q", "staging")
	gitRun(t, upstream, "commit", "-q", "--allow-empty", "-m", "staging two")
	want := gitOut(t, upstream, "rev-parse", "HEAD")

	var stdout, stderr bytes.Buffer
	if code := pullCheckout(checkout, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := gitOut(t, checkout, "symbolic-ref", "--short", "HEAD"); got != "staging" {
		t.Fatalf("branch %q", got)
	}
	if got := gitOut(t, checkout, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/staging" {
		t.Fatalf("upstream %q", got)
	}
	if got := gitOut(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Fatalf("HEAD %s, want %s", got, want)
	}
	if gitOut(t, checkout, "branch", "--list", "main") != "" {
		t.Fatal("old main branch kept")
	}
}

func TestPullCheckoutAheadOfChannelStays(t *testing.T) {
	// dev moved on; the checkout follows dev with a local commit on top,
	// then moves to stable.
	upstream, checkout := channelCheckout(t, "dev", "stable")
	gitRun(t, upstream, "commit", "-q", "--allow-empty", "-m", "dev two")
	gitRun(t, checkout, "pull", "-q", "--ff-only")
	gitRun(t, checkout, "commit", "-q", "--allow-empty", "-m", "local")
	head := gitOut(t, checkout, "rev-parse", "HEAD")

	var stdout, stderr bytes.Buffer
	if code := pullCheckout(checkout, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := gitOut(t, checkout, "rev-parse", "HEAD"); got != head {
		t.Fatal("checkout went back to an older commit")
	}
	if got := gitOut(t, checkout, "symbolic-ref", "--short", "HEAD"); got != "stable" {
		t.Fatalf("branch %q", got)
	}
	if !strings.Contains(stdout.String(), "until stable reaches it") {
		t.Fatalf("stdout %q", stdout.String())
	}
	if gitOut(t, checkout, "branch", "--list", "dev") != "" {
		t.Fatal("old dev branch kept")
	}
}

func TestPullCheckoutRefusesFeatureBranch(t *testing.T) {
	_, checkout := channelCheckout(t, "dev", "dev")
	gitRun(t, checkout, "checkout", "-q", "-b", "feature")

	var stdout, stderr bytes.Buffer
	if code := pullCheckout(checkout, &stdout, &stderr); code == 0 {
		t.Fatal("feature branch updated")
	}
	if !strings.Contains(stderr.String(), "not on the dev channel") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestRebuildHelpListsUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runRebuild([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout.String(), "--update ") {
		t.Fatalf("help: %q", stdout.String())
	}
}

func TestODDCOverrideRevision(t *testing.T) {
	const sha = "9958ddcb93c26721a60f033e21cbb27c8c89325f"
	cases := []struct {
		args    []string
		want    string
		wantErr bool
	}{
		{nil, "", false},
		{[]string{"--show-trace"}, "", false},
		{[]string{"--override-input", "nixpkgs", "github:NixOS/nixpkgs/" + sha}, "", false},
		{[]string{"--override-input", "oddc", "github:JadeOpenServices/oddc/" + sha, "--no-write-lock-file"}, sha, false},
		{[]string{"--override-input", "oddc", "git+https://github.com/JadeOpenServices/oddc?ref=main&rev=" + sha}, sha, false},
		{[]string{"--override-input", "oddc", "github:JadeOpenServices/oddc/main"}, "", true},
		{[]string{"--override-input", "oddc", "path:/home/user/oddc"}, "", true},
	}
	for _, c := range cases {
		got, err := oddcOverrideRevision(c.args)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("oddcOverrideRevision(%q) = %q, %v; want %q, error %v", c.args, got, err, c.want, c.wantErr)
		}
	}
}

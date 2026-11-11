package oddccli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

// upstreamRemote makes a git repository with one commit, points oddc.Remote
// at it and returns the commit.
func upstreamRemote(t *testing.T) string {
	t.Helper()

	remote := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", remote}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "upstream")

	previous := oddc.Remote
	oddc.Remote = remote
	t.Cleanup(func() { oddc.Remote = previous })

	return git("rev-parse", "HEAD")
}

// lockedODDC is the real ODDC commit flake.lock pins.
const lockedODDC = "1f951e41753c2cee31b90ecace5b1e020a0c10db"

// realLock is this repository's flake.lock, resolved before tests chdir.
var realLock, _ = filepath.Abs("../../flake.lock")

// lockedRepo is a checkout with this repository's flake.lock, its ODDC
// input moved to rev.
func lockedRepo(t *testing.T, rev string) string {
	t.Helper()

	lock, err := os.ReadFile(realLock)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lock, []byte(lockedODDC)) {
		t.Fatalf("flake.lock no longer pins ODDC at %s", lockedODDC)
	}
	repo := t.TempDir()
	lock = bytes.ReplaceAll(lock, []byte(lockedODDC), []byte(rev))
	if err := os.WriteFile(filepath.Join(repo, "flake.lock"), lock, 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestCheckUpdateReportsMovedBranch(t *testing.T) {
	head := upstreamRemote(t)
	repo := lockedRepo(t, lockedODDC)

	var out bytes.Buffer
	CheckUpdate(repo, &out)

	want := "ODDC update available: " + short(lockedODDC) + " -> " + short(head)
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "gjallarctl rebuild --hardware-update") {
		t.Fatalf("output = %q, want %q and the apply hint", out.String(), want)
	}
}

func TestCheckUpdateQuietWhenCurrent(t *testing.T) {
	repo := lockedRepo(t, upstreamRemote(t))

	var out bytes.Buffer
	CheckUpdate(repo, &out)
	if out.Len() != 0 {
		t.Fatalf("output = %q, want none", out.String())
	}
}

func TestCheckUpdateQuietOffline(t *testing.T) {
	upstreamRemote(t)
	repo := lockedRepo(t, lockedODDC)
	oddc.Remote = filepath.Join(t.TempDir(), "missing")

	var out bytes.Buffer
	CheckUpdate(repo, &out)
	if out.Len() != 0 {
		t.Fatalf("offline output = %q, want none", out.String())
	}
}

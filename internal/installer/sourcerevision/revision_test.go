package sourcerevision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesExplicitRevision(t *testing.T) {
	t.Setenv(EnvironmentVariable, "test-revision")

	revision, err := Resolve(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if revision != "test-revision" {
		t.Fatalf("revision=%q", revision)
	}
}

func TestResolveRecoveryRequiresTrustedRevision(t *testing.T) {
	t.Setenv(EnvironmentVariable, "")

	_, err := Resolve(t.TempDir(), true)
	if err == nil ||
		!strings.Contains(err.Error(), EnvironmentVariable+" is required") {
		t.Fatalf("error=%v", err)
	}
}

func TestResolveNormalInstallerUsesGitHEAD(t *testing.T) {
	t.Setenv(EnvironmentVariable, "")

	repo := t.TempDir()

	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(repo, "tracked"),
		[]byte("test\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "add", "tracked").Run(); err != nil {
		t.Fatal(err)
	}

	commit := exec.Command(
		"git",
		"-C",
		repo,
		"-c", "user.name=gjallarOS Test",
		"-c", "user.email=test@gjallar.invalid",
		"commit",
		"-m", "test",
	)
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, output)
	}

	expectedRaw, err := exec.Command(
		"git",
		"-C",
		repo,
		"rev-parse",
		"HEAD",
	).Output()
	if err != nil {
		t.Fatal(err)
	}

	revision, err := Resolve(repo, false)
	if err != nil {
		t.Fatal(err)
	}

	expected := strings.TrimSpace(string(expectedRaw))
	if revision != expected {
		t.Fatalf("revision=%q want %q", revision, expected)
	}
}

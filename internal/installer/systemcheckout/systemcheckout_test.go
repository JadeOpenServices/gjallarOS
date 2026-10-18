package systemcheckout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const lockTemplate = `{
  "nodes": {
    "nixpkgs": {"locked": {"rev": "1111111111111111111111111111111111111111"}},
    "oddc": {"locked": {"rev": "%s", "type": "github"}}
  },
  "root": "root",
  "version": 7
}
`

func ownedByMe(t *testing.T) {
	t.Helper()
	uid, parents := rootUID, checkParents
	rootUID, checkParents = uint32(os.Getuid()), false
	t.Cleanup(func() { rootUID, checkParents = uid, parents })
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func lock(rev string) string {
	return strings.Replace(lockTemplate, "%s", rev, 1)
}

// checkout makes a git checkout whose HEAD is on origin/main.
func checkout(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "system")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "init", "-q", "-b", "main")
	write(t, filepath.Join(root, "flake.lock"), lock(strings.Repeat("a", 40)))
	write(t, filepath.Join(root, "flake.nix"), "{ }\n")
	run(t, root, "add", ".")
	run(t, root, "commit", "-q", "-m", "init")
	run(t, root, "remote", "add", "origin", Origins[0])
	run(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	return root
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestActive(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repository")
	write(t, file, Path+"\n")
	if !Active(file) {
		t.Fatal("system checkout not active")
	}
	write(t, file, "/home/someone/gjallarOS\n")
	if Active(file) {
		t.Fatal("home checkout counted as the system checkout")
	}
	if Active(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("missing repository file counted as active")
	}
}

func TestCheckOwnership(t *testing.T) {
	ownedByMe(t)
	root := checkout(t)
	if err := CheckOwnership(root); err != nil {
		t.Fatalf("clean checkout: %v", err)
	}

	if err := os.Symlink("flake.nix", filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnership(root); err != nil {
		t.Fatalf("link inside the checkout: %v", err)
	}
	if err := os.Symlink("../elsewhere", filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnership(root); err == nil || !strings.Contains(err.Error(), "outside the checkout") {
		t.Fatalf("link leaving the checkout: %v", err)
	}
	os.Remove(filepath.Join(root, "outside"))

	if err := os.Chmod(filepath.Join(root, "flake.nix"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnership(root); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("group-writable file: %v", err)
	}
	os.Chmod(filepath.Join(root, "flake.nix"), 0o644)

	rootUID = uint32(os.Getuid()) + 1
	if err := CheckOwnership(root); err == nil || !strings.Contains(err.Error(), "not root") {
		t.Fatalf("checkout owned by someone else: %v", err)
	}
}

func TestCheckOwnershipEmptyOrMissing(t *testing.T) {
	ownedByMe(t)
	dir := t.TempDir()
	if err := CheckOwnership(filepath.Join(dir, "system")); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("missing checkout: %v", err)
	}
	if err := CheckOwnership(dir); err == nil || !strings.Contains(err.Error(), "empty or not a git checkout") {
		t.Fatalf("empty checkout: %v", err)
	}
}

func TestInspect(t *testing.T) {
	ctx := context.Background()
	root := checkout(t)
	source, err := Inspect(ctx, root)
	if err != nil {
		t.Fatalf("clean checkout: %v", err)
	}
	if len(source.Branches) != 1 || source.Branches[0] != "main" || source.ODDC != strings.Repeat("a", 40) || source.ODDCMoved {
		t.Fatalf("source = %+v", source)
	}

	write(t, filepath.Join(root, "flake.lock"), lock(strings.Repeat("b", 40)))
	source, err = Inspect(ctx, root)
	if err != nil || !source.ODDCMoved || source.ODDC != strings.Repeat("b", 40) {
		t.Fatalf("moved oddc pin: %+v, %v", source, err)
	}

	write(t, filepath.Join(root, "flake.lock"), strings.Replace(lock(strings.Repeat("a", 40)), "1111", "2222", 1))
	if _, err := Inspect(ctx, root); err == nil || !strings.Contains(err.Error(), "beyond the oddc pin") {
		t.Fatalf("other lock change: %v", err)
	}
	run(t, root, "checkout", "-q", "flake.lock")

	write(t, filepath.Join(root, "flake.nix"), "{ changed = true; }\n")
	if _, err := Inspect(ctx, root); err == nil || !strings.Contains(err.Error(), "flake.nix") {
		t.Fatalf("edited tracked file: %v", err)
	}
	run(t, root, "checkout", "-q", "flake.nix")

	write(t, filepath.Join(root, "new.nix"), "{ }\n")
	run(t, root, "add", "new.nix")
	run(t, root, "commit", "-q", "-m", "local")
	if _, err := Inspect(ctx, root); err == nil || !strings.Contains(err.Error(), "no branch of the official origin") {
		t.Fatalf("local commit: %v", err)
	}
	run(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	run(t, root, "remote", "set-url", "origin", "https://example.invalid/fork.git")
	if _, err := Inspect(ctx, root); err == nil || !strings.Contains(err.Error(), "not the official") {
		t.Fatalf("foreign origin: %v", err)
	}
}

func TestOfficialODDC(t *testing.T) {
	ctx := context.Background()
	upstream := filepath.Join(t.TempDir(), "oddc")
	if err := os.Mkdir(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, upstream, "init", "-q", "-b", "stable")
	run(t, upstream, "config", "uploadpack.allowFilter", "true")
	write(t, filepath.Join(upstream, "a"), "a\n")
	run(t, upstream, "add", "a")
	run(t, upstream, "commit", "-q", "-m", "stable")
	stable := run(t, upstream, "rev-parse", "HEAD")
	run(t, upstream, "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(upstream, "b"), "b\n")
	run(t, upstream, "add", "b")
	run(t, upstream, "commit", "-q", "-m", "feature")
	feature := run(t, upstream, "rev-parse", "HEAD")

	origin := ODDCOrigin
	ODDCOrigin = "file://" + upstream
	t.Cleanup(func() { ODDCOrigin = origin })
	cache := filepath.Join(t.TempDir(), "cache", "oddc.git")

	if err := OfficialODDC(ctx, cache, stable); err != nil {
		t.Fatalf("stable commit: %v", err)
	}
	if err := OfficialODDC(ctx, cache, feature); err == nil {
		t.Fatal("commit only on a feature branch accepted")
	}
	if err := OfficialODDC(ctx, cache, strings.Repeat("c", 40)); err == nil {
		t.Fatal("unknown commit accepted")
	}
	if err := OfficialODDC(ctx, cache, "stable"); err == nil {
		t.Fatal("branch name accepted as a commit")
	}

	run(t, upstream, "branch", "verify/feature", "feature")
	if err := OfficialODDC(ctx, cache, feature); err != nil {
		t.Fatalf("commit on a verify branch after fetch: %v", err)
	}
}

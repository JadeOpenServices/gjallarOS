package oddc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc/oddctest"
)

// offline points Remote at a repository that is not there, as a rebuild
// without network finds ODDC.
func offline(t *testing.T) {
	previous := Remote
	Remote = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { Remote = previous })
}

// catalogRemote commits the go.mod catalog to a git repository, points
// Remote at it and returns the commit.
func catalogRemote(t *testing.T) string {
	t.Helper()

	remote := t.TempDir()
	if err := os.CopyFS(remote, os.DirFS(oddctest.Catalog(t))); err != nil {
		t.Fatal(err)
	}
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
	git("init", "-q")
	// GitHub serves partial clones of any commit; a local repository
	// only when told to.
	git("config", "uploadpack.allowFilter", "true")
	git("config", "uploadpack.allowAnySHA1InWant", "true")
	git("add", "-A")
	git("commit", "-q", "-m", "catalog")

	previous := Remote
	Remote = remote
	t.Cleanup(func() { Remote = previous })

	return git("rev-parse", "HEAD")
}

func TestRefreshFetchesFromGit(t *testing.T) {
	rev := catalogRemote(t)

	for _, answer := range oddctest.Answers(t) {
		current := portable.DirSource{Root: answer.Root}.Revision()

		before, err := Refresh(answer.Root, rev)
		if err != nil || before != current {
			t.Fatalf("%s: Refresh = %q, %v; want %q", answer.Model, before, err, current)
		}
		if got := AnswerRevision(answer.Root); got != rev {
			t.Errorf("%s: answer at %q, want %q", answer.Model, got, rev)
		}
		if model, err := AnswerModel(answer.Root); err != nil || model != answer.Model {
			t.Errorf("%s: answer holds %q, %v", answer.Model, model, err)
		}
	}
}

func TestRefreshKeepsAnswerReadableUnderStrictUmask(t *testing.T) {
	rev := catalogRemote(t)
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	answer := oddctest.Answers(t)[0]
	if _, err := Refresh(answer.Root, rev); err != nil {
		t.Fatal(err)
	}

	err := filepath.WalkDir(answer.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o644)
		if entry.IsDir() {
			want = 0o755
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s: mode %v, want %v", path, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefreshWithoutAnswerDoesNothing(t *testing.T) {
	offline(t)
	root := filepath.Join(t.TempDir(), "oddc")

	before, err := Refresh(root, "4e311931ed5fac2dca79b99d01e50e620624afa0")
	if err != nil || before != "" {
		t.Fatalf("Refresh = %q, %v; want no answer, no error", before, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Refresh created %s", root)
	}
}

func TestRefreshAtSameRevisionDoesNotAsk(t *testing.T) {
	offline(t)
	for _, answer := range oddctest.Answers(t) {
		current := portable.DirSource{Root: answer.Root}.Revision()

		before, err := Refresh(answer.Root, current)
		if err != nil || before != current {
			t.Fatalf("%s: Refresh = %q, %v; want %q without asking", answer.Model, before, err, current)
		}
	}
}

func TestRefreshOfflineKeepsAnswer(t *testing.T) {
	offline(t)
	for _, answer := range oddctest.Answers(t) {
		current := portable.DirSource{Root: answer.Root}.Revision()
		rev := strings.Repeat("0", 40)

		before, err := Refresh(answer.Root, rev)
		if err == nil {
			t.Fatalf("%s: Refresh offline succeeded", answer.Model)
		}
		if before != current {
			t.Errorf("%s: before = %q, want %q", answer.Model, before, current)
		}
		registry, err := portable.LoadRegistry(answer.Root)
		if err != nil {
			t.Fatalf("%s: answer lost: %v", answer.Model, err)
		}
		if _, ok := registry.Entities[answer.Model]; !ok {
			t.Fatalf("%s: answer no longer holds its model", answer.Model)
		}
	}
}

func TestRefreshRejectsWholeCatalog(t *testing.T) {
	offline(t)
	catalog := t.TempDir()
	if err := os.CopyFS(catalog, os.DirFS(oddctest.Catalog(t))); err != nil {
		t.Fatal(err)
	}

	if _, err := Refresh(catalog, strings.Repeat("0", 40)); err == nil ||
		!strings.Contains(err.Error(), "want 1") {
		t.Fatalf("Refresh of a whole catalog: %v", err)
	}
}

func TestLockedRevisionReadsRepositoryLock(t *testing.T) {
	rev, err := LockedRevision("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(rev) != 40 || strings.Trim(rev, "0123456789abcdef") != "" {
		t.Fatalf("LockedRevision = %q, want a commit", rev)
	}
}

func TestLockedRevisionWithoutODDCInput(t *testing.T) {
	data, err := os.ReadFile("../../../flake.lock")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	broken := strings.Replace(string(data), `"oddc": "oddc"`, `"oddc-gone": "oddc"`, 1)
	if broken == string(data) {
		t.Fatal(`flake.lock has no "oddc": "oddc" root input`)
	}
	if err := os.WriteFile(filepath.Join(repo, "flake.lock"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LockedRevision(repo); err == nil || !strings.Contains(err.Error(), "no oddc input") {
		t.Fatalf("LockedRevision = %v, want no oddc input", err)
	}
}

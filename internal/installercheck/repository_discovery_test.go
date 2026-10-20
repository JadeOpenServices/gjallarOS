package installercheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryRootForTest(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Clean(
		filepath.Join(cwd, "..", ".."),
	)

	resolved, err := ResolveRepository(root)
	if err != nil {
		t.Fatalf(
			"resolve test repository %q: %v",
			root,
			err,
		)
	}

	return resolved
}

func TestDiscoverRepositoryWalksUp(t *testing.T) {
	root := repositoryRootForTest(t)

	got, err := discoverRepository(
		"",
		"",
		"",
		"",
		filepath.Join(root, "internal", "installercheck"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("repository = %q, want %q", got, root)
	}
}

func TestDiscoverRepositoryUsesRememberedCheckout(t *testing.T) {
	root := repositoryRootForTest(t)

	got, err := discoverRepository(
		"",
		"",
		root,
		"/definitely/not/a/repository",
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("repository = %q, want %q", got, root)
	}
}

func TestDiscoverRepositoryEnvironmentWinsRemembered(t *testing.T) {
	root := repositoryRootForTest(t)

	got, err := discoverRepository(
		"",
		root,
		"/definitely/not/a/repository",
		"/definitely/not/a/repository",
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("repository = %q, want %q", got, root)
	}
}

func TestDiscoverRepositoryExplicitWins(t *testing.T) {
	root := repositoryRootForTest(t)

	got, err := discoverRepository(
		root,
		"/definitely/not/a/repository",
		"/also/not/a/repository",
		"/also/not/a/repository",
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("repository = %q, want %q", got, root)
	}
}

func TestDiscoverRepositoryUsesSystemCheckout(t *testing.T) {
	root := repositoryRootForTest(t)

	got, err := discoverRepository(
		"",
		"",
		"/definitely/not/a/repository",
		root+"\n",
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("repository = %q, want %q", got, root)
	}
}

func TestDiscoverRepositoryRememberedWinsSystem(t *testing.T) {
	root := repositoryRootForTest(t)
	other := t.TempDir()

	got, err := discoverRepository("", "", root, other, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("repository = %q, want %q", got, root)
	}
}

func TestRememberRepository(t *testing.T) {
	root := repositoryRootForTest(t)

	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := RememberRepository(root); err != nil {
		t.Fatal(err)
	}

	got, err := readRememberedRepository()
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("remembered = %q, want %q", got, root)
	}

	path := filepath.Join(
		os.Getenv("XDG_STATE_HOME"),
		"gjallarOS",
		"repository",
	)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if strings.TrimSpace(string(data)) != root {
		t.Fatalf("state file = %q", string(data))
	}
}

func TestDiscoverRepositorySystemCheckoutWinsClones(t *testing.T) {
	root := repositoryRootForTest(t)
	clone := t.TempDir()
	old := systemCheckoutPath
	systemCheckoutPath = root
	t.Cleanup(func() { systemCheckoutPath = old })

	got, err := discoverRepository("", clone, clone, root+"\n", clone)
	if err != nil {
		t.Fatal(err)
	}

	if got != root {
		t.Fatalf("repository = %q, want %q", got, root)
	}
}

func TestDiscoverRepositoryMissingSystemCheckoutFails(t *testing.T) {
	root := repositoryRootForTest(t)
	missing := filepath.Join(t.TempDir(), "system")
	old := systemCheckoutPath
	systemCheckoutPath = missing
	t.Cleanup(func() { systemCheckoutPath = old })

	if got, err := discoverRepository("", "", root, missing, root); err == nil {
		t.Fatalf("repository = %q, want an error", got)
	}
}

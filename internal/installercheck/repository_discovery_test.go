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
		filepath.Join(root, "internal", "installercheck"),
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

func TestDiscoverRepositorySystemCheckoutWinsClones(t *testing.T) {
	root := repositoryRootForTest(t)
	clone := t.TempDir()
	old := systemCheckoutPath
	systemCheckoutPath = root
	t.Cleanup(func() { systemCheckoutPath = old })

	got, err := discoverRepository("", clone, root+"\n", clone)
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

	got, err := discoverRepository("", root, missing, root)
	if err == nil || !strings.Contains(err.Error(), "the system checkout "+missing+" is missing or broken") ||
		!strings.Contains(err.Error(), "sudo gjallarctl system clone --from DIR") {
		t.Fatalf("repository = %q, %v", got, err)
	}
}

func TestDiscoverRepositoryNamesStaleSystemPointer(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gjallarOS")
	start := t.TempDir()
	_, err := discoverRepository("", "", missing, start)
	if err == nil || !strings.Contains(err.Error(), "the checkout at "+missing+" that this system was built from is missing or empty") {
		t.Fatalf("err = %v", err)
	}
	if _, err := discoverRepository("", "", "", start); err == nil || !strings.Contains(err.Error(), "no GjallarOS repository found") {
		t.Fatalf("no pointer: %v", err)
	}
}

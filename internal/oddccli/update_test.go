package oddccli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc/oddctest"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

// Real ODDC commits: the one flake.lock pins, and the previous pin.
const (
	lockedODDC = "eab37c4f3de331d3d7b1d5f273c60e59fe6bd0c2"
	parentODDC = "d3b91226fd1afb501092d2e022a2c3ca2e2b84cd"
)

// realLock is this repository's flake.lock, resolved before tests chdir.
var realLock, _ = filepath.Abs("../../flake.lock")

// fakeCommand stands in for a command: it appends its arguments to log,
// then runs then.
func fakeCommand(t *testing.T, log, name, then string) string {
	t.Helper()

	script := filepath.Join(t.TempDir(), name)
	body := "#!/bin/sh\necho " + name + " \"$@\" >> " + log + "\n" + then + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()

	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// testRepo is a GjallarOS checkout whose flake.lock pins ODDC at locked;
// with answer, it holds a real model's answer fetched at lockedODDC.
func testRepo(t *testing.T, locked string, answer bool) string {
	t.Helper()

	repo := t.TempDir()
	for _, file := range []string{"flake.nix", "scripts/installation/install.sh"} {
		copyFile(t, filepath.Join("..", "..", file), filepath.Join(repo, file))
	}

	lock, err := os.ReadFile("../../flake.lock")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lock, []byte(lockedODDC)) {
		t.Fatalf("flake.lock no longer pins ODDC at %s", lockedODDC)
	}
	lock = bytes.ReplaceAll(lock, []byte(lockedODDC), []byte(locked))
	if err := os.WriteFile(filepath.Join(repo, "flake.lock"), lock, 0o644); err != nil {
		t.Fatal(err)
	}

	if answer {
		// The go.mod catalog is ODDC at lockedODDC.
		root := answerDir(repo)
		if err := os.CopyFS(root, os.DirFS(oddctest.Answers(t)[0].Root)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "revision"), []byte(lockedODDC+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return repo
}

// runUpdateWith runs update in repo; the fake nix moves the lock to the
// repository's real flake.lock.
func runUpdateWith(t *testing.T, args ...string) (int, []string, string) {
	t.Helper()

	log := filepath.Join(t.TempDir(), "log")
	oldNix, oldSelf := Nix, Gjallarctl
	Nix = fakeCommand(t, log, "nix", `cp "`+realLock+`" "$5/flake.lock"`)
	Gjallarctl = fakeCommand(t, log, "gjallarctl", "")
	t.Cleanup(func() { Nix, Gjallarctl = oldNix, oldSelf })

	var stdout, stderr bytes.Buffer
	code := Run(append([]string{"update"}, args...), &stdout, &stderr)
	if stderr.Len() > 0 {
		t.Logf("stderr: %s", stderr.String())
	}

	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(data) == 0 {
		lines = nil
	}
	return code, lines, stdout.String()
}

func TestUpdateReportsMovedLock(t *testing.T) {
	repo := testRepo(t, parentODDC, false)

	code, ran, out := runUpdateWith(t, "--repo", repo)
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if want := []string{"nix flake update oddc --flake " + repo}; strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	if want := "ODDC: d3b91226fd1a -> eab37c4f3de3"; !strings.Contains(out, want) {
		t.Fatalf("output %q lacks %q", out, want)
	}
}

func TestUpdateRebuildReportsAnswer(t *testing.T) {
	repo := testRepo(t, lockedODDC, true)

	code, ran, out := runUpdateWith(t, "--repo", repo, "--rebuild")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	want := []string{
		"nix flake update oddc --flake " + repo,
		"gjallarctl rebuild --repo " + repo,
	}
	if strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	for _, line := range []string{"ODDC: already at eab37c4f3de3", "ODDC answer: at eab37c4f3de3"} {
		if !strings.Contains(out, line) {
			t.Fatalf("output %q lacks %q", out, line)
		}
	}
}

func TestUpdateSimplePrintsOneLine(t *testing.T) {
	repo := testRepo(t, lockedODDC, true)

	code, _, out := runUpdateWith(t, "--repo", repo, "--rebuild", "--simple")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if want := "ODDC: already at eab37c4f3de3; ODDC answer: at eab37c4f3de3\n"; out != want {
		t.Fatalf("output %q, want %q", out, want)
	}
}

func TestUpdateDebugShowsModel(t *testing.T) {
	repo := testRepo(t, lockedODDC, true)
	model := oddctest.Answers(t)[0].Model

	code, _, out := runUpdateWith(t, "--repo", repo, "--debug")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	for _, line := range []string{
		"lock before:   " + lockedODDC,
		"answer after:  " + lockedODDC,
		"model:         " + model,
		"answer action: unchanged",
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("output %q lacks %q", out, line)
		}
	}
}

func TestUpdateFindsSystemCheckout(t *testing.T) {
	repo := testRepo(t, lockedODDC, false)

	pointer := filepath.Join(t.TempDir(), "repository")
	if err := os.WriteFile(pointer, []byte(repo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := installercheck.SystemRepositoryFile
	installercheck.SystemRepositoryFile = pointer
	t.Cleanup(func() { installercheck.SystemRepositoryFile = old })
	t.Setenv("GJALLAROS_REPO", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	code, ran, _ := runUpdateWith(t)
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if want := "nix flake update oddc --flake " + repo; len(ran) != 1 || ran[0] != want {
		t.Fatalf("ran %q, want %q", ran, want)
	}
}

func TestUpdateRejectsArguments(t *testing.T) {
	for _, args := range [][]string{{"main"}, {"--simple", "--debug"}} {
		code, ran, _ := runUpdateWith(t, args...)
		if code != 2 || ran != nil {
			t.Fatalf("%q: exit code %d, ran %q; want 2 and nothing run", args, code, ran)
		}
	}
}

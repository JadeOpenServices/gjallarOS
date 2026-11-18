package repocopy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The whole copy with the git on PATH, as this user: an old repository full
// of things root must never run (hooks, core.hooksPath, core.fsmonitor) is
// packed by packScript as the pack unit would, and root's git steps take it
// into a fresh repository. Names, mails and file content are made up here
// (SYNTHETIC); nothing is fetched.

type gitRun struct {
	t   *testing.T
	bin string
	env []string
}

func newGitRun(t *testing.T) gitRun {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not on PATH")
	}
	return gitRun{t: t, bin: bin, env: []string{
		// PATH stays whole so a hook that did run would leave its mark.
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Test Person", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test Person", "GIT_COMMITTER_EMAIL=test@example.invalid",
	}}
}

func (g gitRun) run(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command(g.bin, append([]string{"-C", dir}, args...)...)
	cmd.Env = g.env
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		g.t.Fatalf("git %q: %v\n%s", args, err, errs.String())
	}
	return strings.TrimSpace(out.String())
}

func (g gitRun) ok(dir string, args ...string) bool {
	cmd := exec.Command(g.bin, append([]string{"-C", dir}, args...)...)
	cmd.Env = g.env
	return cmd.Run() == nil
}

// host runs the pack script locally, with the unit's /old and /tmp/r swapped
// for test folders, and root's git steps with GitEnv.
func (g gitRun) host(t *testing.T, objects string, failOnce string) Host {
	failed := false
	return Host{
		Pack: func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
			if args[len(args)-1] != Git || !slices.Contains(args, packScript) {
				return fmt.Errorf("not the pack unit: %q", args)
			}
			script := strings.ReplaceAll(packScript, "/tmp/r", filepath.Join(t.TempDir(), "r"))
			script = strings.ReplaceAll(script, "GIT_OBJECT_DIRECTORY="+OldObjects, "GIT_OBJECT_DIRECTORY="+objects)
			cmd := exec.CommandContext(ctx, "bash", "-c", script, "sh", g.bin)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
			var errs bytes.Buffer
			cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &errs
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%w: %s", err, errs.String())
			}
			return nil
		},
		Git: func(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
			for _, a := range args {
				if !failed && a == failOnce {
					failed = true
					return errors.New("SYNTHETIC failure")
				}
			}
			cmd := exec.CommandContext(ctx, g.bin, args...)
			cmd.Env = slices.Concat([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, GitEnv)
			var errs bytes.Buffer
			cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &errs
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%w: %s", err, errs.String())
			}
			return nil
		},
	}
}

// oldRepo makes the old disk's checkout: main, the dev channel, a feature
// branch, a stash and a detached HEAD, then plants hooks and config that
// would leave a mark in marks if any git step ran them.
func oldRepo(t *testing.T, g gitRun, marks string) (string, map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "old")
	g.run(t.TempDir(), "init", "-q", "-b", "main", dir)
	put(t, filepath.Join(dir, "f"), "SYNTHETIC one\n")
	g.run(dir, "add", "f")
	g.run(dir, "commit", "-q", "-m", "one")
	put(t, filepath.Join(dir, "f"), "SYNTHETIC two\n")
	g.run(dir, "commit", "-q", "-am", "two")
	g.run(dir, "branch", "dev")
	g.run(dir, "checkout", "-q", "-b", "feature/x")
	put(t, filepath.Join(dir, "g"), "SYNTHETIC feature\n")
	g.run(dir, "add", "g")
	g.run(dir, "commit", "-q", "-m", "feature")
	g.run(dir, "tag", "v1")
	put(t, filepath.Join(dir, "f"), "SYNTHETIC work in progress\n")
	g.run(dir, "stash", "-q")
	g.run(dir, "checkout", "-q", "--detach", "main~1")
	shas := map[string]string{}
	for _, r := range []string{"main", "dev", "feature/x", "HEAD", "stash", "v1"} {
		shas[r] = g.run(dir, "rev-parse", r)
	}

	hook := "#!/bin/sh\ntouch " + marks + "/$(basename \"$0\")\n"
	hooks := filepath.Join(t.TempDir(), "hooks")
	for _, h := range []string{"reference-transaction", "post-index-change", "pre-auto-gc", "post-checkout", "fsmonitor"} {
		for _, d := range []string{hooks, filepath.Join(dir, ".git/hooks")} {
			put(t, filepath.Join(d, h), hook)
			if err := os.Chmod(filepath.Join(d, h), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, ".git/config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "[core]\n\thooksPath = %s\n\tfsmonitor = %s/fsmonitor\n[gjallar]\n\toldonly = SYNTHETIC\n", hooks, hooks)
	f.Close()
	return dir, shas
}

// newRepo is the fresh system checkout: its own dev channel and a remote
// ref, the way the install leaves it.
func newRepo(t *testing.T, g gitRun) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "new")
	g.run(t.TempDir(), "init", "-q", "-b", "dev", dir)
	put(t, filepath.Join(dir, "README"), "SYNTHETIC official\n")
	g.run(dir, "add", "README")
	g.run(dir, "commit", "-q", "-m", "official")
	g.run(dir, "update-ref", "refs/remotes/origin/dev", "HEAD")
	return dir
}

func TestGitCopy(t *testing.T) {
	g := newGitRun(t)
	marks := t.TempDir()
	old, shas := oldRepo(t, g, marks)
	nw := newRepo(t, g)
	devBefore := g.run(nw, "rev-parse", "refs/heads/dev")
	configBefore, err := os.ReadFile(filepath.Join(nw, ".git/config"))
	if err != nil {
		t.Fatal(err)
	}
	objects := filepath.Join(old, ".git/objects")

	res, err := Copy(context.Background(), g.host(t, objects, ""), open(t, filepath.Join(old, ".git")), objects, nw, 1<<26)
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Branches: 2, Moved: 2, Stashes: 1}) {
		t.Errorf("%+v", res)
	}
	for ref, want := range map[string]string{
		"refs/heads/main":      shas["main"],
		"refs/heads/feature/x": shas["feature/x"],
		"refs/moved/heads/dev": shas["dev"],
		"refs/moved/HEAD":      shas["HEAD"],
		"refs/heads/dev":       devBefore,
	} {
		if got := g.run(nw, "rev-parse", ref); got != want {
			t.Errorf("%s = %s, want %s", ref, got, want)
		}
	}
	stash := g.run(nw, "for-each-ref", "--format=%(objectname)", "refs/moved/stash/")
	if stash != shas["stash"] {
		t.Errorf("stash %q, want %s", stash, shas["stash"])
	}
	// The stash's index and work-tree commits came along.
	g.run(nw, "cat-file", "-e", shas["stash"]+"^2")
	if g.ok(nw, "rev-parse", "-q", "--verify", "refs/tags/v1") {
		t.Error("tag copied")
	}
	g.run(nw, "fsck", "--strict", "--no-dangling", "--no-progress")

	// Nothing of the old repository ran or carried over.
	if m, _ := os.ReadDir(marks); len(m) != 0 {
		t.Errorf("old hooks ran: %v", m)
	}
	configAfter, err := os.ReadFile(filepath.Join(nw, ".git/config"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(configBefore, configAfter) {
		t.Errorf("new config changed:\n%s", configAfter)
	}
	if _, err := os.Lstat(filepath.Join(nw, ".git/objects/info/alternates")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("alternates in the new repository: %v", err)
	}

	// A second run moves the branches it finds taken; a third finds every
	// name and its moved name taken and writes nothing.
	for i, want := range []Result{{Moved: 2, Skipped: 3}, {Skipped: 5}} {
		res, err = Copy(context.Background(), g.host(t, objects, ""), open(t, filepath.Join(old, ".git")), objects, nw, 1<<26)
		if err != nil || res != want {
			t.Errorf("run %d: %+v, %v", i+2, res, err)
		}
	}
	if got := g.run(nw, "rev-parse", "refs/moved/heads/main"); got != shas["main"] {
		t.Errorf("moved main %s", got)
	}
	if m, _ := os.ReadDir(marks); len(m) != 0 {
		t.Errorf("old hooks ran: %v", m)
	}
}

// A failed check takes back every ref and object the copy wrote.
func TestGitRollback(t *testing.T) {
	g := newGitRun(t)
	old, shas := oldRepo(t, g, t.TempDir())
	objects := filepath.Join(old, ".git/objects")
	for _, step := range []string{"fsck", "repack"} {
		t.Run(step, func(t *testing.T) {
			nw := newRepo(t, g)
			refsBefore := g.run(nw, "for-each-ref")
			res, err := Copy(context.Background(), g.host(t, objects, step), open(t, filepath.Join(old, ".git")), objects, nw, 1<<26)
			if err != nil {
				t.Fatal(err)
			}
			if res.Failed == "" || res.Branches+res.Moved+res.Stashes != 0 {
				t.Errorf("%+v", res)
			}
			if got := g.run(nw, "for-each-ref"); got != refsBefore {
				t.Errorf("refs left:\n%s", got)
			}
			for _, r := range []string{"main", "feature/x", "stash"} {
				if g.ok(nw, "cat-file", "-e", shas[r]) {
					t.Errorf("object of %s left", r)
				}
			}
			g.run(nw, "fsck", "--strict", "--no-progress")
		})
	}

	// Over the budget: index-pack stops, nothing is left.
	nw := newRepo(t, g)
	res, err := Copy(context.Background(), g.host(t, objects, ""), open(t, filepath.Join(old, ".git")), objects, nw, 64)
	if err != nil || res.Failed != "pack" {
		t.Errorf("small budget: %+v, %v", res, err)
	}
	if g.ok(nw, "cat-file", "-e", shas["main"]) {
		t.Error("object left after a pack over the budget")
	}
}

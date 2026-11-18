package repocopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// Made-up hashes (SYNTHETIC): only their shape matters before git runs.
var (
	shaA = strings.Repeat("a", 40)
	shaB = strings.Repeat("b", 40)
	shaC = strings.Repeat("c", 40)
	shaD = strings.Repeat("d", 40)
)

func put(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitDir makes a .git folder of the plain shape Copy takes.
func gitDir(t *testing.T) string {
	t.Helper()
	g := filepath.Join(t.TempDir(), ".git")
	put(t, filepath.Join(g, "config"), "[core]\n\trepositoryformatversion = 0\n\tbare = false\n")
	put(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n")
	if err := os.MkdirAll(filepath.Join(g, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(g, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	return g
}

func open(t *testing.T, p string) *safefs.Dir {
	t.Helper()
	d, err := safefs.OpenDir(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestRefName(t *testing.T) {
	for _, ok := range []string{"main", "feature/x", "v1.2", "a_b-c+d", "a/b/c/d/e/f/g/h/i/j"} {
		if !RefName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{
		"", ".hidden", "a/.b", "a..b", "x.lock", "x.", "a//b", "/a", "a/",
		"-flag", "a b", "a:b", "a~1", "a^", "a?", "a*", "a[", "a\\b", "@{x}", "a\nb",
		"a/b/c/d/e/f/g/h/i/j/k", strings.Repeat("x", 201),
	} {
		if RefName(bad) {
			t.Errorf("%q taken", bad)
		}
	}
}

func TestReadTips(t *testing.T) {
	g := gitDir(t)
	put(t, filepath.Join(g, "packed-refs"), "# pack-refs with: peeled fully-peeled sorted\n"+
		shaA+" refs/heads/main\n"+
		shaB+" refs/heads/old\n"+
		shaC+" refs/tags/v1\n^"+shaD+"\n"+
		shaC+" refs/heads/bad..name\n"+
		"zz refs/heads/short\n")
	// The loose file wins over the packed one.
	put(t, filepath.Join(g, "refs/heads/main"), shaC+"\n")
	put(t, filepath.Join(g, "refs/heads/topic/one"), shaD+"\n")
	put(t, filepath.Join(g, "refs/heads/sym"), "ref: refs/heads/main\n")
	put(t, filepath.Join(g, "HEAD"), shaB+"\n")
	put(t, filepath.Join(g, "logs/refs/stash"),
		strings.Repeat("0", 40)+" "+shaA+" A <a@example.invalid> 1 +0000\tWIP 0\n"+
			shaA+" "+shaB+" A <a@example.invalid> 2 +0000\tWIP 1\n"+
			"broken line\n")
	tips, skipped, refused := readTips(open(t, g))
	if refused != "" {
		t.Fatal(refused)
	}
	want := []tip{
		{branch: "main", sha: shaC},
		{branch: "old", sha: shaB},
		{branch: "topic/one", sha: shaD},
		{ref: "refs/moved/HEAD", sha: shaB},
		{ref: "refs/moved/stash/1", sha: shaB, stash: true},
		{ref: "refs/moved/stash/2", sha: shaA, stash: true},
	}
	if !slices.Equal(tips, want) {
		t.Errorf("tips\n got %+v\nwant %+v", tips, want)
	}
	// bad..name, short, sym, broken stash line.
	if skipped != 4 {
		t.Errorf("skipped %d", skipped)
	}
}

// L63: what root reads on the old disk goes through safefs. A link or a
// FIFO in place of a ref file is skipped, never followed or opened; one in
// place of a folder or of a list file refuses the copy.
func TestReadTipsPlanted(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	put(t, outside, shaA+"\n")
	link := func(target string) func(t *testing.T, p string) {
		return func(t *testing.T, p string) {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			os.RemoveAll(p)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	fifo := func(t *testing.T, p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(p)
		if err := unix.Mkfifo(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		rel     string
		plant   func(t *testing.T, p string)
		refused string
	}{
		"link branch":       {"refs/heads/evil", link(outside), ""},
		"FIFO branch":       {"refs/heads/evil", fifo, ""},
		"link HEAD":         {"HEAD", link(outside), ""},
		"FIFO HEAD":         {"HEAD", fifo, ""},
		"link heads folder": {"refs/heads", link(filepath.Dir(outside)), "refs"},
		"link refs folder":  {"refs", link(filepath.Dir(outside)), "refs"},
		"link sub folder":   {"refs/heads/topic", link(filepath.Dir(outside)), ""},
		"link packed-refs":  {"packed-refs", link(outside), "refs"},
		"FIFO packed-refs":  {"packed-refs", fifo, "refs"},
		"link stash log":    {"logs/refs/stash", link(outside), "refs"},
		"FIFO stash log":    {"logs/refs/stash", fifo, "refs"},
		"link logs folder":  {"logs", link(filepath.Dir(outside)), "refs"},
	} {
		t.Run(name, func(t *testing.T) {
			g := gitDir(t)
			put(t, filepath.Join(g, "refs/heads/main"), shaB+"\n")
			tc.plant(t, filepath.Join(g, tc.rel))
			tips, _, refused := readTips(open(t, g))
			if refused != tc.refused {
				t.Fatalf("refused %q, want %q", refused, tc.refused)
			}
			for _, tp := range tips {
				if tp.sha == shaA {
					t.Errorf("read through the plant: %+v", tp)
				}
			}
		})
	}
}

func TestReadTipsCaps(t *testing.T) {
	g := gitDir(t)
	var b strings.Builder
	for i := range maxRefs + 1 {
		fmt.Fprintf(&b, "%s refs/heads/b%d\n", shaA, i)
	}
	put(t, filepath.Join(g, "packed-refs"), b.String())
	if _, _, refused := readTips(open(t, g)); refused != "refs" {
		t.Errorf("too many packed refs: %q", refused)
	}

	g = gitDir(t)
	put(t, filepath.Join(g, "packed-refs"), strings.Repeat("x", maxPackedRefs+1))
	if _, _, refused := readTips(open(t, g)); refused != "refs" {
		t.Errorf("packed-refs over the cap: %q", refused)
	}

	g = gitDir(t)
	deep := filepath.Join(g, "refs/heads", strings.Repeat("d/", maxRefDepth+1), "x")
	put(t, deep, shaA+"\n")
	if _, _, refused := readTips(open(t, g)); refused != "refs" {
		t.Errorf("too deep: %q", refused)
	}

	g = gitDir(t)
	put(t, filepath.Join(g, "refs/heads/big"), shaA+"\n"+strings.Repeat("x", maxLooseRef))
	tips, skipped, refused := readTips(open(t, g))
	if refused != "" || len(tips) != 0 || skipped != 1 {
		t.Errorf("big loose ref: %v %d %q", tips, skipped, refused)
	}

	g = gitDir(t)
	b.Reset()
	for range maxStashes + 3 {
		fmt.Fprintf(&b, "%s %s A <a@example.invalid> 1 +0000\tWIP\n", shaA, shaB)
	}
	put(t, filepath.Join(g, "logs/refs/stash"), b.String())
	tips, skipped, refused = readTips(open(t, g))
	if refused != "" || len(tips) != maxStashes || skipped != 3 {
		t.Errorf("stash cap: %d tips, %d skipped, %q", len(tips), skipped, refused)
	}
}

func TestShape(t *testing.T) {
	if word := shape(open(t, gitDir(t))); word != "" {
		t.Fatalf("plain repository refused: %q", word)
	}
	outside := t.TempDir()
	for name, tc := range map[string]struct {
		plant func(t *testing.T, g string)
		word  string
	}{
		"alternates":      {func(t *testing.T, g string) { put(t, filepath.Join(g, "objects/info/alternates"), "/elsewhere\n") }, "alternates"},
		"http alternates": {func(t *testing.T, g string) { put(t, filepath.Join(g, "objects/info/http-alternates"), "x\n") }, "alternates"},
		"alternates link": {func(t *testing.T, g string) {
			if err := os.Symlink("/dev/null", filepath.Join(g, "objects/info/alternates")); err != nil {
				t.Fatal(err)
			}
		}, "alternates"},
		"shallow":  {func(t *testing.T, g string) { put(t, filepath.Join(g, "shallow"), shaA+"\n") }, "shallow"},
		"worktree": {func(t *testing.T, g string) { put(t, filepath.Join(g, "commondir"), "../..\n") }, "worktree"},
		"reftable": {func(t *testing.T, g string) { put(t, filepath.Join(g, "reftable/tables.list"), "") }, "reftable"},
		"sha256": {func(t *testing.T, g string) {
			put(t, filepath.Join(g, "config"), "[extensions]\n\tobjectFormat = sha256\n")
		}, "sha256"},
		"refstore": {func(t *testing.T, g string) {
			put(t, filepath.Join(g, "config"), "[extensions]\n\trefStorage = reftable\n")
		}, "reftable"},
		"partial": {func(t *testing.T, g string) {
			put(t, filepath.Join(g, "config"), "[extensions]\n\tpartialClone = origin\n")
		}, "partial"},
		"no config": {func(t *testing.T, g string) { os.Remove(filepath.Join(g, "config")) }, "no-repo"},
		"config link": {func(t *testing.T, g string) {
			os.Remove(filepath.Join(g, "config"))
			if err := os.Symlink(filepath.Join(outside, "c"), filepath.Join(g, "config")); err != nil {
				t.Fatal(err)
			}
		}, "no-repo"},
		"config FIFO": {func(t *testing.T, g string) {
			os.Remove(filepath.Join(g, "config"))
			if err := unix.Mkfifo(filepath.Join(g, "config"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "no-repo"},
		"config too big": {func(t *testing.T, g string) { put(t, filepath.Join(g, "config"), strings.Repeat("#", maxConfig+1)) }, "no-repo"},
		"objects link": {func(t *testing.T, g string) {
			os.RemoveAll(filepath.Join(g, "objects"))
			if err := os.Symlink(outside, filepath.Join(g, "objects")); err != nil {
				t.Fatal(err)
			}
		}, "no-repo"},
		"objects file": {func(t *testing.T, g string) {
			os.RemoveAll(filepath.Join(g, "objects"))
			put(t, filepath.Join(g, "objects"), "")
		}, "no-repo"},
		"info a file": {func(t *testing.T, g string) {
			os.RemoveAll(filepath.Join(g, "objects/info"))
			put(t, filepath.Join(g, "objects/info"), "")
		}, "alternates"},
	} {
		t.Run(name, func(t *testing.T) {
			g := gitDir(t)
			tc.plant(t, g)
			if word := shape(open(t, g)); word != tc.word {
				t.Errorf("got %q, want %q", word, tc.word)
			}
		})
	}
}

func refs(names ...string) refSet {
	s := refSet{names: map[string]bool{}, dirs: map[string]bool{}}
	for _, n := range names {
		s.add(n)
	}
	return s
}

func TestPlace(t *testing.T) {
	existing := refs(
		"refs/heads/dev", "refs/heads/main", "refs/heads/team/x",
		"refs/remotes/origin/dev", "refs/moved/heads/main",
	)
	tips := []tip{
		{branch: "dev", sha: shaA},      // channel: moved
		{branch: "stable", sha: shaA},   // channel even when free: moved
		{branch: "main", sha: shaA},     // taken, moved taken too: skipped
		{branch: "team", sha: shaA},     // a folder of team/x: moved
		{branch: "dev/fix", sha: shaA},  // refs/moved/heads/dev placed above: skipped
		{branch: "mine", sha: shaA},     // free: own name
		{branch: "mine/sub", sha: shaA}, // below the branch just placed: moved
		{branch: "blob", sha: shaB},     // no commit: skipped
		{ref: "refs/moved/HEAD", sha: shaA},
		{ref: "refs/moved/stash/0", sha: shaA, stash: true},
	}
	var res Result
	placed := place(tips, map[string]bool{shaA: true}, existing, &res)
	var got []string
	for _, p := range placed {
		got = append(got, p.ref)
	}
	want := []string{
		"refs/moved/heads/dev", "refs/moved/heads/stable", "refs/moved/heads/team",
		"refs/heads/mine", "refs/moved/heads/mine/sub",
		"refs/moved/HEAD", "refs/moved/stash/0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("placed\n got %q\nwant %q", got, want)
	}
	if res != (Result{Branches: 1, Moved: 5, Stashes: 1, Skipped: 3}) {
		t.Errorf("%+v", res)
	}
	for _, p := range placed {
		if strings.HasPrefix(p.ref, "refs/heads/") && slices.Contains(Channels, strings.TrimPrefix(p.ref, "refs/heads/")) {
			t.Errorf("channel written: %s", p.ref)
		}
	}
}

func TestPackArgs(t *testing.T) {
	args, err := PackArgs("/run/gjallar-move/old/@config/.git/objects")
	if err != nil {
		t.Fatal(err)
	}
	cmd := slices.Index(args, "--")
	if cmd < 0 || !slices.Equal(args[cmd+1:], []string{Shell, "-c", packScript, "sh", Git}) {
		t.Fatalf("command %q", args)
	}
	props := args[:cmd]
	for _, want := range []string{
		"--unit=" + storecopy.PackUnit, "--pipe", "--wait",
		"--property=DynamicUser=yes", "--property=PrivateNetwork=yes",
		"--property=BindReadOnlyPaths=/run/gjallar-move/old/@config/.git/objects:" + OldObjects,
	} {
		if !slices.Contains(props, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, a := range props {
		if strings.HasPrefix(a, "--property=BindPaths=") {
			t.Errorf("the pack unit gets a writable folder: %q", a)
		}
	}
	if _, err := PackArgs("/o:/etc"); err == nil {
		t.Error("folder with a colon accepted")
	}
	// The script reads no config of the old repository and runs no git
	// command in it.
	for _, bad := range []string{"--git-dir", "/old/..", "config", "hooks"} {
		if strings.Contains(strings.ReplaceAll(packScript, "GIT_CONFIG_", ""), bad) {
			t.Errorf("pack script has %q", bad)
		}
	}
}

// fakeHost records root's git steps and fails the one named.
type fakeHost struct {
	fail    string // the git subcommand to fail
	failAll bool   // fail every call of it, the rollback's too
	packOut string
	packErr error
	steps   []string
	stdin   map[string][]string
	failed  int
	cat     string // what cat-file prints
}

func (f *fakeHost) host() Host {
	f.stdin = map[string][]string{}
	return Host{
		Pack: func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
			io.Copy(io.Discard, stdin)
			if _, err := io.WriteString(stdout, f.packOut); err != nil {
				return err
			}
			return f.packErr
		},
		Git: func(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
			i := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })
			if i < 0 || args[i] != "--git-dir=/new/repo/.git" {
				return fmt.Errorf("git without the new repository: %q", args)
			}
			step := args[i+1]
			f.steps = append(f.steps, step)
			if stdin != nil {
				in, _ := io.ReadAll(stdin)
				f.stdin[step] = append(f.stdin[step], string(in))
			}
			if step == f.fail && (f.failed == 0 || f.failAll) {
				f.failed++
				return errors.New("SYNTHETIC failure")
			}
			switch step {
			case "for-each-ref":
				io.WriteString(stdout, "refs/heads/dev\nrefs/remotes/origin/dev\n")
			case "cat-file":
				io.WriteString(stdout, f.cat)
			case "index-pack":
				io.Copy(io.Discard, stdin)
			}
			return nil
		},
	}
}

func copyWith(t *testing.T, f *fakeHost, budget int64) (Result, error) {
	t.Helper()
	g := gitDir(t)
	put(t, filepath.Join(g, "refs/heads/dev"), shaA+"\n")
	put(t, filepath.Join(g, "refs/heads/mine"), shaB+"\n")
	if f.cat == "" {
		f.cat = shaA + " commit 200\n" + shaB + " commit 210\n"
	}
	if f.packOut == "" {
		f.packOut = "PACK SYNTHETIC"
	}
	return Copy(context.Background(), f.host(), open(t, g), "/old/objects", "/new/repo", budget)
}

func TestCopyFake(t *testing.T) {
	f := &fakeHost{}
	res, err := copyWith(t, f, 1<<20)
	if err != nil || res != (Result{Branches: 1, Moved: 1}) {
		t.Fatalf("%+v, %v", res, err)
	}
	if want := []string{"for-each-ref", "index-pack", "cat-file", "update-ref", "fsck", "repack"}; !slices.Equal(f.steps, want) {
		t.Errorf("steps %q", f.steps)
	}
	if got := f.stdin["update-ref"]; len(got) != 1 || got[0] != "create refs/moved/heads/dev "+shaA+"\ncreate refs/heads/mine "+shaB+"\n" {
		t.Errorf("update-ref %q", got)
	}
}

func TestCopyRollback(t *testing.T) {
	for _, tc := range []struct {
		fail    string
		failed  string
		deletes bool
	}{
		{"index-pack", "pack", false},
		{"cat-file", "kind", false},
		{"update-ref", "refs", false},
		{"fsck", "check", true},
		{"repack", "repack", true},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			f := &fakeHost{fail: tc.fail}
			res, err := copyWith(t, f, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if res.Failed != tc.failed || res.Branches+res.Moved+res.Stashes != 0 || res.Skipped != 2 {
				t.Errorf("%+v", res)
			}
			n := len(f.steps)
			if n < 2 || f.steps[n-2] != "repack" || f.steps[n-1] != "prune" {
				t.Errorf("no repack and prune at the end: %q", f.steps)
			}
			dels := f.stdin["update-ref"]
			if tc.deletes {
				want := "delete refs/moved/heads/dev " + shaA + "\ndelete refs/heads/mine " + shaB + "\n"
				if len(dels) != 2 || dels[1] != want {
					t.Errorf("rollback update-ref %q", dels)
				}
			} else if len(dels) > 1 {
				t.Errorf("refs deleted that were never written: %q", dels)
			}
		})
	}

	// A rollback that fails is the only hard error.
	f := &fakeHost{fail: "repack", failAll: true}
	if _, err := copyWith(t, f, 1<<20); err == nil {
		t.Error("failed rollback not reported")
	}
}

func TestCopyPackFails(t *testing.T) {
	f := &fakeHost{packErr: errors.New("SYNTHETIC unit failure")}
	if res, err := copyWith(t, f, 1<<20); err != nil || res.Failed != "pack" {
		t.Errorf("%+v, %v", res, err)
	}
	f = &fakeHost{packOut: strings.Repeat("p", 101)}
	if res, err := copyWith(t, f, 100); err != nil || res.Failed != "pack" {
		t.Errorf("over budget: %+v, %v", res, err)
	}
	f = &fakeHost{cat: shaA + " missing\n" + shaB + " blob 3\n"}
	res, err := copyWith(t, f, 1<<20)
	if err != nil || res != (Result{Skipped: 2}) || slices.Contains(f.steps, "update-ref") {
		t.Errorf("no commits: %+v, %v, %q", res, err, f.steps)
	}
	if n := len(f.steps); f.steps[n-1] != "prune" {
		t.Errorf("unused pack not dropped: %q", f.steps)
	}
}

func TestCopyRefused(t *testing.T) {
	g := gitDir(t)
	put(t, filepath.Join(g, "shallow"), shaA+"\n")
	f := &fakeHost{}
	res, err := Copy(context.Background(), f.host(), open(t, g), "/old/objects", "/new/repo", 1<<20)
	if err != nil || res.Refused != "shallow" || len(f.steps) != 0 {
		t.Errorf("%+v, %v, %q", res, err, f.steps)
	}
	if _, err := Copy(context.Background(), f.host(), open(t, gitDir(t)), "/o", "/new/repo", 0); err == nil {
		t.Error("no budget accepted")
	}
	// No refs: nothing runs.
	res, err = Copy(context.Background(), f.host(), open(t, gitDir(t)), "/o", "/new/repo", 1<<20)
	if err != nil || res != (Result{}) || len(f.steps) != 0 {
		t.Errorf("empty: %+v, %v, %q", res, err, f.steps)
	}
}

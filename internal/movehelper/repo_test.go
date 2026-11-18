package movehelper

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/repocopy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// The helper does not link systemcheckout (it starts programs); the new
// checkout it copies into must still be the one the system uses.
func TestNewCheckoutIsSystemCheckout(t *testing.T) {
	if "/"+newCheckout != systemcheckout.Path {
		t.Errorf("newCheckout %q, systemcheckout.Path %q", newCheckout, systemcheckout.Path)
	}
}

func TestRepoNeedsBudgetAndOpenDisk(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	if _, err := run(t, base, `{"op":"repo"}`); err == nil {
		t.Error("repo without a budget accepted")
	}
	out, err := run(t, base, `{"op":"repo","budget":1048576}`)
	var res RefusalResult
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Refusal == nil || res.Refusal.Reason != "not-open" {
		t.Errorf("repo with nothing open: %q, %v", out, err)
	}
}

// config hands out nothing while no old disk is open.
func TestConfigNeedsOpenDisk(t *testing.T) {
	out, err := run(t, filepath.Join(t.TempDir(), "gjallar-move"), `{"op":"config"}`)
	var res RefusalResult
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Refusal == nil || res.Refusal.Reason != "not-open" {
		t.Errorf("config with nothing open: %q, %v", out, err)
	}
}

func openTest(t *testing.T, dir string) *safefs.Dir {
	t.Helper()
	d, err := safefs.OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// A link or FIFO anywhere on the way to the old git folder is refused, never
// followed: the pack unit would bind what it points at.
func TestOldGitDirPlanted(t *testing.T) {
	top := t.TempDir()
	mkdirs(t, filepath.Join(top, "@config/.git"))
	d, refused := oldGitDir(openTest(t, top))
	if refused != "" {
		t.Fatalf("plain checkout refused: %s", refused)
	}
	d.Close()

	if _, refused := oldGitDir(openTest(t, t.TempDir())); refused != "no-repo" {
		t.Errorf("no @config: %q", refused)
	}
	elsewhere := t.TempDir()
	mkdirs(t, filepath.Join(elsewhere, ".git"))
	for name, plant := range map[string]func(top string) error{
		"link @config": func(top string) error { return os.Symlink(elsewhere, filepath.Join(top, "@config")) },
		"link .git": func(top string) error {
			mkdirs(t, filepath.Join(top, "@config"))
			return os.Symlink(filepath.Join(elsewhere, ".git"), filepath.Join(top, "@config/.git"))
		},
		"fifo .git": func(top string) error {
			mkdirs(t, filepath.Join(top, "@config"))
			return unix.Mkfifo(filepath.Join(top, "@config/.git"), 0o644)
		},
		"file .git": func(top string) error {
			mkdirs(t, filepath.Join(top, "@config"))
			return os.WriteFile(filepath.Join(top, "@config/.git"), []byte("gitdir: /etc\n"), 0o644)
		},
	} {
		top := t.TempDir()
		if err := plant(top); err != nil {
			t.Fatal(err)
		}
		if d, refused := oldGitDir(openTest(t, top)); refused != "path" {
			if d != nil {
				d.Close()
			}
			t.Errorf("%s: %q", name, refused)
		}
	}
}

// asRoot makes the test user stand in for root as the new checkout's owner.
func asRoot(t *testing.T) {
	t.Helper()
	was := rootUID
	rootUID = uint32(os.Getuid())
	t.Cleanup(func() { rootUID = was })
}

// Root's git only runs in a plain new checkout folder, and only on the
// mounted @config: a plain folder at the checkout path is refused. The
// mounted case runs in TestNewRootMounts.
func TestNewGitDirPlanted(t *testing.T) {
	asRoot(t)
	root := t.TempDir()
	mkdirs(t, filepath.Join(root, newCheckout, ".git/objects/pack"), filepath.Join(root, newCheckout, ".git/refs/heads"))
	if err := os.WriteFile(filepath.Join(root, newCheckout, ".git/HEAD"), []byte("ref: refs/heads/dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := newGitDir(openTest(t, root)); !errors.Is(err, safefs.ErrNotMount) {
		t.Fatalf("plain checkout folder: got %v, want ErrNotMount", err)
	}
	if err := checkoutGitDir(openTest(t, filepath.Join(root, newCheckout))); err != nil {
		t.Fatal(err)
	}
	if err := newGitDir(openTest(t, t.TempDir())); err == nil {
		t.Error("no checkout accepted")
	}
	elsewhere := t.TempDir()
	mkdirs(t, filepath.Join(elsewhere, ".git"))
	for _, at := range []string{"etc", "etc/gjallar", newCheckout} {
		root := t.TempDir()
		mkdirs(t, filepath.Join(root, filepath.Dir(at)))
		if err := os.Symlink(elsewhere, filepath.Join(root, at)); err != nil {
			t.Fatal(err)
		}
		if err := newGitDir(openTest(t, root)); err == nil || errors.Is(err, safefs.ErrNotMount) {
			t.Errorf("link at %s: %v", at, err)
		}
	}
	co := t.TempDir()
	if err := os.Symlink(filepath.Join(elsewhere, ".git"), filepath.Join(co, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := checkoutGitDir(openTest(t, co)); err == nil {
		t.Error("link at .git followed")
	}
}

// L139: anything in the new .git that is not root's plain file or folder,
// or that sends git elsewhere, stops the copy before root's git runs.
func TestNewGitDirContent(t *testing.T) {
	asRoot(t)
	git := func(t *testing.T) (string, string) {
		co := filepath.Join(t.TempDir(), "system")
		g := filepath.Join(co, ".git")
		mkdirs(t, filepath.Join(g, "objects/info"), filepath.Join(g, "info"), filepath.Join(g, "refs/heads"))
		return co, g
	}
	write := func(p string) error { return os.WriteFile(p, []byte("SYNTHETIC\n"), 0o644) }
	if root, _ := git(t); checkoutGitDir(openTest(t, root)) != nil {
		t.Fatal("plain checkout refused")
	}
	for name, plant := range map[string]func(g string) error{
		"commondir":       func(g string) error { return write(filepath.Join(g, "commondir")) },
		"alternates":      func(g string) error { return write(filepath.Join(g, "objects/info/alternates")) },
		"http-alternates": func(g string) error { return write(filepath.Join(g, "objects/info/http-alternates")) },
		"grafts":          func(g string) error { return write(filepath.Join(g, "info/grafts")) },
		"alternates link": func(g string) error {
			return os.Symlink("/nowhere", filepath.Join(g, "objects/info/alternates"))
		},
		"link deep":   func(g string) error { return os.Symlink("/etc", filepath.Join(g, "refs/heads/x")) },
		"fifo":        func(g string) error { return unix.Mkfifo(filepath.Join(g, "refs/heads/x"), 0o644) },
		"group write": func(g string) error { return os.Chmod(filepath.Join(g, "refs"), 0o775) },
		"other write file": func(g string) error {
			if err := write(filepath.Join(g, "packed-refs")); err != nil {
				return err
			}
			return os.Chmod(filepath.Join(g, "packed-refs"), 0o646)
		},
		"checkout writable": func(g string) error { return os.Chmod(filepath.Dir(g), 0o777) },
	} {
		root, g := git(t)
		if err := plant(g); err != nil {
			t.Fatal(err)
		}
		if err := checkoutGitDir(openTest(t, root)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// Someone else's file: the test user is not "root" here.
	root, g := git(t)
	if err := write(filepath.Join(g, "packed-refs")); err != nil {
		t.Fatal(err)
	}
	rootUID = uint32(os.Getuid()) + 1
	if err := checkoutGitDir(openTest(t, root)); err == nil {
		t.Error("checkout of another user accepted")
	}
}

// L141: root's git steps never start git directly; each runs in the git
// unit with only the new checkout's git folder writable.
func TestGitUnitArgs(t *testing.T) {
	base := "/run/gjallar-move"
	args, err := gitUnitArgs(base, []string{"--git-dir=x", "fsck"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := slices.Index(args, "--")
	if cmd < 0 || !slices.Equal(args[cmd+1:], []string{repocopy.Git, "--git-dir=x", "fsck"}) {
		t.Fatalf("command %q", args)
	}
	if !slices.Contains(args[:cmd], "--property=ReadWritePaths="+filepath.Join(base, New, newCheckout, ".git")) ||
		!slices.Contains(args[:cmd], "--unit="+storecopy.GitUnit) {
		t.Errorf("%q", args)
	}
	for _, e := range repocopy.GitEnv {
		if !slices.Contains(args[:cmd], "--setenv="+e) {
			t.Errorf("missing env %q", e)
		}
	}
}

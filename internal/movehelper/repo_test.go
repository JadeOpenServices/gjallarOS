package movehelper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// Root's git only runs in a plain new checkout folder.
func TestNewGitDirPlanted(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, filepath.Join(root, newCheckout, ".git"))
	if err := newGitDir(openTest(t, root)); err != nil {
		t.Fatal(err)
	}
	if err := newGitDir(openTest(t, t.TempDir())); err == nil {
		t.Error("no checkout accepted")
	}
	elsewhere := t.TempDir()
	mkdirs(t, filepath.Join(elsewhere, ".git"))
	for _, at := range []string{"etc", "etc/gjallar", newCheckout, newCheckout + "/.git"} {
		root := t.TempDir()
		mkdirs(t, filepath.Join(root, filepath.Dir(at)))
		target := elsewhere
		if strings.HasSuffix(at, ".git") {
			target = filepath.Join(elsewhere, ".git")
		}
		if err := os.Symlink(target, filepath.Join(root, at)); err != nil {
			t.Fatal(err)
		}
		if err := newGitDir(openTest(t, root)); err == nil {
			t.Errorf("link at %s followed", at)
		}
	}
}

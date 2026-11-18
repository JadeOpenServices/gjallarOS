package systemcheckout

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// cloned is a system checkout made the way Clone makes one, with a
// submodule, from local origins. originOK and moduleOK accept exactly
// those origins.
type cloned struct {
	root, origin, module string
}

func (c cloned) check() error {
	return checkGit(c.root,
		func(u string) bool { return u == c.origin },
		func(u string) bool { return u == c.module })
}

func clone(t *testing.T) cloned {
	t.Helper()
	top := t.TempDir()
	module := filepath.Join(top, "monique")
	run(t, top, "init", "-q", "-b", "main", module)
	write(t, filepath.Join(module, "README"), "SYNTHETIC\n")
	run(t, module, "add", ".")
	run(t, module, "commit", "-q", "-m", "init")

	origin := filepath.Join(top, "origin")
	run(t, top, "init", "-q", "-b", "stable", origin)
	write(t, filepath.Join(origin, "flake.nix"), "{ }\n")
	run(t, origin, "add", ".")
	run(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", module, "pkgs/monique")
	run(t, origin, "commit", "-q", "-m", "init")
	commit := run(t, origin, "rev-parse", "HEAD")

	root := filepath.Join(top, "system")
	run(t, top, "clone", "--quiet", "--no-checkout", "--", origin, root)
	run(t, root, "checkout", "--quiet", "--no-track", "-B", "stable", commit)
	run(t, root, "branch", "--quiet", "--set-upstream-to=refs/remotes/origin/stable", "stable")
	run(t, root, "-c", "protocol.file.allow=always", "submodule", "update", "--quiet", "--init", "--recursive")
	return cloned{root, origin, module}
}

// What Clone and submodule update write passes; the official origin rule
// still refuses a local one.
func TestCheckGitAcceptsClone(t *testing.T) {
	c := clone(t)
	if err := c.check(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.root, ".git/modules/pkgs/monique/HEAD")); err != nil {
		t.Fatalf("no submodule git folder: %v", err)
	}
	if err := CheckGit(c.root); err == nil || !strings.Contains(err.Error(), "remote.origin.url") {
		t.Errorf("local origin accepted by CheckGit: %v", err)
	}
}

// L147: a .git that makes root's git run a program, read from elsewhere or
// fetch from elsewhere is refused before git runs.
func TestCheckGitPlanted(t *testing.T) {
	top := func(c cloned) string { return filepath.Join(c.root, ".git") }
	mod := func(c cloned) string { return filepath.Join(c.root, ".git/modules/pkgs/monique") }
	set := func(dir func(cloned) string, kv ...string) func(*testing.T, cloned) {
		return func(t *testing.T, c cloned) {
			for i := 0; i < len(kv); i += 2 {
				run(t, c.root, "config", "--file", filepath.Join(dir(c), "config"), kv[i], kv[i+1])
			}
		}
	}
	file := func(dir func(cloned) string, name string) func(*testing.T, cloned) {
		return func(t *testing.T, c cloned) {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir(c), name)), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir(c), name), "SYNTHETIC\n")
		}
	}
	for name, plant := range map[string]func(*testing.T, cloned){
		"ext origin":      set(top, "remote.origin.url", "ext::sh -c true", "protocol.ext.allow", "always"),
		"attributes":      file(top, "info/attributes"),
		"filter driver":   set(top, "filter.x.smudge", "true"),
		"ssh command":     set(top, "core.sshCommand", "true"),
		"credential":      set(top, "credential.helper", "true"),
		"askpass":         set(top, "core.askPass", "true"),
		"insteadOf":       set(top, "url.https://example.invalid/.insteadOf", "x"),
		"include":         set(top, "include.path", "/nowhere"),
		"upload-pack":     set(top, "remote.origin.uploadpack", "true"),
		"vcs":             set(top, "remote.origin.vcs", "x"),
		"pushurl":         set(top, "remote.origin.pushurl", "https://example.invalid/x.git"),
		"fetch refspec":   set(top, "remote.origin.fetch", "+refs/*:refs/*"),
		"submodule cmd":   set(top, "submodule.pkgs/monique.update", "!true"),
		"submodule url":   set(top, "submodule.pkgs/monique.url", "ext::sh -c true"),
		"branch merge":    set(top, "branch.stable.merge", "refs/heads/other"),
		"top worktree":    set(top, "core.worktree", "/"),
		"bare":            set(top, "core.bare", "true"),
		"hooks path":      set(top, "core.hooksPath", "/tmp"),
		"fsmonitor":       set(top, "core.fsmonitor", "true"),
		"pager":           set(top, "core.pager", "true"),
		"format version":  set(top, "core.repositoryformatversion", "1", "extensions.worktreeConfig", "true"),
		"alternates":      file(top, "objects/info/alternates"),
		"http-alternates": file(top, "objects/info/http-alternates"),
		"commondir":       file(top, "commondir"),
		"grafts":          file(top, "info/grafts"),
		"config.worktree": file(top, "config.worktree"),
		"module worktree": set(mod, "core.worktree", "../../../../.."),
		"module origin":   set(mod, "remote.origin.url", "ext::sh -c true"),
		"module ssh":      set(mod, "core.sshCommand", "true"),
		"module attrs":    file(mod, "info/attributes"),
		"twice": func(t *testing.T, c cloned) {
			run(t, c.root, "config", "--file", filepath.Join(top(c), "config"), "--add", "remote.origin.url", c.origin)
		},
		"config link": func(t *testing.T, c cloned) {
			cfg := filepath.Join(top(c), "config")
			if err := os.Rename(cfg, cfg+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(cfg+".real", cfg); err != nil {
				t.Fatal(err)
			}
		},
		"config fifo": func(t *testing.T, c cloned) {
			cfg := filepath.Join(top(c), "config")
			if err := os.Remove(cfg); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(cfg, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"config too large": func(t *testing.T, c cloned) {
			f, err := os.OpenFile(filepath.Join(top(c), "config"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString("#" + strings.Repeat("x", MaxGitConfig) + "\n"); err != nil {
				t.Fatal(err)
			}
		},
		".git link": func(t *testing.T, c cloned) {
			if err := os.Rename(top(c), top(c)+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(top(c)+".real", top(c)); err != nil {
				t.Fatal(err)
			}
		},
		"modules link": func(t *testing.T, c cloned) {
			m := filepath.Join(top(c), "modules")
			if err := os.Rename(m, m+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(m+".real", m); err != nil {
				t.Fatal(err)
			}
		},
		"module .git elsewhere": func(t *testing.T, c cloned) {
			write(t, filepath.Join(c.root, "pkgs/monique/.git"), "gitdir: "+filepath.Join(c.module, ".git")+"\n")
		},
		"module .git relative elsewhere": func(t *testing.T, c cloned) {
			write(t, filepath.Join(c.root, "pkgs/monique/.git"), "gitdir: ../../../monique/.git\n")
		},
		".git folder in tree": func(t *testing.T, c cloned) {
			if err := os.MkdirAll(filepath.Join(c.root, "pkgs/other/.git"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		".git file in tree": func(t *testing.T, c cloned) {
			if err := os.MkdirAll(filepath.Join(c.root, "pkgs/other"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(c.root, "pkgs/other/.git"), "gitdir: ../../.git/modules/pkgs/monique\n")
		},
		".git link in tree": func(t *testing.T, c cloned) {
			if err := os.Symlink(top(c), filepath.Join(c.root, "pkgs/.git")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := clone(t)
			plant(t, c)
			if err := c.check(); err == nil {
				t.Errorf("%s accepted", name)
			}
		})
	}
}

func TestHTTPSURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://github.com/vardstein/monique.git":            true,
		"https://user@github.com/x.git":                       false,
		"https://user:secret@github.com/x.git":                false,
		"http://github.com/x.git":                             false,
		"ssh://git@github.com/x.git":                          false,
		"git@github.com:x.git":                                false,
		"ext::sh -c true":                                     false,
		"file:///etc":                                         false,
		"/srv/x.git":                                          false,
		"https:///x.git":                                      false,
		"https://github.com/x.git\nhttps://example.invalid/x": false,
		"https://github.com/x .git":                           false,
		"https:github.com/x.git":                              false,
	} {
		if httpsURL(raw) != want {
			t.Errorf("httpsURL(%q) = %v", raw, !want)
		}
	}
}

// Root's git takes nothing from the caller's environment that changes what
// it reads.
func TestGitEnv(t *testing.T) {
	env := gitEnv([]string{
		"PATH=/bin", "HOME=/home/someone", "XDG_CONFIG_HOME=/home/someone/.config",
		"GIT_CONFIG_GLOBAL=/home/someone/x", "GIT_CONFIG_COUNT=1", "GIT_DIR=/x",
		"GIT_EXEC_PATH=/x", "GIT_SSH_COMMAND=true", "GIT_ASKPASS=true", "LANG=C",
	}, "/root")
	want := []string{"PATH=/bin", "LANG=C", "HOME=/root", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0"}
	if !slices.Equal(env, want) {
		t.Errorf("env %q, want %q", env, want)
	}
}

// A hook planted in the checkout never runs in root's git.
func TestGitCommandRunsNoHook(t *testing.T) {
	c := clone(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(c.root, ".git/hooks/post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := GitCommand(context.Background(), c.root, "checkout", "--quiet", "-B", "other").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("planted hook ran")
	}
	// The same checkout runs the hook in a plain git: the plant is real.
	run(t, c.root, "checkout", "--quiet", "-B", "third")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("hook plant did not work: %v", err)
	}
}

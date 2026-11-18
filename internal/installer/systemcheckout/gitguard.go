package systemcheckout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// GitCommand is root's git in the system checkout (L146, L147). It never
// runs a hook or an fsmonitor program, never detaches a background gc, and
// takes nothing from the caller's environment that changes what git reads:
// no GIT_* variable, no XDG_CONFIG_HOME, and HOME is the home of the user
// git runs as, so root reads only root's own global config. dir "" runs
// git without -C.
func GitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "gc.autoDetach=false",
		"-c", "maintenance.autoDetach=false",
	}
	if dir != "" {
		full = append([]string{"-C", dir}, full...)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = gitEnv(os.Environ(), ownHome())
	return cmd
}

func gitEnv(environ []string, home string) []string {
	env := make([]string, 0, len(environ)+3)
	for _, e := range environ {
		name, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(name, "GIT_") || name == "HOME" || name == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, e)
	}
	// A missing commit must not be fetched on demand from the promisor:
	// GitHub would hand out a fork commit.
	return append(env, "HOME="+home, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
}

// ownHome is the passwd home of the effective user; /var/empty, where git
// finds no config, when there is none.
func ownHome() string {
	if u, err := user.LookupId(strconv.Itoa(os.Geteuid())); err == nil && filepath.IsAbs(u.HomeDir) {
		return u.HomeDir
	}
	return "/var/empty"
}

// MaxGitConfig caps a git config file CheckGit reads.
const MaxGitConfig = 64 << 10

// CheckGit refuses a system checkout whose git folder could make root's
// git run a program, read objects, refs or settings from elsewhere, or
// send its fetch somewhere else (L147): a moved or restored .git keeps the
// config of the disk it came from. Each config may hold only the keys a
// clone, the installer's Clone and gjallarctl rebuild --update write, with
// the values they write; the origin must be official. CheckOwnership must
// pass first, so that nobody but root changes what is checked here.
func CheckGit(root string) error {
	return checkGit(root, official, httpsURL)
}

var (
	refName       = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	submoduleName = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// notInGitDir are the files that send git to another repository's objects,
// refs or config, or attach attributes outside the tracked tree.
var notInGitDir = []string{
	"commondir",
	"config.worktree",
	"info/attributes",
	"info/grafts",
	"objects/info/alternates",
	"objects/info/http-alternates",
}

func checkGit(root string, originOK, moduleURLOK func(string) bool) error {
	gitDir := filepath.Join(root, ".git")
	if err := plainDir(gitDir); err != nil {
		return err
	}
	if err := checkGitDir(gitDir, "", originOK, moduleURLOK); err != nil {
		return err
	}
	// Each submodule's git folder, and the worktree it names.
	worktrees := map[string]string{}
	if err := modules(filepath.Join(gitDir, "modules"), root, moduleURLOK, worktrees); err != nil {
		return err
	}
	// In the tree, a .git may only be a submodule's pointer back to its
	// own folder under .git/modules.
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == gitDir {
			return fs.SkipDir
		}
		if d.Name() != ".git" {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a submodule's .git file", path)
		}
		data, err := readCapped(path, 4096)
		if err != nil {
			return err
		}
		target, ok := strings.CutPrefix(strings.TrimSuffix(string(data), "\n"), "gitdir: ")
		if !ok || filepath.IsAbs(target) {
			return fmt.Errorf("%s does not point into %s", path, filepath.Join(gitDir, "modules"))
		}
		resolved := filepath.Join(filepath.Dir(path), target)
		if worktrees[resolved] != filepath.Dir(path) {
			return fmt.Errorf("%s does not point to its own folder in %s", path, filepath.Join(gitDir, "modules"))
		}
		return nil
	})
}

// modules checks every submodule git folder under dir. worktree is the
// worktree of the repository dir belongs to.
func modules(dir, worktree string, urlOK func(string) bool, worktrees map[string]string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := plainDir(dir); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || path == dir {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(path, "HEAD")); err != nil {
			return nil // a folder above a submodule's git folder
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		tree := filepath.Join(worktree, rel)
		want, err := filepath.Rel(path, tree)
		if err != nil {
			return err
		}
		if err := checkGitDir(path, want, urlOK, urlOK); err != nil {
			return err
		}
		worktrees[path] = tree
		if err := modules(filepath.Join(path, "modules"), tree, urlOK, worktrees); err != nil {
			return err
		}
		return fs.SkipDir
	})
}

// checkGitDir checks one git folder. worktree is the core.worktree a
// submodule's folder must name, "" for the checkout's own .git.
func checkGitDir(dir, worktree string, originOK, moduleURLOK func(string) bool) error {
	for _, name := range notInGitDir {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return fmt.Errorf("%s has %s, which a clone never writes", dir, name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	entries, err := gitConfig(filepath.Join(dir, "config"))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.key] {
			return fmt.Errorf("%s sets %s twice", filepath.Join(dir, "config"), e.key)
		}
		seen[e.key] = true
		if !configAllowed(e.key, e.value, worktree, originOK, moduleURLOK) {
			return fmt.Errorf("%s sets %s, which a clone of the official origin does not write", filepath.Join(dir, "config"), e.key)
		}
	}
	return nil
}

func configAllowed(key, value, worktree string, originOK, moduleURLOK func(string) bool) bool {
	isBool := value == "true" || value == "false"
	switch key {
	case "core.repositoryformatversion":
		return value == "0"
	case "core.bare":
		return value == "false"
	case "core.filemode", "core.logallrefupdates", "core.ignorecase", "core.symlinks", "core.precomposeunicode":
		return isBool
	case "core.worktree":
		return worktree != "" && value == worktree
	case "remote.origin.url":
		return originOK(value)
	case "remote.origin.fetch":
		return value == "+refs/heads/*:refs/remotes/origin/*"
	}
	section, rest, _ := strings.Cut(key, ".")
	i := strings.LastIndex(rest, ".")
	if i < 0 {
		return false
	}
	name, variable := rest[:i], rest[i+1:]
	switch {
	case section == "branch" && refName.MatchString(name) && variable == "remote":
		return value == "origin"
	case section == "branch" && refName.MatchString(name) && variable == "merge":
		return value == "refs/heads/"+name
	case section == "submodule" && submoduleName.MatchString(name) && variable == "url":
		return moduleURLOK(value)
	case section == "submodule" && submoduleName.MatchString(name) && variable == "active":
		return value == "true"
	}
	return false
}

// httpsURL accepts a plain https URL without credentials: git fetches it
// over HTTPS and runs no program for it.
func httpsURL(raw string) bool {
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Opaque == ""
}

type configEntry struct{ key, value string }

// gitConfig reads one config file with git's own parser, as git reads it
// for the repository but without following include.path: an include would
// already be a refused key.
func gitConfig(path string) ([]configEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plain file", path)
	}
	if info.Size() > MaxGitConfig {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxGitConfig)
	}
	cmd := GitCommand(context.Background(), "", "config", "--file", path, "--no-includes", "--null", "--list")
	// Outside any repository: git reads no other repository's config.
	cmd.Dir = "/"
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	var entries []configEntry
	for _, item := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if item == "" {
			continue
		}
		key, value, _ := strings.Cut(item, "\n")
		entries = append(entries, configEntry{key, value})
	}
	return entries, nil
}

func plainDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a plain folder", path)
	}
	return nil
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s is not a small plain file", path)
	}
	data := make([]byte, info.Size())
	if _, err := f.ReadAt(data, 0); err != nil && info.Size() > 0 {
		return nil, err
	}
	return slices.Clip(data), nil
}

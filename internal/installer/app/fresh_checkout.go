package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

// officialOrigin is what a new install clones; tests point it at a local
// repository.
var officialOrigin = systemcheckout.Origins[0]

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// machineLocal are the checkout entries the installer writes for this
// machine. git does not track them; they are copied into the clone.
var machineLocal = []string{"generated", "user.config.json"}

// cloneSystemCheckout makes the root-owned system checkout of the freshly
// installed system (ROADMAP section 14, checkpoint 4). It clones the
// official origin at the revision the installer runs from instead of
// copying the installer's tree, so the checkout has the history and origin
// update and status need. The clone must hold the same files as the tree
// that was installed, so the first rebuild builds what is running.
// /etc/gjallar/repository names it, and rebuild, update and the Secure Boot
// continuation read generated/ and user.config.json from it.
func cloneSystemCheckout(
	ctx context.Context,
	repo string,
	targetRoot string,
	revision string,
	out io.Writer,
) error {
	revision = strings.TrimPrefix(revision, "git:")
	if !commitPattern.MatchString(revision) {
		return fmt.Errorf(
			"this installer was built from %q, which is not a commit of the official repository; "+
				"build the installer from a pushed commit without local changes",
			revision,
		)
	}
	dest := filepath.Join(targetRoot, systemcheckout.Path)
	git := func(args ...string) ([]byte, error) {
		return privilegedCommand(ctx, append([]string{"env", "GIT_TERMINAL_PROMPT=0", "git", "-C", dest}, args...)...)
	}

	if _, err := privilegedCommand(ctx, "mkdir", "-p", "-m", "0755", "--", filepath.Dir(dest)); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(systemcheckout.Path), err)
	}
	// git clone refuses a directory that is not empty, so nothing is ever
	// merged into an old checkout.
	if _, err := privilegedCommand(ctx,
		"env", "GIT_TERMINAL_PROMPT=0", "git", "clone", "--quiet", "--no-checkout", "--", officialOrigin, dest,
	); err != nil {
		return fmt.Errorf("clone %s into %s: %w", officialOrigin, systemcheckout.Path, err)
	}
	fail := func(err error) error {
		_, _ = privilegedCommand(context.WithoutCancel(ctx), "rm", "-rf", "--", dest)
		return err
	}

	head, err := git("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return fail(fmt.Errorf("read the default branch of %s: %w", officialOrigin, err))
	}
	upstream := strings.TrimSpace(string(head))
	branch := strings.TrimPrefix(upstream, "origin/")
	if _, err := git("merge-base", "--is-ancestor", revision, upstream); err != nil {
		return fail(fmt.Errorf(
			"%s is not on %s of %s; build the installer from a pushed commit: %w",
			revision, branch, officialOrigin, err,
		))
	}
	for _, step := range [][]string{
		{"checkout", "--quiet", "--no-track", "-B", branch, revision},
		{"branch", "--quiet", "--set-upstream-to=" + upstream, branch},
		{"-c", "protocol.file.allow=never", "submodule", "update", "--quiet", "--init", "--recursive"},
	} {
		if _, err := git(step...); err != nil {
			return fail(fmt.Errorf("check out %s in %s: %w", revision, systemcheckout.Path, err))
		}
	}
	if err := compareCheckout(repo, dest, git); err != nil {
		return fail(err)
	}

	for _, name := range machineLocal {
		source := filepath.Join(repo, name)
		if _, err := os.Lstat(source); err != nil {
			continue
		}
		for _, step := range [][]string{
			{"cp", "-R", "-P", "--no-preserve=all", "--", source, dest + "/"},
			{"chown", "-R", "-P", "-h", "0:0", "--", filepath.Join(dest, name)},
			{"chmod", "-R", "u=rwX,go=rX", "--", filepath.Join(dest, name)},
		} {
			if _, err := privilegedCommand(ctx, step...); err != nil {
				return fail(fmt.Errorf("copy %s into %s: %w", name, systemcheckout.Path, err))
			}
		}
	}
	fmt.Fprintf(out, "PASS: %s cloned at %s (%s), owned by root\n", systemcheckout.Path, revision[:12], branch)
	return nil
}

// compareCheckout fails unless the installer's tree and the clone hold the
// same files: every tracked file with the same content and executable bit,
// and nothing else in the tree apart from the machine-local entries, local
// scratch and what .gitignore leaves out.
func compareCheckout(repo, dest string, git func(...string) ([]byte, error)) error {
	listed, err := git("ls-files", "-z", "--recurse-submodules")
	if err != nil {
		return fmt.Errorf("list the files of %s: %w", systemcheckout.Path, err)
	}
	tracked := map[string]bool{}
	for _, rel := range strings.Split(string(listed), "\x00") {
		if rel != "" {
			tracked[rel] = true
		}
	}
	differs := func(rel string) error {
		return fmt.Errorf(
			"the installer's tree differs from the official commit at %s; "+
				"build the installer from a pushed commit without local changes",
			rel,
		)
	}
	for rel := range tracked {
		if !sameFile(filepath.Join(repo, rel), filepath.Join(dest, rel)) {
			return differs(rel)
		}
	}

	var extra []string
	err = filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		if rel == "." {
			return nil
		}
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if entry.Name() == ".git" || flakesource.Scratch(top) || slices.Contains(machineLocal, top) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && !tracked[filepath.ToSlash(rel)] {
			extra = append(extra, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("read the installer's tree: %w", err)
	}
	if len(extra) == 0 {
		return nil
	}
	// check-ignore exits 1 when none of the paths is ignored. It quotes
	// names with quotes or newlines even so; those do not match and count
	// as differing.
	ignoredOut, _ := git(append([]string{"-c", "core.quotePath=false", "check-ignore", "--no-index", "--"}, extra...)...)
	ignored := strings.Split(string(ignoredOut), "\n")
	for _, rel := range extra {
		if !slices.Contains(ignored, rel) {
			return differs(rel)
		}
	}
	return nil
}

// sameFile compares two files as git sees them: the link target, or the
// content and the executable bit.
func sameFile(a, b string) bool {
	ai, err := os.Lstat(a)
	if err != nil {
		return false
	}
	bi, err := os.Lstat(b)
	if err != nil || ai.Mode().Type() != bi.Mode().Type() {
		return false
	}
	if ai.Mode().Type() == fs.ModeSymlink {
		at, aErr := os.Readlink(a)
		bt, bErr := os.Readlink(b)
		return aErr == nil && bErr == nil && at == bt
	}
	if !ai.Mode().IsRegular() || (ai.Mode().Perm()&0o100 != 0) != (bi.Mode().Perm()&0o100 != 0) {
		return false
	}
	ad, aErr := os.ReadFile(a)
	bd, bErr := os.ReadFile(b)
	return aErr == nil && bErr == nil && bytes.Equal(ad, bd)
}

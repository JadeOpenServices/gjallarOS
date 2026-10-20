package systemcheckout

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Runner runs a command as root and returns its combined output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Commit returns the commit of a build revision ("git:<commit>" or a bare
// commit), or an error naming what to do when the build had local changes
// or did not come from git.
func Commit(revision string) (string, error) {
	commit := strings.TrimPrefix(strings.TrimSpace(revision), "git:")
	if !commitPattern.MatchString(commit) {
		return "", fmt.Errorf(
			"%q is not a commit of the official repository; "+
				"build from a pushed commit without local changes",
			revision,
		)
	}
	return commit, nil
}

// Clone clones origin into dest and checks out revision on a local branch
// that tracks the origin default branch, so update can fast-forward it.
// revision must be on that branch. dest must not exist: git clone refuses
// a directory that is not empty, so nothing is merged into an old checkout.
// A failed clone is removed again. It returns the branch.
func Clone(ctx context.Context, run Runner, origin, dest, revision string) (string, error) {
	commit, err := Commit(revision)
	if err != nil {
		return "", err
	}
	git := func(args ...string) ([]byte, error) {
		return run(ctx, append([]string{"env", "GIT_TERMINAL_PROMPT=0", "git", "-C", dest}, args...)...)
	}
	if _, err := run(ctx, "mkdir", "-p", "-m", "0755", "--", filepath.Dir(dest)); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if _, err := run(ctx, "env", "GIT_TERMINAL_PROMPT=0", "git", "clone", "--quiet", "--no-checkout", "--", origin, dest); err != nil {
		return "", fmt.Errorf("clone %s into %s: %w", origin, dest, err)
	}
	fail := func(err error) (string, error) {
		_, _ = run(context.WithoutCancel(ctx), "rm", "-rf", "--", dest)
		return "", err
	}

	head, err := git("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return fail(fmt.Errorf("read the default branch of %s: %w", origin, err))
	}
	upstream := strings.TrimSpace(string(head))
	branch := strings.TrimPrefix(upstream, "origin/")
	if _, err := git("merge-base", "--is-ancestor", commit, upstream); err != nil {
		return fail(fmt.Errorf(
			"%s is not on %s of %s; build from a pushed commit: %w",
			short(commit), branch, origin, err,
		))
	}
	for _, step := range [][]string{
		{"checkout", "--quiet", "--no-track", "-B", branch, commit},
		{"branch", "--quiet", "--set-upstream-to=" + upstream, branch},
		{"-c", "protocol.file.allow=never", "submodule", "update", "--quiet", "--init", "--recursive"},
	} {
		if _, err := git(step...); err != nil {
			return fail(fmt.Errorf("check out %s in %s: %w", short(commit), dest, err))
		}
	}
	return branch, nil
}

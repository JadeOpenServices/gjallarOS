package systemcheckout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
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
// named after channel that tracks origin's branch of that name, so update
// can fast-forward it. revision must be on a release channel branch of
// origin; when it is newer than channel, update keeps it until channel
// catches up. dest must not exist: git clone refuses a directory that is
// not empty, so nothing is merged into an old checkout. A failed clone is
// removed again.
func Clone(ctx context.Context, run Runner, origin, dest, revision, channel string) error {
	commit, err := Commit(revision)
	if err != nil {
		return err
	}
	if !slices.Contains(config.ReleaseChannels, channel) {
		return fmt.Errorf("%q is not a release channel", channel)
	}
	git := func(args ...string) ([]byte, error) {
		return run(ctx, append([]string{"env", "GIT_TERMINAL_PROMPT=0", "git", "-C", dest}, args...)...)
	}
	if _, err := run(ctx, "mkdir", "-p", "-m", "0755", "--", filepath.Dir(dest)); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if _, err := run(ctx, "env", "GIT_TERMINAL_PROMPT=0", "git", "clone", "--quiet", "--no-checkout", "--", origin, dest); err != nil {
		return fmt.Errorf("clone %s into %s: %w", origin, dest, err)
	}
	fail := func(err error) error {
		_, _ = run(context.WithoutCancel(ctx), "rm", "-rf", "--", dest)
		return err
	}

	upstream := "origin/" + channel
	if _, err := git("rev-parse", "--verify", "--quiet", "refs/remotes/"+upstream); err != nil {
		return fail(fmt.Errorf("%s has no %s branch: %w", origin, channel, err))
	}
	if err := onChannel(git, "refs/remotes/origin/", origin, commit); err != nil {
		return fail(err)
	}
	for _, step := range [][]string{
		{"checkout", "--quiet", "--no-track", "-B", channel, commit},
		{"branch", "--quiet", "--set-upstream-to=" + upstream, channel},
		{"-c", "protocol.file.allow=never", "submodule", "update", "--quiet", "--init", "--recursive"},
	} {
		if _, err := git(step...); err != nil {
			return fail(fmt.Errorf("check out %s in %s: %w", short(commit), dest, err))
		}
	}
	return nil
}

// Released checks that revision is a commit on a release channel of
// origin. The installer calls it before it touches a disk, so the clone at
// the end of an install cannot fail on a commit that was never pushed. It
// fetches the commits only, no files, into a temporary folder.
func Released(ctx context.Context, run Runner, origin, revision string) error {
	commit, err := Commit(revision)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "gjallar-release-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "origin.git")
	if _, err := run(ctx, "env", "GIT_TERMINAL_PROMPT=0", "git", "clone", "--quiet", "--bare", "--filter=tree:0", "--", origin, dest); err != nil {
		return fmt.Errorf("read the release channels of %s: %w", origin, err)
	}
	git := func(args ...string) ([]byte, error) {
		return run(ctx, append([]string{"env", "GIT_TERMINAL_PROMPT=0", "git", "-C", dest}, args...)...)
	}
	return onChannel(git, "refs/heads/", origin, commit)
}

// onChannel checks that commit is on one of the release channel branches,
// named prefix+channel in the repository git runs in.
func onChannel(git func(args ...string) ([]byte, error), prefix, origin, commit string) error {
	notPushed := fmt.Errorf(
		"%s is on no release channel of %s (%s); build from a pushed commit",
		short(commit), origin, strings.Join(config.ReleaseChannels, ", "),
	)
	// for-each-ref --contains fails on a commit origin does not have.
	if _, err := git("cat-file", "-e", commit+"^{commit}"); err != nil {
		return notPushed
	}
	refs := make([]string, len(config.ReleaseChannels))
	for i, name := range config.ReleaseChannels {
		refs[i] = prefix + name
	}
	contains, err := git(append([]string{"for-each-ref", "--contains", commit, "--format=%(refname)"}, refs...)...)
	if err != nil {
		return fmt.Errorf("find the branches of %s: %w", short(commit), err)
	}
	if strings.TrimSpace(string(contains)) == "" {
		return notPushed
	}
	return nil
}

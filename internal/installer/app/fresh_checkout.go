package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/deploy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

// officialOrigin is what a new install clones; tests point it at a local
// repository.
var officialOrigin = systemcheckout.Origins[0]

// machineLocal are the checkout entries the installer writes for this
// machine. git does not track them; they are copied into the clone.
var machineLocal = []string{"generated", "user.config.json"}

// checkReleasedRevision checks, as the live user, that revision is on a
// release channel of the official origin. Tests replace it.
var checkReleasedRevision = func(ctx context.Context, revision string) error {
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		output, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			return output, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return output, nil
	}
	return systemcheckout.Released(ctx, run, officialOrigin, revision)
}

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
	channel string,
	out io.Writer,
) error {
	dest := filepath.Join(targetRoot, systemcheckout.Path)
	if err := systemcheckout.Clone(ctx, systemcheckout.Runner(privilegedCommand), officialOrigin, dest, revision, channel); err != nil {
		return fmt.Errorf("the installer cannot set up %s: %w", systemcheckout.Path, err)
	}
	revision, _ = systemcheckout.Commit(revision)
	git := func(args ...string) ([]byte, error) {
		return privilegedCommand(ctx, append([]string{"env", "GIT_TERMINAL_PROMPT=0", "git", "-C", dest}, args...)...)
	}
	fail := func(err error) error {
		_, _ = privilegedCommand(context.WithoutCancel(ctx), "rm", "-rf", "--", dest)
		return err
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
	fmt.Fprintf(out, "PASS: %s cloned at %s (%s), owned by root\n", systemcheckout.Path, revision[:12], channel)
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

// systemProfile is the link nixos-rebuild moves to each new system
// generation. Tests replace it.
var systemProfile = "/nix/var/nix/profiles/system"

// deployApply installs the boot generation; tests replace it.
var deployApply = deploy.Apply

// checkoutFree fails when the system checkout under targetRoot already
// exists. A first install clones into an empty place only; an old folder is
// never merged into or removed.
func checkoutFree(targetRoot string) error {
	_, err := os.Lstat(filepath.Join(targetRoot, systemcheckout.Path))
	switch {
	case err == nil:
		return fmt.Errorf(
			"%s already exists, but gjallarOS is not installed on this system; "+
				"nothing was changed. Move %s away and run the installer again",
			systemcheckout.Path, systemcheckout.Path,
		)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("check %s: %w", systemcheckout.Path, err)
	}
}

// deployInPlace installs the boot generation of an in-place install from
// repo. A first install (Plane 313) clones the system checkout first, so the
// new generation never names a checkout that is missing. When the build
// fails and the system profile still points where it pointed before, no
// generation uses the clone and it is removed again, so a rerun starts
// clean. If the profile moved, or cannot be read, the clone stays.
func deployInPlace(
	ctx context.Context,
	repo string,
	targetRoot string,
	hostname string,
	revision string,
	channel string,
	first bool,
	out io.Writer,
) error {
	if !first {
		if repo == systemcheckout.Path {
			return systemRebuild(ctx, out)
		}
		return deployApply(ctx, repo, hostname)
	}
	before, beforeErr := os.Readlink(systemProfile)
	fmt.Fprintln(out, "STAGE: cloning the system checkout")
	if err := cloneSystemCheckout(ctx, repo, targetRoot, revision, channel, out); err != nil {
		return err
	}
	err := deployApply(ctx, repo, hostname)
	if err == nil {
		return nil
	}
	after, afterErr := os.Readlink(systemProfile)
	if beforeErr != nil || afterErr != nil || before != after {
		fmt.Fprintf(out, "%s is kept: a new system generation may use it.\n", systemcheckout.Path)
		return err
	}
	dest := filepath.Join(targetRoot, systemcheckout.Path)
	if _, rmErr := privilegedCommand(context.WithoutCancel(ctx), "rm", "-rf", "--", dest); rmErr != nil {
		return fmt.Errorf("%w; removing %s again also failed: %v", err, systemcheckout.Path, rmErr)
	}
	fmt.Fprintf(out, "%s removed again; no system generation uses it.\n", systemcheckout.Path)
	return err
}

// systemRebuildCommand is the rebuild every installed host with a system
// checkout runs; tests point it elsewhere.
var systemRebuildCommand = "/run/current-system/sw/bin/gjallar-system-rebuild"

// systemRebuild updates an installed host that builds from the system
// checkout (wiki 752 Q214): gjallar-system-rebuild --update moves
// /etc/gjallar/system to the channel in its user.config.json under the same
// checks gjallarctl rebuild runs (developer grant, official origin, owner),
// builds it and switches to it. Its output is the user's to read.
var systemRebuild = func(ctx context.Context, out io.Writer) error {
	fmt.Fprintf(out, "STAGE: updating %s and building it\n", systemcheckout.Path)
	cmd := exec.CommandContext(ctx, systemRebuildCommand, "--update")
	cmd.Stdin = os.Stdin
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gjallar-system-rebuild --update: %w", err)
	}
	return nil
}

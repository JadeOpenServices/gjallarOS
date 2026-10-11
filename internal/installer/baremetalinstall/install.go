// Package baremetalinstall performs the final NixOS installation into a
// previously prepared fresh target mounted at /mnt.
//
// Disk selection, partitioning, resizing, encryption, filesystem creation, and
// mounting belong to earlier installer stages. This package deliberately does
// none of them. In particular, it preserves the post-GJAL-31 rule that normal
// installation never assumes it owns or may recreate the whole target disk.
package baremetalinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/binarycache"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/buildlimits"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
)

const targetRoot = "/mnt"

// buildLimits is replaced in tests so command keys stay machine independent.
var buildLimits = buildlimits.Args

// stageFlake stages a clean copy of the repository (no .git, no .vm) and
// returns its directory. On live media the Nix store is RAM, and a path:
// reference to the checkout itself copies gigabytes of git history into it.
// Tests replace it so command keys keep the repository path.
var stageFlake = func(repo string) (string, func(), error) {
	source, err := flakesource.Stage(repo, "")
	if err != nil {
		return "", nil, err
	}
	return source.Dir, func() { _ = source.Close() }, nil
}

var hostnamePattern = regexp.MustCompile(
	`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`,
)

type Input struct {
	Repo     string
	Hostname string
	Out      io.Writer
}

type Result struct {
	Target string
	Stage  string
}

type runner interface {
	Run(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args ...string,
) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (execRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func Install(ctx context.Context, input Input) (Result, error) {
	return install(ctx, input, execRunner{})
}

func install(
	ctx context.Context,
	input Input,
	r runner,
) (Result, error) {
	if input.Out == nil {
		return Result{}, fmt.Errorf("installation output writer is required")
	}

	repo, target, err := validateSource(input.Repo, input.Hostname)
	if err != nil {
		return Result{}, err
	}

	fmt.Fprintln(input.Out, "STAGE: validating-target")

	if err := verifyExactMount(ctx, r, targetRoot); err != nil {
		return Result{}, fmt.Errorf("verify target root: %w", err)
	}
	if err := verifyExactMount(ctx, r, filepath.Join(targetRoot, "boot")); err != nil {
		return Result{}, fmt.Errorf("verify target boot mount: %w", err)
	}

	configBefore, err := snapshotInstallerConfig(repo)
	if err != nil {
		return Result{}, err
	}

	swapCleanup, err := enableInstallSwap(ctx, r, input.Out)
	if err != nil {
		return Result{}, err
	}
	defer swapCleanup()

	staged, cleanup, err := stageFlake(repo)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	target = "path:" + staged + "#" + input.Hostname

	fmt.Fprintf(
		input.Out,
		"STAGE: installing target=%s root=%s\n",
		target,
		targetRoot,
	)

	// --no-root-passwd ensures nixos-install never opens a password prompt or
	// tries to move a secret through this stage. GjallarOS owns its configured
	// credential policy separately.
	//
	// No JODS service, endpoint, enrollment command, or installation-complete
	// marker is touched here. Management activation happens only in its later
	// explicitly gated lifecycle.
	installArgs := []string{
		"nixos-install",
		"--root",
		targetRoot,
		"--flake",
		target,
		"--no-root-passwd",
	}
	if limits := buildLimits(); len(limits) > 0 {
		fmt.Fprintf(
			input.Out,
			"Low memory: limiting local builds (%s).\n",
			strings.Join(limits, " "),
		)
		installArgs = append(installArgs, limits...)
	}
	installArgs = append(installArgs, binarycache.Args(repo)...)
	if err := r.Run(
		ctx,
		nil,
		input.Out,
		input.Out,
		"sudo",
		installArgs...,
	); err != nil {
		return Result{}, fmt.Errorf("nixos-install failed: %w", err)
	}

	if err := verifyInstallerConfig(repo, configBefore); err != nil {
		return Result{}, err
	}

	// Successful nixos-install must leave the target system profile behind.
	if err := verifySystemProfile(targetRoot); err != nil {
		return Result{}, err
	}

	fmt.Fprintln(input.Out, "STAGE: os-installed")
	fmt.Fprintf(
		input.Out,
		"PASS: GjallarOS installed into %s using %s\n",
		targetRoot,
		target,
	)

	return Result{
		Target: target,
		Stage:  "os-installed",
	}, nil
}

// verifySystemProfile checks that nixos-install left a system profile link
// that resolves to a store path inside root. Profile links are absolute
// (/nix/store/...) and name the target's store, not the host's: os.Stat
// followed them into the live medium's store and rejected a good install
// (e2e-target, 2026-09-29). Absolute links are therefore rebased onto root.
func verifySystemProfile(root string) error {
	profile := filepath.Join(root, "nix", "var", "nix", "profiles", "system")
	info, err := os.Lstat(profile)
	if err != nil {
		return fmt.Errorf("verify installed system profile %s: %w", profile, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("installed system profile is not a symlink: %s", profile)
	}

	current := profile
	for hops := 0; hops < 8; hops++ {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("verify installed system profile %s: %w", profile, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		link, err := os.Readlink(current)
		if err != nil {
			return fmt.Errorf("verify installed system profile %s: %w", profile, err)
		}
		if filepath.IsAbs(link) {
			current = filepath.Join(root, link)
		} else {
			current = filepath.Join(filepath.Dir(current), link)
		}
	}
	return fmt.Errorf("installed system profile has too many links: %s", profile)
}

// Live media keep their Nix store and the checkout in RAM, and have no swap:
// evaluating the GjallarOS configuration inside nixos-install was OOM-killed
// on a 12 GiB machine. A temporary swapfile on the target avoids that. It
// lives on the encrypted target Btrfs, so swapped pages never reach the disk
// in clear, and is removed after the installation.
const (
	installSwapPath = targetRoot + "/.gjallar-install.swap"
	installSwapSize = "8g"
)

func enableInstallSwap(ctx context.Context, r runner, out io.Writer) (func(), error) {
	noop := func() {}
	active, err := r.Output(ctx, "swapon", "--show=NAME", "--noheadings")
	if err != nil {
		return nil, fmt.Errorf("inspect active swap: %w", err)
	}
	if strings.TrimSpace(string(active)) != "" {
		return noop, nil
	}
	fstype, err := r.Output(ctx, "findmnt", "-nro", "FSTYPE", "--mountpoint", targetRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect target filesystem: %w", err)
	}
	if strings.TrimSpace(string(fstype)) != "btrfs" {
		fmt.Fprintln(out, "No swap active and the target is not Btrfs; installing without temporary swap.")
		return noop, nil
	}
	// Cleanup must also run once an interrupt cancelled ctx.
	cleanupCtx := context.WithoutCancel(ctx)
	remove := func() {
		_ = r.Run(cleanupCtx, nil, out, out, "sudo", "rm", "-f", "--", installSwapPath)
	}
	remove() // leftover from an interrupted attempt
	if err := r.Run(ctx, nil, out, out, "sudo", "btrfs", "filesystem", "mkswapfile", "--size", installSwapSize, installSwapPath); err != nil {
		remove()
		return nil, fmt.Errorf("create temporary installation swap: %w", err)
	}
	if err := r.Run(ctx, nil, out, out, "sudo", "swapon", installSwapPath); err != nil {
		remove()
		return nil, fmt.Errorf("enable temporary installation swap: %w", err)
	}
	fmt.Fprintf(out, "Temporary %s swap on the encrypted target: %s\n", installSwapSize, installSwapPath)
	return func() {
		_ = r.Run(cleanupCtx, nil, out, out, "sudo", "swapoff", installSwapPath)
		remove()
	}, nil
}

func validateSource(repo, hostname string) (string, string, error) {
	if strings.TrimSpace(repo) == "" {
		return "", "", fmt.Errorf("repository path is required")
	}

	root, err := filepath.Abs(repo)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository: %w", err)
	}

	info, err := os.Stat(filepath.Join(root, "flake.nix"))
	if err != nil {
		return "", "", fmt.Errorf("flake.nix not found: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("flake.nix is not a regular file")
	}

	if !hostnamePattern.MatchString(hostname) ||
		hostname == "." ||
		hostname == ".." {
		return "", "", fmt.Errorf(
			"invalid configuration hostname: %q",
			hostname,
		)
	}

	// path:, not a bare directory: that resolves to git+file, which hides the
	// untracked machine-local generated/*.nix files the flake imports. install
	// points it at a staged copy (stageFlake).
	return root, "path:" + root + "#" + hostname, nil
}

func verifyExactMount(
	ctx context.Context,
	r runner,
	mountPoint string,
) error {
	raw, err := r.Output(
		ctx,
		"findmnt",
		"-nro",
		"TARGET",
		"--mountpoint",
		mountPoint,
	)
	if err != nil {
		return fmt.Errorf("%s is not mounted: %w", mountPoint, err)
	}

	actual := filepath.Clean(strings.TrimSpace(string(raw)))
	if actual != mountPoint {
		return fmt.Errorf(
			"mount target mismatch: expected %s, got %q",
			mountPoint,
			strings.TrimSpace(string(raw)),
		)
	}

	return nil
}

type configSnapshot struct {
	Path   string
	Exists bool
	Sum    [sha256.Size]byte
}

func snapshotInstallerConfig(repo string) ([]configSnapshot, error) {
	paths := []string{
		filepath.Join(repo, "generated", "state.nix"),
		filepath.Join(repo, "user.config.json"),
	}

	result := make([]configSnapshot, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			result = append(result, configSnapshot{
				Path:   path,
				Exists: false,
			})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf(
				"snapshot installer config %s: %w",
				path,
				err,
			)
		}

		result = append(result, configSnapshot{
			Path:   path,
			Exists: true,
			Sum:    sha256.Sum256(data),
		})
	}

	return result, nil
}

func verifyInstallerConfig(
	repo string,
	before []configSnapshot,
) error {
	expected := map[string]configSnapshot{}
	for _, snapshot := range before {
		expected[snapshot.Path] = snapshot
	}

	for _, path := range []string{
		filepath.Join(repo, "generated", "state.nix"),
		filepath.Join(repo, "user.config.json"),
	} {
		prior := expected[path]

		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			if prior.Exists {
				return fmt.Errorf(
					"installer config disappeared during installation: %s",
					path,
				)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf(
				"verify installer config %s: %w",
				path,
				err,
			)
		}
		if !prior.Exists {
			return fmt.Errorf(
				"installer config unexpectedly appeared during installation: %s",
				path,
			)
		}

		after := sha256.Sum256(data)
		if !bytes.Equal(after[:], prior.Sum[:]) {
			return fmt.Errorf(
				"installer config changed during installation: %s",
				path,
			)
		}
	}

	return nil
}

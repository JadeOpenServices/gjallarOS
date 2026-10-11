package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/binarycache"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/buildlimits"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

func Target(repo, hostname string) (string, error) {
	root, err := filepath.Abs(repo)
	if err != nil {
		return "", fmt.Errorf("resolve repository: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, "flake.nix")); err != nil {
		return "", fmt.Errorf("flake.nix not found: %w", err)
	}
	if !hostnamePattern.MatchString(hostname) || hostname == "." || hostname == ".." {
		return "", fmt.Errorf("invalid configuration hostname: %q", hostname)
	}
	return "path:" + root + "#" + hostname, nil
}

// Apply validates and installs the boot generation for hostname. It evaluates
// a staged copy of repo (flakesource) so the store never receives .git or
// other ignored scratch directories.
func Apply(ctx context.Context, repo, hostname string) error {
	if _, err := Target(repo, hostname); err != nil {
		return err
	}
	source, err := flakesource.Stage(repo, "")
	if err != nil {
		return err
	}
	defer source.Close()
	return apply(ctx, source.Ref(hostname), binarycache.Args(repo))
}

func apply(ctx context.Context, target string, cacheArgs []string) error {
	// Before the swap below: build parallelism follows real memory.
	limits := buildlimits.Args()
	cleanup, err := enableEvalSwap(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := run(ctx, "sudo", "nixos-rebuild", "dry-build", "--flake", target, "--show-trace"); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	args := []string{"nixos-rebuild", "boot", "--flake", target}
	if len(limits) > 0 {
		fmt.Printf("Low memory: limiting local builds (%s).\n", strings.Join(limits, " "))
		args = append(args, limits...)
	}
	args = append(args, cacheArgs...)
	if err := run(ctx, "sudo", args...); err != nil {
		return fmt.Errorf("boot generation failed: %w", err)
	}
	return nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// maxEvalSwap caps the temporary zram device, as Fedora's zram-generator
// does: RAM, at most 8 GiB.
const maxEvalSwap = 8 << 30

// enableEvalSwap adds a temporary zram swap when none is active. Evaluating
// the configuration takes about 3.5 GiB; on a 4 GiB machine without swap Nix
// was OOM-killed ("validation failed: exit status 247", Galaxy Book 12-like
// VM, 2026-10-10). zram stays in RAM, so an unencrypted in-place root never
// receives swapped secrets. The installed system brings its own zram swap.
func enableEvalSwap(ctx context.Context) (func(), error) {
	noop := func() {}
	active, err := exec.CommandContext(ctx, "swapon", "--show=NAME", "--noheadings").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect active swap: %w", err)
	}
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("read memory size: %w", err)
	}
	size := evalSwapSize(strings.TrimSpace(string(active)) != "", meminfo)
	if size == 0 {
		return noop, nil
	}
	if err := run(ctx, "sudo", "modprobe", "zram"); err != nil {
		return nil, fmt.Errorf("load zram module: %w", err)
	}
	found, err := exec.CommandContext(ctx, "sudo", "zramctl", "--find", "--algorithm", "zstd", "--size", strconv.FormatUint(size, 10)).Output()
	if err != nil {
		return nil, fmt.Errorf("create temporary zram swap: %w", err)
	}
	device := strings.TrimSpace(string(found))
	cleanupCtx := context.WithoutCancel(ctx)
	reset := func() { _ = run(cleanupCtx, "sudo", "zramctl", "--reset", device) }
	if err := run(ctx, "sudo", "mkswap", device); err != nil {
		reset()
		return nil, fmt.Errorf("format temporary zram swap: %w", err)
	}
	if err := run(ctx, "sudo", "swapon", device); err != nil {
		reset()
		return nil, fmt.Errorf("enable temporary zram swap: %w", err)
	}
	fmt.Printf("No swap active: temporary %d MiB compressed swap in RAM on %s.\n", size>>20, device)
	return func() {
		_ = run(cleanupCtx, "sudo", "swapoff", device)
		reset()
	}, nil
}

// evalSwapSize returns the temporary zram size in bytes, or 0 when swap is
// already active or MemTotal is unreadable.
func evalSwapSize(swapActive bool, meminfo []byte) uint64 {
	if swapActive {
		return 0
	}
	for _, line := range strings.Split(string(meminfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return min(kib<<10, maxEvalSwap)
	}
	return 0
}

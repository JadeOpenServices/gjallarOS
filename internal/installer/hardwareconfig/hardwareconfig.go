package hardwareconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	freshTargetRoot = "/mnt"
	relativeTarget  = "generated/hardware.nix"
)

func Target(repo string) string {
	return filepath.Join(repo, filepath.FromSlash(relativeTarget))
}

type commandRunner interface {
	Run(context.Context, io.Writer, io.Writer, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args ...string,
) error {
	cmd := exec.CommandContext(ctx, name, args...)
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

func ValidateTarget(repo, target string) (string, error) {
	root, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("hardware target is outside repository: %s", target)
	}
	if filepath.ToSlash(rel) != relativeTarget {
		return "", fmt.Errorf(
			"invalid hardware target %s: expected %s",
			target,
			relativeTarget,
		)
	}
	return abs, nil
}

// Generate preserves the existing behavior: generate hardware configuration
// for the currently running machine.
func Generate(
	ctx context.Context,
	repo,
	target string,
	now time.Time,
) (Result, error) {
	return generate(
		ctx,
		repo,
		target,
		now,
		"",
		execRunner{},
	)
}

// GenerateTarget generates hardware configuration for the prepared fresh
// installation mounted at /mnt.
//
// It deliberately does not alter flake.nix or any unrelated source file.
// Generated hardware state has one canonical machine-local target and is
// backed up before atomic replacement.
func GenerateTarget(
	ctx context.Context,
	repo,
	target string,
	now time.Time,
) (Result, error) {
	return generate(
		ctx,
		repo,
		target,
		now,
		freshTargetRoot,
		execRunner{},
	)
}

func generate(
	ctx context.Context,
	repo,
	target string,
	now time.Time,
	root string,
	runner commandRunner,
) (Result, error) {
	target, err := ValidateTarget(repo, target)
	if err != nil {
		return Result{}, err
	}

	if root != "" {
		cleanRoot := filepath.Clean(root)
		isolatedRoot := strings.HasPrefix(
			filepath.Base(cleanRoot),
			"gjallar-hardware-root-",
		)

		if cleanRoot != freshTargetRoot && !isolatedRoot {
			return Result{}, fmt.Errorf(
				"unsupported hardware discovery root %s",
				root,
			)
		}

		if cleanRoot == freshTargetRoot {
			if err := verifyFreshTargetMount(ctx, runner, root); err != nil {
				return Result{}, err
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return Result{}, fmt.Errorf("create hardware state directory: %w", err)
	}

	backup := ""
	if _, err := os.Stat(target); err == nil {
		backup, err = backupFile(target, now)
		if err != nil {
			return Result{}, err
		}
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("inspect hardware configuration target: %w", err)
	}

	tmp, err := os.CreateTemp(
		filepath.Dir(target),
		".generated.nix-*",
	)
	if err != nil {
		return Result{Backup: backup}, fmt.Errorf(
			"create temporary hardware configuration: %w",
			err,
		)
	}

	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	args := []string{}
	if root != "" {
		args = append(args, "--root", root)
	}
	args = append(args, "--show-hardware-config")

	commandArgs := append(
		[]string{"nixos-generate-config"},
		args...,
	)

	if err := runner.Run(
		ctx,
		tmp,
		os.Stderr,
		"sudo",
		commandArgs...,
	); err != nil {
		_ = tmp.Close()
		return Result{Backup: backup}, fmt.Errorf("nixos-generate-config: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Result{Backup: backup}, fmt.Errorf(
			"sync hardware configuration: %w",
			err,
		)
	}
	if err := tmp.Close(); err != nil {
		return Result{Backup: backup}, fmt.Errorf(
			"close hardware configuration: %w",
			err,
		)
	}

	info, err := os.Stat(tmpName)
	if err != nil {
		return Result{Backup: backup}, fmt.Errorf(
			"inspect generated hardware configuration: %w",
			err,
		)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return Result{Backup: backup}, fmt.Errorf(
			"nixos-generate-config produced an empty or invalid hardware configuration",
		)
	}

	data, err := os.ReadFile(tmpName)
	if err != nil {
		return Result{Backup: backup}, fmt.Errorf(
			"read generated hardware configuration: %w",
			err,
		)
	}
	if !bytesContainNixModule(data) {
		return Result{Backup: backup}, fmt.Errorf(
			"generated hardware configuration does not look like a NixOS module",
		)
	}
	data, dropped, err := dropDiskSwap(data)
	if err != nil {
		return Result{Backup: backup}, err
	}
	if len(dropped) > 0 {
		if err := os.WriteFile(tmpName, data, 0o600); err != nil {
			return Result{Backup: backup}, fmt.Errorf(
				"write hardware configuration without disk swap: %w",
				err,
			)
		}
	}

	if err := os.Rename(tmpName, target); err != nil {
		return Result{Backup: backup}, fmt.Errorf(
			"replace hardware configuration: %w",
			err,
		)
	}

	return Result{Backup: backup, DroppedSwap: dropped}, nil
}

// Result is what a hardware generation did.
type Result struct {
	// Backup is the saved previous hardware configuration, if any.
	Backup string
	// DroppedSwap lists the disk swap devices that were active while
	// nixos-generate-config ran and were left out of the configuration.
	DroppedSwap []string
}

// DroppedSwapNotice tells the user which old swap partitions gjallarOS
// does not use, or returns "" when there were none.
func DroppedSwapNotice(dropped []string) string {
	var b strings.Builder
	for _, device := range dropped {
		fmt.Fprintf(&b, "Old disk swap not used: %s\n", device)
	}
	if b.Len() > 0 {
		b.WriteString("gjallarOS swaps to compressed memory (zram) only. The partition stays as it is; nothing on it was changed.\n")
	}
	return b.String()
}

var swapDevice = regexp.MustCompile(`device = "([^"]*)";`)

// dropDiskSwap empties swapDevices. gjallarOS swaps to zram only
// (system/hardware/memory.nix). nixos-generate-config lists every active
// swap partition, but the unlock of an encrypted one lives in the old
// configuration.nix, which gjallarOS does not use: boot then waits 90s
// for a device that never appears, and the extra LUKS partition stopped
// TPM2 setup (locked laptop, 2026-11-15). The partition itself is left
// untouched.
func dropDiskSwap(data []byte) ([]byte, []string, error) {
	text := string(data)
	const key = "swapDevices ="
	switch strings.Count(text, key) {
	case 0:
		return data, nil, nil
	case 1:
	default:
		return nil, nil, fmt.Errorf("generated hardware configuration sets swapDevices more than once")
	}
	start := strings.Index(text, key)
	length := strings.Index(text[start:], "];")
	if length < 0 {
		return nil, nil, fmt.Errorf("generated hardware configuration has an unterminated swapDevices list")
	}
	end := start + length + len("];")
	var dropped []string
	for _, match := range swapDevice.FindAllStringSubmatch(text[start:end], -1) {
		dropped = append(dropped, match[1])
	}
	if len(dropped) == 0 {
		return data, nil, nil
	}
	return []byte(text[:start] + "swapDevices = [ ];" + text[end:]), dropped, nil
}

func verifyFreshTargetMount(
	ctx context.Context,
	runner commandRunner,
	root string,
) error {
	raw, err := runner.Output(
		ctx,
		"findmnt",
		"-nro",
		"TARGET",
		"--mountpoint",
		root,
	)
	if err != nil {
		return fmt.Errorf(
			"fresh target %s is not mounted: %w",
			root,
			err,
		)
	}

	if filepath.Clean(strings.TrimSpace(string(raw))) != root {
		return fmt.Errorf(
			"fresh target mount verification failed: expected %s, got %q",
			root,
			strings.TrimSpace(string(raw)),
		)
	}

	return nil
}

func bytesContainNixModule(data []byte) bool {
	text := strings.TrimSpace(string(data))
	return strings.Contains(text, "{") &&
		strings.Contains(text, "config") &&
		strings.Contains(text, "lib") &&
		strings.Contains(text, "pkgs")
}

// backupFile copies target beside itself. A second run within the same
// second gets a numbered name instead of failing on the existing backup.
func backupFile(target string, now time.Time) (string, error) {
	base := target + ".bak." + now.Format("20060102150405")
	backup := base
	for n := 1; ; n++ {
		err := copyFile(target, backup)
		if !errors.Is(err, fs.ErrExist) || n > 99 {
			return backup, err
		}
		backup = fmt.Sprintf("%s.%d", base, n)
	}
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf(
			"open hardware backup source: %w",
			err,
		)
	}
	defer in.Close()

	out, err := os.OpenFile(
		target,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0600,
	)
	if err != nil {
		return fmt.Errorf("create hardware backup: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("write hardware backup: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync hardware backup: %w", err)
	}
	return out.Close()
}

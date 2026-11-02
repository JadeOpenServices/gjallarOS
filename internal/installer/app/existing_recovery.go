package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryresize"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

type recoveryCapabilityRunner struct{}

func (recoveryCapabilityRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

var inspectCurrentRoot = func(
	ctx context.Context,
) (source string, filesystem string, err error) {
	out, err := exec.CommandContext(
		ctx,
		"findmnt",
		"-nvro",
		"SOURCE,FSTYPE",
		"--target",
		"/",
	).Output()
	if err != nil {
		return "", "", fmt.Errorf(
			"inspect current root mount: %w",
			err,
		)
	}

	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return "", "", fmt.Errorf(
			"unexpected current root mount information %q",
			strings.TrimSpace(string(out)),
		)
	}

	return filepath.Clean(fields[0]),
		strings.ToLower(strings.TrimSpace(fields[1])),
		nil
}

var detectCurrentRootFilesystem = func(
	ctx context.Context,
) (string, error) {
	return recoveryresize.DetectRootFilesystem(
		ctx,
		recoveryCapabilityRunner{},
		"/",
	)
}

// installedSystemEvidence reports whether the running system was deployed by
// GjallarOS. Every flake-built system names its checkout in
// /etc/gjallar/repository; installation-complete is written only by JODS
// enrollment, so unmanaged installs have the first one alone. Swapped in tests,
// both read the live host.
var installedSystemEvidence = func(ctx context.Context) bool {
	if _, err := os.Stat(installercheck.SystemRepositoryFile); err == nil {
		return true
	}
	return privilegedFileExists(ctx, "/var/lib/gjallarOS/installation-complete")
}

func detectExistingInstalledSystem(
	ctx context.Context,
	repo string,
) (bool, error) {
	// Live media can carry the same files; only a disk root is an install.
	persistent, err := detectPersistentInstalledHost(ctx)
	if err != nil || !persistent {
		return false, err
	}
	return installedSystemEvidence(ctx), nil
}

func handleExistingRecoveryFilesystem(
	ctx context.Context,
	ui prompt.UI,
	presetPath string,
	s *state,
	out io.Writer,
) (bool, error) {
	filesystem, err := detectCurrentRootFilesystem(ctx)
	if err != nil {
		return false, err
	}

	if filesystem == "btrfs" {
		return true, nil
	}

	message := fmt.Sprintf(
		"Dedicated GjallarOS recovery storage cannot be enabled on this existing installation.\n\n"+
			"Current root filesystem: %s\n\n"+
			"GjallarOS currently supports shrinking an existing root for the dedicated recovery partition only when the root filesystem is Btrfs. "+
			"The current %s root cannot use the supported live inspection and maintenance shrink path.\n\n"+
			"To use this recovery feature, reinstall or migrate the root filesystem to Btrfs.\n\n"+
			"Continue installing GjallarOS without the recovery feature?",
		filesystem,
		filesystem,
	)

	if s.user.UnattendedInstall {
		return false, fmt.Errorf(
			"recovery requires Btrfs on an existing installation; current filesystem is %s; "+
				"set recoveryEnable and recoveryPartitionEnable to false or migrate/reinstall with Btrfs",
			filesystem,
		)
	}

	continued, err := ui.Confirm(ctx, message, false)
	if err != nil {
		return false, err
	}
	if !continued {
		return false, nil
	}

	s.user.RecoveryEnable = false
	s.user.RecoveryPartitionEnable = false
	s.recoveryDisk = ""
	s.recoveryPartition = ""
	s.recoverySigningKey = ""
	s.recoverySigningPublicKey = ""

	if err := config.WriteAtomic(presetPath, s.user); err != nil {
		return false, fmt.Errorf(
			"persist recovery-disabled user configuration: %w",
			err,
		)
	}

	fmt.Fprintf(
		out,
		"Recovery disabled for this installation because the current root filesystem is %s.\n",
		filesystem,
	)
	fmt.Fprintln(
		out,
		"Wrote recoveryEnable=false and recoveryPartitionEnable=false to",
		presetPath,
	)

	return true, nil
}

var inspectRecoveryRoot = func(
	ctx context.Context,
	target string,
) ([]byte, error) {
	return exec.CommandContext(
		ctx,
		"findmnt",
		"-nvro",
		"SOURCE,OPTIONS",
		"--target",
		target,
	).Output()
}

func detectRecoveryInstalledRoot(ctx context.Context, target string) (bool, error) {
	target = filepath.Clean(target)
	if !filepath.IsAbs(target) || target == "/" {
		return false, fmt.Errorf("recovery installed root must be an absolute non-root path: %q", target)
	}

	out, err := inspectRecoveryRoot(ctx, target)
	if err != nil {
		return false, fmt.Errorf(
			"authenticated recovery root is not mounted at %s: %w",
			target,
			err,
		)
	}

	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		return false, fmt.Errorf(
			"unexpected recovery root mount information %q",
			strings.TrimSpace(string(out)),
		)
	}

	source := filepath.Clean(fields[0])
	if !strings.HasPrefix(source, "/dev/mapper/") {
		return false, fmt.Errorf(
			"recovery root at %s is not an authenticated LUKS mapping: %s",
			target,
			source,
		)
	}

	options := "," + fields[1] + ","
	if !strings.Contains(options, ",rw,") {
		return false, fmt.Errorf(
			"recovery root at %s is not mounted read-write",
			target,
		)
	}

	if _, err := os.Stat(filepath.Join(target, "etc", "NIXOS")); err != nil {
		return false, fmt.Errorf(
			"recovery root at %s is not a mounted NixOS installation: %w",
			target,
			err,
		)
	}

	return true, nil
}

func detectPersistentInstalledHost(
	ctx context.Context,
) (bool, error) {
	source, filesystem, err := inspectCurrentRoot(ctx)
	if err != nil {
		return false, err
	}

	if source == "overlay" || filesystem == "overlay" {
		return false, nil
	}

	return strings.HasPrefix(source, "/dev/"), nil
}

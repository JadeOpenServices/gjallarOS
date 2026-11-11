package recoveryresize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

const FreshInstallerTargetMountpoint = "/mnt"

type ExecutorRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
	Run(context.Context, string, ...string) error
	RunInput(context.Context, []byte, string, ...string) error
}

type ExecuteInput struct {
	Topology   Topology
	Plan       Plan
	Passphrase []byte
	Out        io.Writer
}

type ExecuteResult struct {
	Stage                  string
	RootPartitionPath      string
	RootPartitionSizeBytes uint64
	RootPARTUUID           string
	LUKSUUID               string
	BtrfsUUID              string
}

// ExecuteFreshInstallerResize performs only the root contraction required to
// expose verified GPT free space.
//
// SECURITY / LIFECYCLE BOUNDARY:
//
// This executor is intentionally restricted to a fresh installer target
// mounted at /mnt. It must never operate on the running installed root "/".
//
// Mutation order:
//
//  1. independently validate current identities and geometry;
//  2. shrink mounted Btrfs;
//  3. verify Btrfs size/identity;
//  4. sync and unmount;
//  5. close dm-crypt mapping;
//  6. shorten GPT partition END only;
//  7. make kernel re-read that partition entry;
//  8. verify start/PARTUUID/type/GUID/size;
//  9. reopen LUKS using caller-provided stdin secret;
//  10. remount and verify Btrfs/LUKS identity.
//
// GJAL-31 remains responsible for creating the recovery partition after this
// function returns successfully.
func ExecuteFreshInstallerResize(
	ctx context.Context,
	runner ExecutorRunner,
	input ExecuteInput,
) (ExecuteResult, error) {
	if input.Out == nil {
		input.Out = io.Discard
	}

	if len(input.Passphrase) == 0 {
		return ExecuteResult{}, errors.New(
			"LUKS passphrase is required for recovery resize",
		)
	}

	secret := append([]byte(nil), input.Passphrase...)
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()

	top := input.Topology
	plan := input.Plan

	if err := ValidateTopology(top); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"validate recovery resize topology: %w",
			err,
		)
	}

	if filepath.Clean(top.RootMountpoint) !=
		FreshInstallerTargetMountpoint {
		return ExecuteResult{}, fmt.Errorf(
			"recovery resize executor is fresh-install only: target mountpoint must be %s, got %s",
			FreshInstallerTargetMountpoint,
			top.RootMountpoint,
		)
	}

	if filepath.Clean(top.RootMountpoint) == "/" {
		return ExecuteResult{}, errors.New(
			"refusing recovery resize against the running system root",
		)
	}

	if err := validateExecutionPlan(top, plan); err != nil {
		return ExecuteResult{}, err
	}

	// Re-run the read-only discovery immediately before the first mutation.
	discovered, err := DiscoverTopology(
		ctx,
		runner,
		DiscoveryInput{
			RootMountpoint:            top.RootMountpoint,
			ExpectedDiskPath:          top.DiskPath,
			ExpectedRootPartitionPath: top.RootPartitionPath,
			ExpectedMappingPath:       top.MappingPath,
		},
	)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"rediscover topology before recovery resize: %w",
			err,
		)
	}

	if err := sameExecutionIdentity(top, discovered); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"topology changed before recovery resize: %w",
			err,
		)
	}

	fmt.Fprintln(input.Out, "STAGE: resizing root storage")

	resizeTarget := fmt.Sprintf(
		"%d:%d",
		top.BtrfsDeviceID,
		plan.TargetBtrfsDeviceBytes,
	)

	if err := runner.Run(
		ctx,
		"sudo",
		"btrfs",
		"filesystem",
		"resize",
		resizeTarget,
		top.RootMountpoint,
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"shrink Btrfs root filesystem: %w",
			err,
		)
	}

	if err := verifyBtrfsAfterShrink(
		ctx,
		runner,
		top,
		plan,
	); err != nil {
		return ExecuteResult{}, err
	}

	if err := runner.Run(ctx, "sudo", "sync"); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"sync shrunk root filesystem: %w",
			err,
		)
	}

	if err := runner.Run(
		ctx,
		"sudo",
		"umount",
		top.RootMountpoint,
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"unmount shrunk root filesystem: %w",
			err,
		)
	}

	// Fail closed if /mnt still resolves to a mounted source.
	mounted, err := runner.Output(
		ctx,
		"findmnt",
		"-nvro",
		"SOURCE",
		"--mountpoint",
		top.RootMountpoint,
	)
	if err == nil && strings.TrimSpace(string(mounted)) != "" {
		return ExecuteResult{}, errors.New(
			"root target remained mounted after unmount",
		)
	}

	if err := runner.Run(
		ctx,
		"sudo",
		"cryptsetup",
		"close",
		top.MappingName,
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"close LUKS mapping before GPT resize: %w",
			err,
		)
	}

	// The desired new end is a boundary immediately after the final usable
	// sector. Parted's resizepart takes the inclusive final sector.
	if plan.NewRootEndBytes%top.LogicalSectorBytes != 0 {
		return ExecuteResult{}, fmt.Errorf(
			"new root end %d is not sector aligned to %d",
			plan.NewRootEndBytes,
			top.LogicalSectorBytes,
		)
	}

	endSector := plan.NewRootEndBytes/
		top.LogicalSectorBytes - 1

	if endSector == 0 {
		return ExecuteResult{}, errors.New(
			"calculated GPT end sector is invalid",
		)
	}

	// GNU Parted --script deliberately answers NO to warnings, including
	// partition shrink confirmation. The destructive action has already
	// passed gjallarOS installer consent and identity validation, so perform
	// this single resizepart interactively with deterministic C-locale input.
	//
	// Only the partition END is changed. The start, PARTUUID and type GUID
	// are verified immediately afterwards.
	if err := runner.RunInput(
		ctx,
		[]byte("Yes\n"),
		"sudo",
		"env",
		"LC_ALL=C",
		"parted",
		"---pretend-input-tty",
		"--align",
		"optimal",
		top.DiskPath,
		"unit",
		"s",
		"resizepart",
		strconv.FormatUint(top.RootPartitionNumber, 10),
		fmt.Sprintf("%ds", endSector),
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"shorten root GPT partition end: %w",
			err,
		)
	}

	if err := runner.Run(
		ctx,
		"sudo",
		"partx",
		"--update",
		"--nr",
		strconv.FormatUint(top.RootPartitionNumber, 10),
		top.DiskPath,
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"update kernel root partition geometry: %w",
			err,
		)
	}

	if err := runner.Run(
		ctx,
		"sudo",
		"udevadm",
		"settle",
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"settle resized partition device: %w",
			err,
		)
	}

	if err := verifyPartitionAfterResize(
		ctx,
		runner,
		top,
		plan,
	); err != nil {
		return ExecuteResult{}, err
	}

	// Reopen from stdin only. The passphrase never enters argv/state/logs.
	if err := runner.RunInput(
		ctx,
		secret,
		"sudo",
		"cryptsetup",
		"open",
		"--type",
		"luks2",
		"--key-file",
		"-",
		top.RootPartitionPath,
		top.MappingName,
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"reopen resized LUKS root: %w",
			err,
		)
	}

	luksUUID, err := outputTrim(
		ctx,
		runner,
		"sudo",
		"cryptsetup",
		"luksUUID",
		top.RootPartitionPath,
	)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"verify resized LUKS UUID: %w",
			err,
		)
	}
	if !strings.EqualFold(
		luksUUID,
		top.LUKSUUID,
	) {
		return ExecuteResult{}, fmt.Errorf(
			"LUKS UUID changed after resize: got=%q expected=%q",
			luksUUID,
			top.LUKSUUID,
		)
	}

	if err := runner.Run(
		ctx,
		"sudo",
		"mount",
		top.MappingPath,
		top.RootMountpoint,
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"remount resized Btrfs root: %w",
			err,
		)
	}

	if err := verifyBtrfsAfterShrink(
		ctx,
		runner,
		top,
		plan,
	); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"verify remounted resized Btrfs root: %w",
			err,
		)
	}

	fmt.Fprintln(
		input.Out,
		"PASS: root storage resized and identity preserved",
	)

	return ExecuteResult{
		Stage:                  StageResizingRootStorage,
		RootPartitionPath:      top.RootPartitionPath,
		RootPartitionSizeBytes: plan.NewRootSizeBytes,
		RootPARTUUID:           top.RootPARTUUID,
		LUKSUUID:               top.LUKSUUID,
		BtrfsUUID:              top.BtrfsUUID,
	}, nil
}

func validateExecutionPlan(
	top Topology,
	plan Plan,
) error {
	if plan.Stage != StagePreparingResize {
		return fmt.Errorf(
			"resize plan stage %q is not executable",
			plan.Stage,
		)
	}

	if plan.OriginalRootStartBytes != top.RootStartBytes ||
		plan.OriginalRootEndBytes != top.RootEndBytes ||
		plan.OriginalRootSizeBytes != top.RootSizeBytes {
		return errors.New(
			"resize plan no longer matches current root geometry",
		)
	}

	if plan.NewRootStartBytes != top.RootStartBytes {
		return errors.New(
			"resize plan attempts to move root partition start",
		)
	}

	if plan.NewRootEndBytes >= top.RootEndBytes {
		return errors.New(
			"resize plan does not reduce root partition end",
		)
	}

	if plan.ExpectedDiskGUID != top.DiskGUID ||
		plan.ExpectedPARTUUID != top.RootPARTUUID ||
		plan.ExpectedLUKSUUID != top.LUKSUUID ||
		plan.ExpectedBtrfsUUID != top.BtrfsUUID {
		return errors.New(
			"resize plan identity does not match current topology",
		)
	}

	return nil
}

func sameExecutionIdentity(
	expected Topology,
	actual Topology,
) error {
	checks := []struct {
		name string
		a    string
		b    string
	}{
		{"disk GUID", actual.DiskGUID, expected.DiskGUID},
		{"PARTUUID", actual.RootPARTUUID, expected.RootPARTUUID},
		{"partition type GUID", actual.RootPartitionTypeGUID, expected.RootPartitionTypeGUID},
		{"LUKS UUID", actual.LUKSUUID, expected.LUKSUUID},
		{"Btrfs UUID", actual.BtrfsUUID, expected.BtrfsUUID},
	}
	for _, check := range checks {
		if !strings.EqualFold(
			strings.TrimSpace(check.a),
			strings.TrimSpace(check.b),
		) {
			return fmt.Errorf(
				"%s mismatch: got=%q expected=%q",
				check.name,
				check.a,
				check.b,
			)
		}
	}

	if actual.RootPartitionNumber != expected.RootPartitionNumber ||
		actual.RootStartBytes != expected.RootStartBytes ||
		actual.RootEndBytes != expected.RootEndBytes ||
		actual.RootSizeBytes != expected.RootSizeBytes {
		return errors.New(
			"partition geometry changed",
		)
	}

	return nil
}

func verifyBtrfsAfterShrink(
	ctx context.Context,
	runner ExecutorRunner,
	top Topology,
	plan Plan,
) error {
	raw, err := runner.Output(
		ctx,
		"btrfs",
		"filesystem",
		"show",
		"--raw",
		top.RootMountpoint,
	)
	if err != nil {
		return fmt.Errorf(
			"verify Btrfs after shrink: %w",
			err,
		)
	}

	uuid, deviceID, size, _, devicePath, err :=
		parseBtrfsFilesystemShow(string(raw))
	if err != nil {
		return fmt.Errorf(
			"parse Btrfs verification: %w",
			err,
		)
	}

	if !strings.EqualFold(uuid, top.BtrfsUUID) {
		return fmt.Errorf(
			"Btrfs UUID changed after shrink: got=%q expected=%q",
			uuid,
			top.BtrfsUUID,
		)
	}

	if deviceID != top.BtrfsDeviceID {
		return fmt.Errorf(
			"Btrfs device ID changed after shrink: got=%d expected=%d",
			deviceID,
			top.BtrfsDeviceID,
		)
	}

	if size != plan.TargetBtrfsDeviceBytes {
		return fmt.Errorf(
			"Btrfs device size mismatch after shrink: got=%d expected=%d",
			size,
			plan.TargetBtrfsDeviceBytes,
		)
	}

	if filepath.Clean(devicePath) !=
		filepath.Clean(top.MappingPath) {
		return fmt.Errorf(
			"Btrfs device path changed: got=%q expected=%q",
			devicePath,
			top.MappingPath,
		)
	}

	return nil
}

func verifyPartitionAfterResize(
	ctx context.Context,
	runner ExecutorRunner,
	top Topology,
	plan Plan,
) error {
	diskGUID, err := outputTrim(
		ctx,
		runner,
		"lsblk",
		"-dnro",
		"PTUUID",
		top.DiskPath,
	)
	if err != nil {
		return fmt.Errorf(
			"verify disk GUID after resize: %w",
			err,
		)
	}
	if !strings.EqualFold(diskGUID, top.DiskGUID) {
		return fmt.Errorf(
			"disk GUID changed after resize: got=%q expected=%q",
			diskGUID,
			top.DiskGUID,
		)
	}

	partUUID, err := outputTrim(
		ctx,
		runner,
		"lsblk",
		"-dnro",
		"PARTUUID",
		top.RootPartitionPath,
	)
	if err != nil {
		return fmt.Errorf(
			"verify PARTUUID after resize: %w",
			err,
		)
	}
	if !strings.EqualFold(partUUID, top.RootPARTUUID) {
		return fmt.Errorf(
			"PARTUUID changed after resize: got=%q expected=%q",
			partUUID,
			top.RootPARTUUID,
		)
	}

	partType, err := outputTrim(
		ctx,
		runner,
		"lsblk",
		"-dnro",
		"PARTTYPE",
		top.RootPartitionPath,
	)
	if err != nil {
		return fmt.Errorf(
			"verify partition type after resize: %w",
			err,
		)
	}
	if !strings.EqualFold(
		partType,
		top.RootPartitionTypeGUID,
	) {
		return fmt.Errorf(
			"partition type GUID changed after resize: got=%q expected=%q",
			partType,
			top.RootPartitionTypeGUID,
		)
	}

	startSectors, err := outputUint(
		ctx,
		runner,
		"lsblk",
		"-dnro",
		"START",
		top.RootPartitionPath,
	)
	if err != nil {
		return fmt.Errorf(
			"verify partition start after resize: %w",
			err,
		)
	}

	startBytes, err := multiply(
		startSectors,
		top.LogicalSectorBytes,
	)
	if err != nil {
		return err
	}
	if startBytes != top.RootStartBytes {
		return fmt.Errorf(
			"root partition start moved: got=%d expected=%d",
			startBytes,
			top.RootStartBytes,
		)
	}

	size, err := outputUint(
		ctx,
		runner,
		"blockdev",
		"--getsize64",
		top.RootPartitionPath,
	)
	if err != nil {
		return fmt.Errorf(
			"verify root partition size after resize: %w",
			err,
		)
	}
	if size != plan.NewRootSizeBytes {
		return fmt.Errorf(
			"root partition size mismatch after resize: got=%d expected=%d",
			size,
			plan.NewRootSizeBytes,
		)
	}

	return nil
}

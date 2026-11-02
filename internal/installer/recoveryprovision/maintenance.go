package recoveryprovision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/gptprovision"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryresize"
)

var evalSymlinks = filepath.EvalSymlinks

type MaintenanceInput struct {
	Plan       diskplan.Plan
	Passphrase []byte
	Out        io.Writer
}

type MaintenanceResult struct {
	RecoveryPartition string
}

func ExecuteMaintenance(
	ctx context.Context,
	runner recoveryresize.ExecutorRunner,
	input MaintenanceInput,
) (MaintenanceResult, error) {
	if input.Out == nil {
		input.Out = io.Discard
	}
	if len(input.Passphrase) == 0 {
		return MaintenanceResult{}, errors.New(
			"LUKS passphrase is required for recovery maintenance",
		)
	}
	if input.Plan.Recovery == nil ||
		input.Plan.Recovery.SizeBytes != RecoveryBytes {
		return MaintenanceResult{}, errors.New(
			"maintenance plan requires exact 12 GiB recovery storage",
		)
	}

	// cryptsetup status reports the kernel node (/dev/vda2), so the stable
	// by-partuuid link must be resolved before discovery compares them
	// (e2e-target, 2026-10-05: `LUKS backing-device mismatch`).
	rootPart, err := evalSymlinks(
		"/dev/disk/by-partuuid/" + input.Plan.Root.Partition.PARTUUID,
	)
	if err != nil {
		return MaintenanceResult{}, fmt.Errorf(
			"resolve root partition: %w",
			err,
		)
	}

	mapping := "/dev/mapper/" +
		input.Plan.Root.Encryption.MappingName

	topology, err := recoveryresize.DiscoverTopology(
		ctx,
		runner,
		recoveryresize.DiscoveryInput{
			RootMountpoint:            recoveryresize.FreshInstallerTargetMountpoint,
			ExpectedDiskPath:          input.Plan.TargetDisk.Path,
			ExpectedRootPartitionPath: rootPart,
			ExpectedMappingPath:       mapping,
		},
	)
	if err != nil {
		return MaintenanceResult{}, fmt.Errorf(
			"discover maintenance topology: %w",
			err,
		)
	}

	resizePlan, err := recoveryresize.BuildPlan(
		topology,
		recoveryresize.Requirements{
			RecoveryBytes:           RecoveryBytes,
			SafetyMarginBytes:       GPTSafetyMarginBytes,
			FilesystemHeadroomBytes: FilesystemHeadroomBytes,
			AlignmentBytes:          AlignmentBytes,
		},
	)
	if err != nil {
		return MaintenanceResult{}, fmt.Errorf(
			"build maintenance resize plan: %w",
			err,
		)
	}

	if _, err := recoveryresize.ExecuteFreshInstallerResize(
		ctx,
		runner,
		recoveryresize.ExecuteInput{
			Topology:   topology,
			Plan:       resizePlan,
			Passphrase: input.Passphrase,
			Out:        input.Out,
		},
	); err != nil {
		return MaintenanceResult{}, fmt.Errorf(
			"resize encrypted Btrfs root: %w",
			err,
		)
	}

	gpt, err := gptprovision.ProvisionWithRunner(
		ctx,
		runner,
		gptprovision.Input{
			Plan:       input.Plan,
			UI:         prompt.New(nil, input.Out),
			Out:        input.Out,
			Unattended: true,
		},
	)
	if err != nil {
		return MaintenanceResult{}, fmt.Errorf(
			"create recovery GPT after resize: %w",
			err,
		)
	}

	if gpt.Status != gptprovision.StatusCreated &&
		gpt.Status != gptprovision.StatusAlreadyPresent {
		return MaintenanceResult{}, fmt.Errorf(
			"unexpected post-resize GPT state %q",
			gpt.Status,
		)
	}

	fmt.Fprintf(
		input.Out,
		"PASS: exact 12 GiB recovery partition ready at %s\n",
		gpt.PartitionPath,
	)

	return MaintenanceResult{
		RecoveryPartition: gpt.PartitionPath,
	}, nil
}

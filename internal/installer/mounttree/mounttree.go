// Package mounttree prepares the fresh-install filesystem tree below /mnt.
//
// The encrypted root itself is created and mounted by rootprovision. This
// package verifies that root mount, formats the ESP, optionally prepares the
// planned recovery filesystem, mounts child filesystems deterministically, and
// verifies every mount before returning.
package mounttree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
)

const targetRoot = "/mnt"

type Input struct {
	Plan diskplan.Plan
	Out  io.Writer
}

type Result struct {
	RootMount     string
	ESPMount      string
	RecoveryMount string
}

type commandRunner interface {
	Run(context.Context, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func (execRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func Prepare(ctx context.Context, input Input) (Result, error) {
	return prepare(ctx, input, execRunner{})
}

func prepare(
	ctx context.Context,
	input Input,
	runner commandRunner,
) (Result, error) {
	if input.Out == nil {
		return Result{}, fmt.Errorf("mount-tree output writer is required")
	}
	if err := input.Plan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate canonical disk plan: %w", err)
	}

	mapper := "/dev/mapper/" + input.Plan.Root.Encryption.MappingName
	if err := verifyMount(ctx, runner, targetRoot, mapper, ""); err != nil {
		return Result{}, fmt.Errorf(
			"encrypted root must already be mounted by root provisioning: %w",
			err,
		)
	}

	espMount := targetMount(input.Plan.ESP.MountPoint)
	if espMount != "/mnt/boot" {
		return Result{}, fmt.Errorf(
			"ESP mount must resolve to /mnt/boot, got %s",
			espMount,
		)
	}

	espDevice, err := resolvePartition(ctx, runner, input.Plan.ESP)
	if err != nil {
		return Result{}, fmt.Errorf("resolve ESP: %w", err)
	}
	if err := requireFreshPartition(ctx, runner, input.Plan.ESP, espDevice); err != nil {
		return Result{}, fmt.Errorf("validate ESP: %w", err)
	}
	if !isFAT(input.Plan.ESP.Filesystem.Type) {
		return Result{}, fmt.Errorf(
			"ESP filesystem must be vfat/fat32, got %q",
			input.Plan.ESP.Filesystem.Type,
		)
	}

	if err := makeFAT(ctx, runner, espDevice, input.Plan.ESP.Filesystem.Label); err != nil {
		return Result{}, fmt.Errorf("format ESP: %w", err)
	}
	if err := privileged(ctx, runner, "mkdir", "-p", espMount); err != nil {
		return Result{}, fmt.Errorf("create ESP mount point: %w", err)
	}
	if err := privileged(ctx, runner, "mount", espDevice, espMount); err != nil {
		return Result{}, fmt.Errorf("mount ESP: %w", err)
	}

	espMounted := true
	cleanupESP := func() {
		if espMounted {
			_ = privileged(ctx, runner, "umount", espMount)
		}
	}

	if err := verifyMount(ctx, runner, espMount, espDevice, "vfat"); err != nil {
		cleanupESP()
		return Result{}, fmt.Errorf("verify ESP mount: %w", err)
	}

	result := Result{
		RootMount: targetRoot,
		ESPMount:  espMount,
	}

	if input.Plan.Recovery != nil {
		recovery := *input.Plan.Recovery
		recoveryMount := targetMount(recovery.MountPoint)

		if recoveryMount != "/mnt/recovery" {
			cleanupESP()
			return Result{}, fmt.Errorf(
				"recovery mount must resolve to /mnt/recovery, got %s",
				recoveryMount,
			)
		}
		if !isFAT(recovery.Filesystem.Type) {
			cleanupESP()
			return Result{}, fmt.Errorf(
				"recovery filesystem must be vfat/fat32, got %q",
				recovery.Filesystem.Type,
			)
		}

		recoveryDevice, err := resolvePartition(ctx, runner, recovery)
		if err != nil {
			cleanupESP()
			return Result{}, fmt.Errorf("resolve recovery partition: %w", err)
		}
		if err := requireFreshPartition(ctx, runner, recovery, recoveryDevice); err != nil {
			cleanupESP()
			return Result{}, fmt.Errorf("validate recovery partition: %w", err)
		}

		// FAT volume labels are limited to 11 characters.
		recoveryLabel := strings.TrimSpace(recovery.Filesystem.Label)
		if len(recoveryLabel) > 11 {
			cleanupESP()
			return Result{}, fmt.Errorf(
				"recovery FAT filesystem label %q exceeds 11 characters",
				recovery.Filesystem.Label,
			)
		}

		if err := makeFAT(ctx, runner, recoveryDevice, recoveryLabel); err != nil {
			cleanupESP()
			return Result{}, fmt.Errorf("format recovery filesystem: %w", err)
		}
		if err := privileged(ctx, runner, "mkdir", "-p", recoveryMount); err != nil {
			cleanupESP()
			return Result{}, fmt.Errorf("create recovery mount point: %w", err)
		}
		if err := privileged(ctx, runner, "mount", recoveryDevice, recoveryMount); err != nil {
			cleanupESP()
			return Result{}, fmt.Errorf("mount recovery filesystem: %w", err)
		}

		if err := verifyMount(
			ctx,
			runner,
			recoveryMount,
			recoveryDevice,
			"vfat",
		); err != nil {
			_ = privileged(ctx, runner, "umount", recoveryMount)
			cleanupESP()
			return Result{}, fmt.Errorf("verify recovery mount: %w", err)
		}

		result.RecoveryMount = recoveryMount
	}

	fmt.Fprintf(
		input.Out,
		"PASS: fresh install mount tree ready: root=%s esp=%s",
		result.RootMount,
		result.ESPMount,
	)
	if result.RecoveryMount != "" {
		fmt.Fprintf(input.Out, " recovery=%s", result.RecoveryMount)
	}
	fmt.Fprintln(input.Out)

	return result, nil
}

func targetMount(planMount string) string {
	clean := filepath.Clean(planMount)
	return filepath.Join(targetRoot, strings.TrimPrefix(clean, "/"))
}

func isFAT(fs string) bool {
	switch strings.ToLower(strings.TrimSpace(fs)) {
	case "vfat", "fat", "fat32":
		return true
	default:
		return false
	}
}

func makeFAT(
	ctx context.Context,
	runner commandRunner,
	device string,
	label string,
) error {
	args := []string{"mkfs.vfat", "-F", "32"}
	if label = strings.TrimSpace(label); label != "" {
		if len(label) > 11 {
			return fmt.Errorf("FAT filesystem label %q exceeds 11 characters", label)
		}
		args = append(args, "-n", label)
	}
	args = append(args, device)
	return privileged(ctx, runner, args...)
}

func privileged(
	ctx context.Context,
	runner commandRunner,
	args ...string,
) error {
	return runner.Run(ctx, "sudo", args...)
}

func resolvePartition(
	ctx context.Context,
	runner commandRunner,
	part diskplan.Partition,
) (string, error) {
	stable := "/dev/disk/by-partuuid/" + strings.ToLower(part.PARTUUID)
	raw, err := runner.Output(ctx, "readlink", "-f", stable)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", stable, err)
	}
	device := strings.TrimSpace(string(raw))
	if !filepath.IsAbs(device) {
		return "", fmt.Errorf(
			"PARTUUID %s resolved to invalid device %q",
			part.PARTUUID,
			device,
		)
	}
	return device, nil
}

type blockTree struct {
	BlockDevices []blockDevice `json:"blockdevices"`
}

type blockDevice struct {
	Path        string   `json:"path"`
	Type        string   `json:"type"`
	Size        uint64   `json:"size"`
	FSType      string   `json:"fstype"`
	PartUUID    string   `json:"partuuid"`
	MountPoints []string `json:"mountpoints"`
}

func requireFreshPartition(
	ctx context.Context,
	runner commandRunner,
	expected diskplan.Partition,
	device string,
) error {
	raw, err := runner.Output(
		ctx,
		"lsblk",
		"-J",
		"-b",
		"-p",
		// Without NAME among the columns, lsblk -J lists children (partitions,
		// crypt mappings) as flat siblings; --tree keeps them nested.
		"--tree",
		"-o",
		"PATH,TYPE,SIZE,FSTYPE,PARTUUID,MOUNTPOINTS",
		"--",
		device,
	)
	if err != nil {
		return fmt.Errorf("inspect partition: %w", err)
	}

	var tree blockTree
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&tree); err != nil {
		return fmt.Errorf("decode partition metadata: %w", err)
	}
	if len(tree.BlockDevices) != 1 {
		return fmt.Errorf(
			"partition inspection returned %d devices",
			len(tree.BlockDevices),
		)
	}

	actual := tree.BlockDevices[0]
	if actual.Type != "part" {
		return fmt.Errorf("%s is not a partition", device)
	}
	if !strings.EqualFold(actual.PartUUID, expected.PARTUUID) {
		return fmt.Errorf(
			"PARTUUID changed: expected %s, got %s",
			expected.PARTUUID,
			actual.PartUUID,
		)
	}
	if actual.Size != expected.SizeBytes {
		return fmt.Errorf(
			"partition size changed: expected %d, got %d",
			expected.SizeBytes,
			actual.Size,
		)
	}
	if strings.TrimSpace(actual.FSType) != "" {
		return fmt.Errorf(
			"partition already contains filesystem/signature %q",
			actual.FSType,
		)
	}
	for _, mount := range actual.MountPoints {
		if strings.TrimSpace(mount) != "" {
			return fmt.Errorf("partition already mounted at %s", mount)
		}
	}

	return nil
}

func verifyMount(
	ctx context.Context,
	runner commandRunner,
	mountPoint string,
	expectedSource string,
	expectedFS string,
) error {
	raw, err := runner.Output(
		ctx,
		"findmnt",
		"-nvro",
		"SOURCE,FSTYPE",
		"--mountpoint",
		mountPoint,
	)
	if err != nil {
		return fmt.Errorf("%s is not mounted: %w", mountPoint, err)
	}

	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return fmt.Errorf(
			"unexpected findmnt result for %s: %q",
			mountPoint,
			strings.TrimSpace(string(raw)),
		)
	}

	if fields[0] != expectedSource {
		return fmt.Errorf(
			"%s source mismatch: expected %s, got %s",
			mountPoint,
			expectedSource,
			fields[0],
		)
	}
	if expectedFS != "" && !strings.EqualFold(fields[1], expectedFS) {
		return fmt.Errorf(
			"%s filesystem mismatch: expected %s, got %s",
			mountPoint,
			expectedFS,
			fields[1],
		)
	}

	return nil
}

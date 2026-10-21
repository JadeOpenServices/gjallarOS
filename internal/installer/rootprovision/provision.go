// Package rootprovision creates the encrypted GjallarOS root filesystem.
//
// Secrets are supplied to cryptsetup only through stdin. They are never part
// of the canonical disk plan, command-line arguments, generated Nix
// configuration, or package output.
package rootprovision

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

const mountPoint = "/mnt"

// snapshotsPath holds the read-only snapshots of @home and @config.
const snapshotsPath = "/.snapshots"

// Subvolume is one btrfs subvolume of a new install and where it is mounted.
type Subvolume struct {
	Name    string
	Path    string
	Options []string
}

// Subvolumes is the layout of a new install, parents before children (ROADMAP
// section 15). Only @home and @config are ever snapshotted: the system comes
// back from NixOS generations, and / and /var/lib, where the secrets live,
// never go into a snapshot. @config is the system checkout with
// user.config.json and generated/; /etc/gjallar itself stays on @ because
// NixOS writes its own links there.
var Subvolumes = []Subvolume{
	{Name: "@", Path: "/"},
	{Name: "@home", Path: "/home"},
	{Name: "@nix", Path: "/nix"},
	{Name: "@config", Path: "/etc/gjallar/system"},
	{Name: "@snapshots", Path: snapshotsPath, Options: []string{"nosuid", "nodev", "noexec"}},
}

type Input struct {
	Plan       diskplan.Plan
	Passphrase []byte
	Out        io.Writer
}

type Result struct {
	PartitionPath string
	MapperPath    string
	MountPoint    string
	Filesystem    string
	LUKSUUID      string
	StableDevice  string
}

type commandRunner interface {
	Run(context.Context, string, ...string) error
	RunInput(context.Context, []byte, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func (execRunner) RunInput(
	ctx context.Context,
	input []byte,
	name string,
	args ...string,
) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Run()
}

func (execRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func Provision(ctx context.Context, input Input) (Result, error) {
	return provision(ctx, input, execRunner{})
}

func provision(
	ctx context.Context,
	input Input,
	runner commandRunner,
) (Result, error) {
	if input.Out == nil {
		return Result{}, fmt.Errorf("root provisioning output writer is required")
	}
	if err := input.Plan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate canonical disk plan: %w", err)
	}
	if len(input.Passphrase) == 0 {
		return Result{}, fmt.Errorf("LUKS passphrase is required")
	}

	secret := append([]byte(nil), input.Passphrase...)
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()

	root := input.Plan.Root
	part := root.Partition

	stablePart := "/dev/disk/by-partuuid/" + strings.ToLower(part.PARTUUID)
	raw, err := runner.Output(ctx, "readlink", "-f", stablePart)
	if err != nil {
		return Result{}, fmt.Errorf(
			"resolve canonical root partition %s: %w",
			stablePart,
			err,
		)
	}
	partition := strings.TrimSpace(string(raw))
	if !filepath.IsAbs(partition) {
		return Result{}, fmt.Errorf(
			"canonical root partition resolved to invalid path %q",
			partition,
		)
	}

	observed, err := inspectPartition(ctx, runner, partition)
	if err != nil {
		return Result{}, err
	}
	if err := validatePartition(part, observed); err != nil {
		return Result{}, err
	}

	if mounted, err := exactMountExists(ctx, runner, mountPoint); err != nil {
		return Result{}, err
	} else if mounted {
		return Result{}, fmt.Errorf("%s is already a mount point", mountPoint)
	}

	fs := strings.ToLower(strings.TrimSpace(part.Filesystem.Type))
	if fs != "btrfs" {
		return Result{}, fmt.Errorf(
			"fresh GjallarOS root provisioning requires Btrfs; requested filesystem %q",
			part.Filesystem.Type,
		)
	}

	mapperPath := "/dev/mapper/" + root.Encryption.MappingName

	fmt.Fprintf(
		input.Out,
		"PROVISION: LUKS2 root %s -> %s -> %s (%s)\n",
		partition,
		mapperPath,
		mountPoint,
		fs,
	)

	// Human passphrase becomes the initial LUKS2 keyslot. No TPM enrollment
	// happens here; later diskcrypto/systemd tooling can enroll TPM2 while
	// retaining this recovery-capable human credential.
	if err := runner.RunInput(
		ctx,
		secret,
		"sudo",
		"cryptsetup",
		"luksFormat",
		"--type",
		"luks2",
		"--batch-mode",
		"--key-file",
		"-",
		partition,
	); err != nil {
		return Result{}, fmt.Errorf("create LUKS2 root container: %w", err)
	}

	opened := false
	mounted := false

	cleanup := func() {
		if mounted {
			_ = runner.Run(ctx, "sudo", "umount", "-R", mountPoint)
		}
		if opened {
			_ = runner.Run(
				ctx,
				"sudo",
				"cryptsetup",
				"close",
				root.Encryption.MappingName,
			)
		}
	}

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
		partition,
		root.Encryption.MappingName,
	); err != nil {
		return Result{}, fmt.Errorf("open LUKS2 root container: %w", err)
	}
	opened = true

	label := strings.TrimSpace(part.Filesystem.Label)

	args := []string{"mkfs.btrfs", "-f"}
	if label != "" {
		args = append(args, "-L", label)
	}
	args = append(args, mapperPath)
	if err := privilegedRun(ctx, runner, args...); err != nil {
		cleanup()
		return Result{}, fmt.Errorf("format btrfs root filesystem: %w", err)
	}

	if err := privilegedRun(ctx, runner, "mkdir", "-p", mountPoint); err != nil {
		cleanup()
		return Result{}, fmt.Errorf("create root mount point: %w", err)
	}

	if err := makeSubvolumes(ctx, runner, mapperPath); err != nil {
		cleanup()
		return Result{}, err
	}

	for _, sub := range Subvolumes {
		target := filepath.Join(mountPoint, sub.Path)
		if sub.Path != "/" {
			if err := privilegedRun(ctx, runner, "mkdir", "-p", target); err != nil {
				cleanup()
				return Result{}, fmt.Errorf("create mount point %s: %w", target, err)
			}
		}
		options := strings.Join(append([]string{"subvol=" + sub.Name}, sub.Options...), ",")
		if err := privilegedRun(ctx, runner, "mount", "-o", options, mapperPath, target); err != nil {
			cleanup()
			return Result{}, fmt.Errorf("mount subvolume %s at %s: %w", sub.Name, target, err)
		}
		mounted = true
		if err := verifySubvolume(ctx, runner, target, mapperPath, sub.Name); err != nil {
			cleanup()
			return Result{}, err
		}
	}

	// A plain mount of the root, as recovery tools do, gets @ and not the
	// top level.
	if err := privilegedRun(ctx, runner, "btrfs", "subvolume", "set-default", mountPoint); err != nil {
		cleanup()
		return Result{}, fmt.Errorf("make @ the default subvolume: %w", err)
	}

	// Only root may look into the snapshots: a snapshot keeps the modes
	// files had when it was taken.
	if err := privilegedRun(ctx, runner, "chmod", "0700", filepath.Join(mountPoint, snapshotsPath)); err != nil {
		cleanup()
		return Result{}, fmt.Errorf("restrict %s: %w", snapshotsPath, err)
	}

	uuidRaw, err := runner.Output(
		ctx,
		"sudo",
		"cryptsetup",
		"luksUUID",
		partition,
	)
	if err != nil {
		cleanup()
		return Result{}, fmt.Errorf("read LUKS2 UUID: %w", err)
	}
	luksUUID := strings.TrimSpace(string(uuidRaw))
	if luksUUID == "" {
		cleanup()
		return Result{}, fmt.Errorf("cryptsetup returned an empty LUKS UUID")
	}

	fmt.Fprintf(
		input.Out,
		"PASS: encrypted root mounted at %s; LUKS2 identity ready for future TPM enrollment\n",
		mountPoint,
	)

	return Result{
		PartitionPath: partition,
		MapperPath:    mapperPath,
		MountPoint:    mountPoint,
		Filesystem:    fs,
		LUKSUUID:      luksUUID,
		StableDevice:  "/dev/disk/by-uuid/" + luksUUID,
	}, nil
}

// makeSubvolumes creates every subvolume at the top level of the new file
// system, mounted at mountPoint only for that.
func makeSubvolumes(ctx context.Context, runner commandRunner, mapperPath string) error {
	if err := privilegedRun(ctx, runner, "mount", "-o", "subvolid=5", mapperPath, mountPoint); err != nil {
		return fmt.Errorf("mount new btrfs top level at %s: %w", mountPoint, err)
	}
	for _, sub := range Subvolumes {
		if err := privilegedRun(ctx, runner, "btrfs", "subvolume", "create", filepath.Join(mountPoint, sub.Name)); err != nil {
			_ = privilegedRun(ctx, runner, "umount", mountPoint)
			return fmt.Errorf("create subvolume %s: %w", sub.Name, err)
		}
	}
	if err := privilegedRun(ctx, runner, "umount", mountPoint); err != nil {
		return fmt.Errorf("unmount new btrfs top level: %w", err)
	}
	return nil
}

// verifySubvolume checks that target is the encrypted root's subvolume name.
func verifySubvolume(ctx context.Context, runner commandRunner, target, mapperPath, name string) error {
	raw, err := runner.Output(ctx, "findmnt", "-nvro", "SOURCE,FSROOT", "--mountpoint", target)
	if err != nil {
		return fmt.Errorf("verify mount %s: %w", target, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || fields[0] != mapperPath || fields[1] != "/"+name {
		return fmt.Errorf(
			"mount %s mismatch: expected %s /%s, got %q",
			target, mapperPath, name, strings.TrimSpace(string(raw)),
		)
	}
	return nil
}

func privilegedRun(
	ctx context.Context,
	runner commandRunner,
	args ...string,
) error {
	return runner.Run(ctx, "sudo", args...)
}

func exactMountExists(
	ctx context.Context,
	runner commandRunner,
	path string,
) (bool, error) {
	_, err := runner.Output(
		ctx,
		"findmnt",
		"-n",
		"--mountpoint",
		path,
	)
	if err == nil {
		return true, nil
	}

	type exitCoder interface {
		ExitCode() int
	}

	status, ok := err.(exitCoder)
	if !ok || status.ExitCode() != 1 {
		return false, fmt.Errorf(
			"inspect mount point %s: %w",
			path,
			err,
		)
	}

	return false, nil
}

type partitionTree struct {
	BlockDevices []partitionDevice `json:"blockdevices"`
}

type partitionDevice struct {
	Path        string   `json:"path"`
	Type        string   `json:"type"`
	Size        uint64   `json:"size"`
	FSType      string   `json:"fstype"`
	PartUUID    string   `json:"partuuid"`
	MountPoints []string `json:"mountpoints"`
}

func inspectPartition(
	ctx context.Context,
	runner commandRunner,
	partition string,
) (partitionDevice, error) {
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
		partition,
	)
	if err != nil {
		return partitionDevice{}, fmt.Errorf("inspect root partition: %w", err)
	}

	var tree partitionTree
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&tree); err != nil {
		return partitionDevice{}, fmt.Errorf("decode root partition metadata: %w", err)
	}
	if len(tree.BlockDevices) != 1 {
		return partitionDevice{}, fmt.Errorf(
			"root partition resolved to %d block devices",
			len(tree.BlockDevices),
		)
	}

	return tree.BlockDevices[0], nil
}

func validatePartition(
	expected diskplan.Partition,
	actual partitionDevice,
) error {
	if strings.TrimSpace(actual.Type) != "part" {
		return fmt.Errorf("canonical root target is not a partition")
	}
	if !strings.EqualFold(
		strings.TrimSpace(actual.PartUUID),
		expected.PARTUUID,
	) {
		return fmt.Errorf("canonical root PARTUUID changed")
	}
	if actual.Size != expected.SizeBytes {
		return fmt.Errorf(
			"canonical root partition size changed: expected %d, got %d",
			expected.SizeBytes,
			actual.Size,
		)
	}
	if strings.TrimSpace(actual.FSType) != "" {
		return fmt.Errorf(
			"canonical root partition already contains filesystem/signature %q",
			actual.FSType,
		)
	}
	for _, mount := range actual.MountPoints {
		if strings.TrimSpace(mount) != "" {
			return fmt.Errorf(
				"canonical root partition is already mounted at %s",
				mount,
			)
		}
	}
	return nil
}

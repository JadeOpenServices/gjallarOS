package recoveryprovision

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryresize"
)

const (
	efiSystemPartitionType = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	xbootldrType           = "bc13c2ff-59e6-4262-a352-b275fd6f7172"
)

// RecoveryPresentError reports that the target disk already carries recovery
// storage, so no plan (and no root shrink) is needed. Without this check the
// maintenance entry shrank root by 12 GiB on a disk that already had
// a recovery partition (e2e-target, 2026-10-05).
type RecoveryPresentError struct {
	Partition string
}

func (e *RecoveryPresentError) Error() string {
	return "recovery partition already present at " + e.Partition
}

type existingTree struct {
	BlockDevices []existingDevice `json:"blockdevices"`
}

type existingDevice struct {
	Path        string           `json:"path"`
	Type        string           `json:"type"`
	Size        uint64           `json:"size"`
	Model       string           `json:"model"`
	Serial      string           `json:"serial"`
	WWN         string           `json:"wwn"`
	PTType      string           `json:"pttype"`
	PTUUID      string           `json:"ptuuid"`
	PartN       uint             `json:"partn"`
	PartLabel   string           `json:"partlabel"`
	PartType    string           `json:"parttype"`
	PartUUID    string           `json:"partuuid"`
	FSType      string           `json:"fstype"`
	Mountpoints []string         `json:"mountpoints"`
	Children    []existingDevice `json:"children"`
}

func BuildExistingPlan(
	ctx context.Context,
	runner recoveryresize.OutputRunner,
	top recoveryresize.Topology,
) (diskplan.Plan, error) {
	if err := recoveryresize.ValidateTopology(top); err != nil {
		return diskplan.Plan{}, err
	}

	raw, err := runner.Output(
		ctx,
		"lsblk",
		"-J",
		"-b",
		"-p",
		"--tree",
		"-o",
		"PATH,TYPE,SIZE,MODEL,SERIAL,WWN,PTTYPE,PTUUID,PARTN,PARTLABEL,PARTTYPE,PARTUUID,FSTYPE,MOUNTPOINTS",
		"--",
		top.DiskPath,
	)
	if err != nil {
		return diskplan.Plan{}, fmt.Errorf("inspect existing GPT layout: %w", err)
	}

	var tree existingTree
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&tree); err != nil {
		return diskplan.Plan{}, fmt.Errorf("decode existing GPT layout: %w", err)
	}

	var disk *existingDevice
	for i := range tree.BlockDevices {
		candidate := &tree.BlockDevices[i]
		if candidate.Type == "disk" &&
			filepath.Clean(candidate.Path) == filepath.Clean(top.DiskPath) {
			if disk != nil {
				return diskplan.Plan{}, errors.New("target disk resolved more than once")
			}
			disk = candidate
		}
	}

	if disk == nil {
		return diskplan.Plan{}, fmt.Errorf("target disk %s not found", top.DiskPath)
	}
	if !strings.EqualFold(strings.TrimSpace(disk.PTType), "gpt") {
		return diskplan.Plan{}, errors.New("recovery partition provisioning requires GPT")
	}
	if !strings.EqualFold(strings.TrimSpace(disk.PTUUID), strings.TrimSpace(top.DiskGUID)) {
		return diskplan.Plan{}, errors.New("GPT disk GUID changed during planning")
	}
	if strings.TrimSpace(disk.Serial) == "" && strings.TrimSpace(disk.WWN) == "" {
		return diskplan.Plan{}, errors.New("target disk has neither serial nor WWN")
	}

	used := map[uint]bool{}
	var esp *existingDevice
	existingRecovery := ""

	var walk func([]existingDevice) error
	walk = func(devices []existingDevice) error {
		for i := range devices {
			dev := &devices[i]

			if dev.Type == "part" {
				if dev.PartN == 0 {
					return fmt.Errorf("partition %s has no GPT partition number", dev.Path)
				}

				used[dev.PartN] = true

				if strings.EqualFold(
					strings.TrimSpace(dev.PartType),
					efiSystemPartitionType,
				) {
					if esp != nil {
						return errors.New("multiple EFI System Partitions found on target disk")
					}
					copy := *dev
					esp = &copy
				}

				if existingRecovery == "" &&
					(strings.EqualFold(strings.TrimSpace(dev.PartType), xbootldrType) ||
						strings.TrimSpace(dev.PartLabel) == diskplan.RecoveryPartLabel) {
					existingRecovery = dev.Path
				}
			}

			if err := walk(dev.Children); err != nil {
				return err
			}
		}

		return nil
	}

	if err := walk(disk.Children); err != nil {
		return diskplan.Plan{}, err
	}

	if existingRecovery != "" {
		return diskplan.Plan{}, &RecoveryPresentError{Partition: existingRecovery}
	}

	if esp == nil {
		return diskplan.Plan{}, errors.New("existing GPT has no EFI System Partition")
	}

	recoveryNumber := uint(1)
	for used[recoveryNumber] {
		recoveryNumber++
	}

	recoveryUUID, err := uuidV4()
	if err != nil {
		return diskplan.Plan{}, fmt.Errorf("generate recovery PARTUUID: %w", err)
	}

	rootNumber := uint(top.RootPartitionNumber)
	if uint64(rootNumber) != top.RootPartitionNumber {
		return diskplan.Plan{}, errors.New("root partition number exceeds supported range")
	}

	plan := diskplan.Plan{
		SchemaVersion: diskplan.SchemaVersion,
		TargetDisk: diskplan.Disk{
			Path:        disk.Path,
			Model:       strings.TrimSpace(disk.Model),
			Serial:      strings.TrimSpace(disk.Serial),
			WWN:         strings.TrimSpace(disk.WWN),
			SizeBytes:   disk.Size,
			GPTDiskGUID: strings.ToLower(strings.TrimSpace(top.DiskGUID)),
		},
		ESP: diskplan.Partition{
			Role:       diskplan.RoleESP,
			Number:     esp.PartN,
			Label:      firstNonEmpty(esp.PartLabel, "EFI"),
			TypeGUID:   strings.ToLower(strings.TrimSpace(esp.PartType)),
			PARTUUID:   strings.ToLower(strings.TrimSpace(esp.PartUUID)),
			SizeBytes:  esp.Size,
			Filesystem: diskplan.Filesystem{Type: firstNonEmpty(esp.FSType, "vfat")},
			MountPoint: "/boot",
		},
		Root: diskplan.Root{
			Partition: diskplan.Partition{
				Role:      diskplan.RoleRoot,
				Number:    rootNumber,
				Label:     "GJALLAROS",
				TypeGUID:  strings.ToLower(strings.TrimSpace(top.RootPartitionTypeGUID)),
				PARTUUID:  strings.ToLower(strings.TrimSpace(top.RootPARTUUID)),
				SizeBytes: top.RootSizeBytes,
				Filesystem: diskplan.Filesystem{
					Type: recoveryresize.FilesystemBtrfs,
				},
				MountPoint: "/",
			},
			Encryption: diskplan.Encryption{
				Type:        diskplan.EncryptionLUKS2,
				MappingName: top.MappingName,
			},
		},
		Recovery: &diskplan.Partition{
			Role:      diskplan.RoleRecovery,
			Number:    recoveryNumber,
			Label:     diskplan.RecoveryPartLabel,
			TypeGUID:  xbootldrType,
			PARTUUID:  recoveryUUID,
			SizeBytes: RecoveryBytes,
			Filesystem: diskplan.Filesystem{
				Type:  "vfat",
				Label: diskplan.RecoveryFSLabel,
			},
			MountPoint: "/recovery",
		},
	}

	if err := plan.Validate(); err != nil {
		return diskplan.Plan{}, fmt.Errorf(
			"validate existing-layout recovery plan: %w",
			err,
		)
	}

	return plan, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func uuidV4() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}

	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	hexed := hex.EncodeToString(raw[:])

	return strings.Join([]string{
		hexed[0:8],
		hexed[8:12],
		hexed[12:16],
		hexed[16:20],
		hexed[20:32],
	}, "-"), nil
}

package freshdiskplan

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

const (
	ESPBytes      uint64 = 1024 * 1024 * 1024
	RecoveryBytes uint64 = 12 * 1024 * 1024 * 1024

	AlignmentBytes uint64 = 1024 * 1024

	efiSystemPartitionType = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	linuxLUKSType          = "ca7d7ccb-63ed-4c53-861c-1742536059cc"
	xbootldrType           = "bc13c2ff-59e6-4262-a352-b275fd6f7172"

	rootMappingName = "cryptroot"
)

// RootBytes is the size of the root partition Build lays out on a disk of
// diskBytes, or 0 when the disk is smaller than the fixed partitions.
func RootBytes(diskBytes uint64, recovery bool) uint64 {
	fixed := 2*AlignmentBytes + ESPBytes
	if recovery {
		fixed += RecoveryBytes
	}
	if diskBytes <= fixed {
		return 0
	}
	return alignDown(diskBytes-fixed, AlignmentBytes)
}

// MinimumDiskBytes is the smallest disk a fresh install takes (Q227): the
// ESP, the system, the install swap file, and room for local builds and the
// first update. It is checked before anything is erased, for a chosen disk
// and for --target-disk alike.
const MinimumDiskBytes uint64 = 40 << 30

// MinimumRecoveryDiskBytes is the smallest disk that gets the recovery
// partition (Plane 415, Q259): the partition comes on top of the minimum,
// so the root keeps the room a minimum install has. A 40 GiB disk with the
// partition left a 27 GiB root, and nixos-install ran out of space on it.
const MinimumRecoveryDiskBytes = MinimumDiskBytes + RecoveryBytes

// RecoveryFits tells whether a disk of diskBytes takes the recovery
// partition and still leaves the root a minimum install has.
func RecoveryFits(diskBytes uint64) bool {
	return RootBytes(diskBytes, true) >= RootBytes(MinimumDiskBytes, false)
}

// Installation swap: live media have no swap, and nixos-install was
// OOM-killed evaluating the configuration on a 12 GiB machine, so a fresh
// install puts a temporary swap file on the new root. A root below
// SmallSwapRoot (a disk near the 40 GiB minimum, Q227) gets the smaller
// file, so the system and the first update still fit.
const (
	InstallSwapBytes      uint64 = 8 << 30
	InstallSwapSmallBytes uint64 = 4 << 30
	SmallSwapRoot         uint64 = 48 << 30
)

// InstallSwap is the size of the temporary swap file for a root file
// system of rootBytes.
func InstallSwap(rootBytes uint64) uint64 {
	if rootBytes < SmallSwapRoot {
		return InstallSwapSmallBytes
	}
	return InstallSwapBytes
}

type Input struct {
	Observed       targetdisk.Result
	EnableRecovery bool
}

func Build(input Input) (diskplan.Plan, error) {
	observed := input.Observed

	if strings.TrimSpace(observed.Path) == "" {
		return diskplan.Plan{}, fmt.Errorf("validated target disk path is required")
	}
	if strings.TrimSpace(observed.Serial) == "" &&
		strings.TrimSpace(observed.WWN) == "" {
		return diskplan.Plan{}, fmt.Errorf(
			"validated target disk requires stable serial or WWN identity",
		)
	}
	if observed.SizeBytes == 0 {
		return diskplan.Plan{}, fmt.Errorf(
			"validated target disk size is required",
		)
	}

	diskGUID, err := uuidV4()
	if err != nil {
		return diskplan.Plan{}, fmt.Errorf("generate GPT disk GUID: %w", err)
	}
	espUUID, err := uuidV4()
	if err != nil {
		return diskplan.Plan{}, fmt.Errorf("generate ESP PARTUUID: %w", err)
	}
	rootUUID, err := uuidV4()
	if err != nil {
		return diskplan.Plan{}, fmt.Errorf("generate root PARTUUID: %w", err)
	}

	// Reserve one alignment unit at both the start and end. GPT itself needs
	// less space, but the explicit margin keeps every canonical boundary simple
	// and deterministic.
	reserved := 2 * AlignmentBytes
	required := reserved + ESPBytes
	if input.EnableRecovery {
		required += RecoveryBytes
	}

	// Leave at least 4 GiB for encrypted Btrfs root. This is only a structural
	// minimum; targetdisk/config policy may impose a larger machine minimum.
	const minimumRootBytes uint64 = 4 * 1024 * 1024 * 1024
	required += minimumRootBytes

	if observed.SizeBytes < required {
		return diskplan.Plan{}, fmt.Errorf(
			"target disk is too small for canonical gjallarOS layout: have=%d require-at-least=%d",
			observed.SizeBytes,
			required,
		)
	}

	// Q259: the question leaves the partition out on a smaller disk; this
	// stops a preset or any other path that still asks for it.
	if input.EnableRecovery && !RecoveryFits(observed.SizeBytes) {
		return diskplan.Plan{}, fmt.Errorf(
			"the recovery partition needs a disk of at least %d GiB, and this disk has %d bytes; nothing was changed",
			MinimumRecoveryDiskBytes>>30,
			observed.SizeBytes,
		)
	}

	rootBytes := RootBytes(observed.SizeBytes, input.EnableRecovery)

	if rootBytes < minimumRootBytes {
		return diskplan.Plan{}, fmt.Errorf(
			"canonical root partition would be too small: %d",
			rootBytes,
		)
	}

	plan := diskplan.Plan{
		SchemaVersion: diskplan.SchemaVersion,
		TargetDisk: diskplan.Disk{
			Path:        observed.Path,
			Model:       observed.Model,
			Serial:      observed.Serial,
			WWN:         observed.WWN,
			SizeBytes:   observed.SizeBytes,
			GPTDiskGUID: diskGUID,
		},
		ESP: diskplan.Partition{
			Role:      diskplan.RoleESP,
			Number:    1,
			Label:     "EFI",
			TypeGUID:  efiSystemPartitionType,
			PARTUUID:  espUUID,
			SizeBytes: ESPBytes,
			Filesystem: diskplan.Filesystem{
				Type:  "vfat",
				Label: "EFI",
			},
			MountPoint: "/boot",
		},
		Root: diskplan.Root{
			Partition: diskplan.Partition{
				Role:      diskplan.RoleRoot,
				Number:    2,
				Label:     "GJALLAROS",
				TypeGUID:  linuxLUKSType,
				PARTUUID:  rootUUID,
				SizeBytes: rootBytes,
				Filesystem: diskplan.Filesystem{
					Type:  "btrfs",
					Label: "GJALLAROS",
					Options: map[string]string{
						"compress": "zstd",
					},
				},
				MountPoint: "/",
			},
			Encryption: diskplan.Encryption{
				Type:        diskplan.EncryptionLUKS2,
				MappingName: rootMappingName,
			},
		},
	}

	if input.EnableRecovery {
		recoveryUUID, err := uuidV4()
		if err != nil {
			return diskplan.Plan{}, fmt.Errorf(
				"generate recovery PARTUUID: %w",
				err,
			)
		}
		plan.Recovery = &diskplan.Partition{
			Role:      diskplan.RoleRecovery,
			Number:    3,
			Label:     diskplan.RecoveryPartLabel,
			TypeGUID:  xbootldrType,
			PARTUUID:  recoveryUUID,
			SizeBytes: RecoveryBytes,
			Filesystem: diskplan.Filesystem{
				Type:  "vfat",
				Label: diskplan.RecoveryFSLabel,
			},
			MountPoint: "/recovery",
		}
	}

	if err := plan.Validate(); err != nil {
		return diskplan.Plan{}, fmt.Errorf(
			"validate generated canonical disk plan: %w",
			err,
		)
	}

	return plan, nil
}

func alignDown(value, alignment uint64) uint64 {
	if alignment == 0 {
		return value
	}
	return value - value%alignment
}

func uuidV4() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}

	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	hexed := hex.EncodeToString(raw[:])
	return fmt.Sprintf(
		"%s-%s-%s-%s-%s",
		hexed[0:8],
		hexed[8:12],
		hexed[12:16],
		hexed[16:20],
		hexed[20:32],
	), nil
}

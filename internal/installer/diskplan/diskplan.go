// Package diskplan defines the canonical, planning-only description of a
// gjallarOS bare-metal installation target.
//
// The package intentionally performs no device discovery, partitioning,
// formatting, encryption, mounting, or installation. A Plan is safe to
// serialize and log because it contains identities and intended layout only;
// secrets such as LUKS passphrases or keys have no representation here.
package diskplan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// SchemaVersion is the current serialized disk-plan schema.
	SchemaVersion = 1

	RoleESP      = "esp"
	RoleRoot     = "root"
	RoleRecovery = "recovery"

	EncryptionLUKS2 = "luks2"

	// Recovery partition labels (JODS-565, device contract v2): the GPT
	// partition name and the FAT label, 11 characters at most.
	RecoveryPartLabel = "GJALLAR-RECOVERY"
	RecoveryFSLabel   = "GJALLAR-REC"
)

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Plan is the canonical description of one intended gjallarOS installation.
//
// It is shared installation state, not an execution request. Consumers must
// independently validate the real target device before performing any
// destructive action.
type Plan struct {
	SchemaVersion int        `json:"schema_version"`
	TargetDisk    Disk       `json:"target_disk"`
	ESP           Partition  `json:"esp"`
	Root          Root       `json:"root"`
	Recovery      *Partition `json:"recovery,omitempty"`
}

// Disk identifies the whole physical target disk.
//
// WWN is optional because not every device exposes one. Serial remains the
// required fallback identity and matches the recovery executor's existing
// identity model.
type Disk struct {
	Path        string `json:"path"`
	Model       string `json:"model"`
	Serial      string `json:"serial"`
	WWN         string `json:"wwn,omitempty"`
	SizeBytes   uint64 `json:"size_bytes"`
	GPTDiskGUID string `json:"gpt_disk_guid"`
}

// Partition describes one partition in the intended GPT layout.
type Partition struct {
	Role       string     `json:"role"`
	Number     uint       `json:"number"`
	Label      string     `json:"label"`
	TypeGUID   string     `json:"type_guid"`
	PARTUUID   string     `json:"partuuid"`
	SizeBytes  uint64     `json:"size_bytes"`
	Filesystem Filesystem `json:"filesystem"`
	MountPoint string     `json:"mount_point"`
}

// Root describes the encrypted gjallarOS root partition.
type Root struct {
	Partition  Partition  `json:"partition"`
	Encryption Encryption `json:"encryption"`
}

// Encryption describes the intended root encryption without containing any
// credential, passphrase, key, token, or secret material.
type Encryption struct {
	Type        string `json:"type"`
	MappingName string `json:"mapping_name"`
}

// Filesystem describes the filesystem expected on a partition.
//
// Options contains non-secret declarative filesystem options only. Callers must
// never place credentials or key material in this map.
type Filesystem struct {
	Type    string            `json:"type"`
	Label   string            `json:"label,omitempty"`
	Options map[string]string `json:"options,omitempty"`
}

// Validate checks the canonical planning contract.
//
// Validation is deliberately structural. It does not inspect the host or
// resolve any device, so calling Validate cannot modify or probe a disk.
func (p Plan) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf(
			"disk plan schema version must be %d, got %d",
			SchemaVersion,
			p.SchemaVersion,
		)
	}

	if err := validateDisk(p.TargetDisk); err != nil {
		return err
	}

	seenNumbers := map[uint]string{}
	seenPARTUUIDs := map[string]string{}
	seenMounts := map[string]string{}

	if err := validatePartition(
		p.ESP,
		RoleESP,
		seenNumbers,
		seenPARTUUIDs,
		seenMounts,
	); err != nil {
		return fmt.Errorf("ESP: %w", err)
	}

	if err := validatePartition(
		p.Root.Partition,
		RoleRoot,
		seenNumbers,
		seenPARTUUIDs,
		seenMounts,
	); err != nil {
		return fmt.Errorf("root: %w", err)
	}

	if p.Root.Partition.MountPoint != "/" {
		return fmt.Errorf(
			"root mount point must be /, got %q",
			p.Root.Partition.MountPoint,
		)
	}

	if p.Root.Encryption.Type != EncryptionLUKS2 {
		return fmt.Errorf(
			"root encryption type must be %q, got %q",
			EncryptionLUKS2,
			p.Root.Encryption.Type,
		)
	}

	if strings.TrimSpace(p.Root.Encryption.MappingName) == "" {
		return fmt.Errorf("root encryption mapping name is required")
	}

	if strings.ContainsAny(p.Root.Encryption.MappingName, `/\`) {
		return fmt.Errorf(
			"root encryption mapping name must not contain path separators: %q",
			p.Root.Encryption.MappingName,
		)
	}

	if p.Recovery != nil {
		if err := validatePartition(
			*p.Recovery,
			RoleRecovery,
			seenNumbers,
			seenPARTUUIDs,
			seenMounts,
		); err != nil {
			return fmt.Errorf("recovery: %w", err)
		}
	}

	return nil
}

// JSON validates the plan and returns its canonical serialized representation.
//
// encoding/json preserves struct field order, so the output is stable for the
// same plan while remaining ordinary JSON usable by local installation and
// recovery/JODS consumers.
func (p Plan) JSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("serialize disk plan: %w", err)
	}

	return append(data, '\n'), nil
}

func validateDisk(d Disk) error {
	if !filepath.IsAbs(d.Path) {
		return fmt.Errorf("target disk path must be absolute: %q", d.Path)
	}

	clean := filepath.Clean(d.Path)
	if clean == "/" || !strings.HasPrefix(clean, "/dev/") {
		return fmt.Errorf("target disk path must identify a device under /dev: %q", d.Path)
	}

	if strings.TrimSpace(d.Serial) == "" && strings.TrimSpace(d.WWN) == "" {
		return fmt.Errorf("target disk requires a serial or WWN")
	}

	if d.SizeBytes == 0 {
		return fmt.Errorf("target disk size must be greater than zero")
	}

	if !guidPattern.MatchString(d.GPTDiskGUID) {
		return fmt.Errorf("invalid GPT disk GUID %q", d.GPTDiskGUID)
	}

	return nil
}

func validatePartition(
	part Partition,
	expectedRole string,
	seenNumbers map[uint]string,
	seenPARTUUIDs map[string]string,
	seenMounts map[string]string,
) error {
	if part.Role != expectedRole {
		return fmt.Errorf(
			"partition role must be %q, got %q",
			expectedRole,
			part.Role,
		)
	}

	if part.Number == 0 {
		return fmt.Errorf("partition number must be greater than zero")
	}

	if prior, ok := seenNumbers[part.Number]; ok {
		return fmt.Errorf(
			"partition number %d is already used by %s",
			part.Number,
			prior,
		)
	}
	seenNumbers[part.Number] = expectedRole

	if strings.TrimSpace(part.Label) == "" {
		return fmt.Errorf("partition label is required")
	}

	if !guidPattern.MatchString(part.TypeGUID) {
		return fmt.Errorf("invalid partition type GUID %q", part.TypeGUID)
	}

	if !guidPattern.MatchString(part.PARTUUID) {
		return fmt.Errorf("invalid PARTUUID %q", part.PARTUUID)
	}

	partUUID := strings.ToLower(part.PARTUUID)
	if prior, ok := seenPARTUUIDs[partUUID]; ok {
		return fmt.Errorf(
			"PARTUUID %q is already used by %s",
			part.PARTUUID,
			prior,
		)
	}
	seenPARTUUIDs[partUUID] = expectedRole

	if part.SizeBytes == 0 {
		return fmt.Errorf("partition size must be greater than zero")
	}

	if strings.TrimSpace(part.Filesystem.Type) == "" {
		return fmt.Errorf("filesystem type is required")
	}

	if part.MountPoint == "" || !filepath.IsAbs(part.MountPoint) {
		return fmt.Errorf(
			"mount point must be an absolute path: %q",
			part.MountPoint,
		)
	}

	mount := filepath.Clean(part.MountPoint)
	if prior, ok := seenMounts[mount]; ok {
		return fmt.Errorf(
			"mount point %q is already used by %s",
			mount,
			prior,
		)
	}
	seenMounts[mount] = expectedRole

	return nil
}

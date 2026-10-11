package diskcrypto

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var mappingPattern = regexp.MustCompile(`boot\.initrd\.luks\.devices\.\"?([^\".]+)\"?\.device\s*=\s*\"([^\"]+)\"\s*;`)

type MappingInfo struct {
	Name   string
	Device string
}

func MappingDetails(path string) (MappingInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MappingInfo{}, err
	}
	match := mappingPattern.FindSubmatch(data)
	if len(match) != 3 {
		return MappingInfo{}, nil
	}
	return MappingInfo{Name: string(match[1]), Device: string(match[2])}, nil
}

func Mapping(path string) (string, error) {
	info, err := MappingDetails(path)
	if err != nil {
		return "", err
	}
	return info.Name, nil
}

func VerifyDeviceIdentity(expected, detected string) error {
	if !strings.HasPrefix(expected, "/dev/disk/by-uuid/") {
		return fmt.Errorf("refusing disk-key operation: configured LUKS device %q is not a stable /dev/disk/by-uuid path", expected)
	}
	expectedResolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return fmt.Errorf("resolve configured LUKS device %s: %w", expected, err)
	}
	detectedResolved, err := filepath.EvalSymlinks(detected)
	if err != nil {
		return fmt.Errorf("resolve detected LUKS device %s: %w", detected, err)
	}
	if expectedResolved != detectedResolved {
		return fmt.Errorf("refusing disk-key operation: configured LUKS device %s resolves to %s, but detected %s", expected, expectedResolved, detectedResolved)
	}
	return nil
}

func DetectDevice(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "lsblk", "-pnro", "NAME,FSTYPE").Output()
	if err != nil {
		return "", fmt.Errorf("lsblk: %w", err)
	}
	devices := parseLUKSDevices(string(out))
	if len(devices) > 1 {
		return "", fmt.Errorf("multiple LUKS devices detected (%s); refusing ambiguous automatic selection", strings.Join(devices, ", "))
	}
	if len(devices) == 1 {
		return devices[0], nil
	}
	return "", nil
}

func parseLUKSDevices(output string) []string {
	var devices []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "crypto_LUKS" && filepath.IsAbs(fields[0]) {
			devices = append(devices, fields[0])
		}
	}
	return devices
}

// TPMAvailable reports a TPM 2.0. A TPM 1.2 also creates /dev/tpm0, but
// systemd-cryptenroll and the measured-boot policy need TPM 2.0, so a 1.2
// chip counts as no TPM here instead of failing later at enrollment.
func TPMAvailable() bool {
	return TPMMajorVersion("/") == 2
}

// TPMMajorVersion returns the TPM spec major version (1 or 2) of the first
// TPM under root, or 0 without one. Only TPM 2.0 has the in-kernel resource
// manager (/dev/tpmrm0), which settles kernels without tpm_version_major.
func TPMMajorVersion(root string) int {
	raw, err := os.ReadFile(filepath.Join(root, "sys/class/tpm/tpm0/tpm_version_major"))
	if err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			return v
		}
	}
	if _, err := os.Stat(filepath.Join(root, "dev/tpmrm0")); err == nil {
		return 2
	}
	if _, err := os.Stat(filepath.Join(root, "dev/tpm0")); err == nil {
		return 1
	}
	return 0
}

func TPMPolicyAvailable() error {
	for _, path := range []string{"/var/lib/systemd/pcrlock.json"} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("signed TPM policy missing at %s: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("signed TPM policy at %s is not a non-empty regular file", path)
		}
	}
	return nil
}

func EnableTPMConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(string(data), "boot.initrd.systemd.tpm2.enable") {
		return nil
	}
	index := strings.LastIndex(string(data), "}")
	if index < 0 {
		return fmt.Errorf("hardware configuration has no closing brace")
	}
	// measured-boot.nix adds tpm2-device=auto once luksTpm2Enable is set;
	// adding it here too doubled the crypttab option.
	addition := "    boot.initrd.systemd.enable = true;\n    boot.initrd.systemd.tpm2.enable = true;\n"
	updated := append(append([]byte{}, data[:index]...), append([]byte(addition), data[index:]...)...)
	return atomicWrite(path, updated)
}

// SecretFile writes a secret to a 0600 file on /run, a tmpfs, so it never
// reaches a disk. Call the cleanup as soon as the file is used.
func SecretFile(secret string) (string, func(), error) {
	f, err := os.CreateTemp("/run", "gjallar-luks-key-*")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := f.Chmod(0600); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := f.WriteString(secret); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return name, cleanup, nil
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hardware-tpm-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

type luksMetadata struct {
	Tokens map[string]struct {
		Type     string   `json:"type"`
		Keyslots []string `json:"keyslots"`
	} `json:"tokens"`
}

type TPM2KeyslotRecord struct {
	Schema                int                 `json:"schema"`
	Device                string              `json:"device"`
	TPM2Tokens            map[string][]string `json:"tpm2Tokens"`
	Policy                string              `json:"policy"`
	HumanRecoveryVerified bool                `json:"humanRecoveryVerified"`
}

func ReadTPM2TokenID(metadataPath string) (string, error) {
	metadata, err := readLUKSMetadata(metadataPath)
	if err != nil {
		return "", err
	}

	ids := make([]string, 0, 1)
	for id, token := range metadata.Tokens {
		if token.Type == "systemd-tpm2" {
			ids = append(ids, id)
		}
	}

	if len(ids) != 1 {
		return "", fmt.Errorf(
			"expected exactly one TPM2 token, found %d",
			len(ids),
		)
	}

	return ids[0], nil
}

// CheckPCRLockPolicy fails unless the systemd-pcrlock policy locks every
// requested PCR. make-policy silently drops PCRs it cannot predict; without
// a firmware event log it wrote "pcrValues":[] and the TPM then released
// the disk key to any boot chain (e2e-target, 2026-09-29).
func CheckPCRLockPolicy(path string, pcrs []int) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pcrlock policy: %w", err)
	}
	var policy struct {
		PCRValues []struct {
			PCR int `json:"pcr"`
		} `json:"pcrValues"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		return fmt.Errorf("parse pcrlock policy: %w", err)
	}
	locked := make(map[int]bool, len(policy.PCRValues))
	for _, value := range policy.PCRValues {
		locked[value.PCR] = true
	}
	var missing []string
	for _, pcr := range pcrs {
		if !locked[pcr] {
			missing = append(missing, strconv.Itoa(pcr))
		}
	}
	if len(pcrs) == 0 || len(missing) != 0 {
		return fmt.Errorf(
			"pcrlock policy %s does not lock PCR %s; refusing TPM2 enrollment",
			path,
			strings.Join(missing, ","),
		)
	}
	return nil
}

func WriteTPM2KeyslotRecord(
	metadataPath string,
	targetPath string,
	device string,
	policy string,
) error {
	metadata, err := readLUKSMetadata(metadataPath)
	if err != nil {
		return err
	}

	tokens := make(map[string][]string)
	for id, token := range metadata.Tokens {
		if token.Type == "systemd-tpm2" {
			tokens[id] = append([]string(nil), token.Keyslots...)
		}
	}

	if len(tokens) != 1 {
		return fmt.Errorf(
			"expected exactly one TPM2 token after enrollment, found %d",
			len(tokens),
		)
	}

	record := TPM2KeyslotRecord{
		Schema:                1,
		Device:                device,
		TPM2Tokens:            tokens,
		Policy:                policy,
		HumanRecoveryVerified: true,
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode TPM2 keyslot record: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".keyslots.json-*")
	if err != nil {
		return fmt.Errorf("create temporary TPM2 keyslot record: %w", err)
	}

	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("set TPM2 keyslot record permissions: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write TPM2 keyslot record: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync TPM2 keyslot record: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close TPM2 keyslot record: %w", err)
	}

	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("replace TPM2 keyslot record: %w", err)
	}

	return nil
}

func readLUKSMetadata(path string) (luksMetadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return luksMetadata{}, fmt.Errorf("read LUKS metadata: %w", err)
	}

	var metadata luksMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return luksMetadata{}, fmt.Errorf("parse LUKS metadata: %w", err)
	}

	if metadata.Tokens == nil {
		metadata.Tokens = map[string]struct {
			Type     string   `json:"type"`
			Keyslots []string `json:"keyslots"`
		}{}
	}

	return metadata, nil
}

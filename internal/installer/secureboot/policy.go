package secureboot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

const FirmwarePolicyPath = "/var/lib/gjallarOS/secure-boot/firmware-policy.json"

type FirmwarePolicySnapshot struct {
	Schema                  int      `json:"schema"`
	ModelID                 string   `json:"modelId,omitempty"`
	SourceEntity            string   `json:"sourceEntity,omitempty"`
	FirmwareName            string   `json:"firmwareName"`
	SetupModeStrategy       string   `json:"setupModeStrategy"`
	EnrollmentBackend       string   `json:"enrollmentBackend"`
	RequiredPresent         []string `json:"requiredPresent"`
	RequiredAbsent          []string `json:"requiredAbsent"`
	PreserveFirmwareBuiltin []string `json:"preserveFirmwareBuiltin"`
	Untouched               []string `json:"untouched"`
	FactoryOwnershipProof   string   `json:"factoryOwnershipProof"`
	Instructions            []string `json:"instructions"`
}

func SnapshotFirmwarePolicy(
	modelID string,
	effective oddc.EffectiveSecureBootFirmwarePolicy,
) (FirmwarePolicySnapshot, error) {
	if !effective.Policy.Supported {
		return FirmwarePolicySnapshot{}, fmt.Errorf(
			"cannot snapshot unsupported Secure Boot firmware policy",
		)
	}

	if err := validateSnapshotPolicy(effective.Policy); err != nil {
		return FirmwarePolicySnapshot{}, err
	}

	if !strings.HasPrefix(strings.TrimSpace(modelID), "model/") {
		return FirmwarePolicySnapshot{}, fmt.Errorf(
			"cannot snapshot Secure Boot firmware policy without canonical model id",
		)
	}

	if strings.TrimSpace(effective.SourceEntity) == "" {
		return FirmwarePolicySnapshot{}, fmt.Errorf(
			"cannot snapshot Secure Boot firmware policy without source entity",
		)
	}

	return FirmwarePolicySnapshot{
		Schema:                  2,
		ModelID:                 modelID,
		SourceEntity:            effective.SourceEntity,
		FirmwareName:            effective.Policy.FirmwareName,
		SetupModeStrategy:       effective.Policy.SetupModeStrategy,
		EnrollmentBackend:       effective.Policy.EnrollmentBackend,
		RequiredPresent:         cloneStrings(effective.Policy.RequiredPresent),
		RequiredAbsent:          cloneStrings(effective.Policy.RequiredAbsent),
		PreserveFirmwareBuiltin: cloneStrings(effective.Policy.PreserveFirmwareBuiltin),
		Untouched:               cloneStrings(effective.Policy.Untouched),
		FactoryOwnershipProof:   effective.Policy.FactoryOwnershipProof,
		Instructions:            cloneStrings(effective.Policy.Instructions),
	}, nil
}

func WriteFirmwarePolicySnapshot(
	path string,
	snapshot FirmwarePolicySnapshot,
) error {
	if path == "" {
		return fmt.Errorf("Secure Boot firmware policy snapshot path is empty")
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Secure Boot firmware policy snapshot: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create Secure Boot policy directory: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".firmware-policy-*")
	if err != nil {
		return fmt.Errorf("create Secure Boot policy snapshot: %w", err)
	}
	tempPath := temp.Name()

	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure firmware policy snapshot permissions: %w", err)
	}

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write Secure Boot firmware policy snapshot: %w", err)
	}

	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync Secure Boot firmware policy snapshot: %w", err)
	}

	if err := temp.Close(); err != nil {
		return fmt.Errorf("close Secure Boot firmware policy snapshot: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install Secure Boot firmware policy snapshot: %w", err)
	}

	removeTemp = false
	return nil
}

func LoadFirmwarePolicySnapshot(path string) (FirmwarePolicySnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FirmwarePolicySnapshot{}, fmt.Errorf(
			"read Secure Boot firmware policy snapshot: %w",
			err,
		)
	}

	return decodeFirmwarePolicySnapshot(data)
}

func FirmwareInstructions(path string) ([]string, error) {
	snapshot, err := LoadFirmwarePolicySnapshot(path)
	if err != nil {
		return nil, err
	}

	if err := ValidateFirmwarePolicySnapshot(snapshot); err != nil {
		return nil, fmt.Errorf(
			"validate Secure Boot firmware policy snapshot: %w",
			err,
		)
	}

	instructions := cloneStrings(snapshot.Instructions)
	if len(instructions) == 0 {
		return nil, fmt.Errorf(
			"Secure Boot firmware policy contains no instructions",
		)
	}

	return instructions, nil
}

func ValidateFirmwarePolicySnapshot(
	snapshot FirmwarePolicySnapshot,
) error {
	if snapshot.Schema != 2 {
		return fmt.Errorf(
			"unsupported Secure Boot firmware policy snapshot schema %d",
			snapshot.Schema,
		)
	}

	if !strings.HasPrefix(
		strings.TrimSpace(snapshot.ModelID),
		"model/",
	) {
		return fmt.Errorf(
			"Secure Boot firmware policy snapshot lacks canonical model identity",
		)
	}

	if strings.TrimSpace(snapshot.SourceEntity) == "" ||
		strings.HasPrefix(
			strings.TrimSpace(snapshot.SourceEntity),
			"laptop/",
		) {
		return fmt.Errorf(
			"Secure Boot firmware policy snapshot lacks canonical source entity",
		)
	}

	policy := oddc.SecureBootFirmwarePolicy{
		Supported:               true,
		FirmwareName:            snapshot.FirmwareName,
		SetupModeStrategy:       snapshot.SetupModeStrategy,
		EnrollmentBackend:       snapshot.EnrollmentBackend,
		RequiredPresent:         cloneStrings(snapshot.RequiredPresent),
		RequiredAbsent:          cloneStrings(snapshot.RequiredAbsent),
		PreserveFirmwareBuiltin: cloneStrings(snapshot.PreserveFirmwareBuiltin),
		Untouched:               cloneStrings(snapshot.Untouched),
		FactoryOwnershipProof:   snapshot.FactoryOwnershipProof,
		Instructions:            cloneStrings(snapshot.Instructions),
	}

	return validateSnapshotPolicy(policy)
}

func validateSnapshotPolicy(policy oddc.SecureBootFirmwarePolicy) error {
	switch policy.SetupModeStrategy {
	case "clear-platform-key", "firmware-setup-mode":
	default:
		return fmt.Errorf(
			"unsupported Secure Boot setup mode strategy %q",
			policy.SetupModeStrategy,
		)
	}

	if policy.EnrollmentBackend != "sbctl" {
		return fmt.Errorf(
			"unsupported Secure Boot enrollment backend %q",
			policy.EnrollmentBackend,
		)
	}

	if policy.FactoryOwnershipProof != "pk-equals-pkdefault" {
		return fmt.Errorf(
			"unsupported Secure Boot factory ownership proof %q",
			policy.FactoryOwnershipProof,
		)
	}

	allowed := map[string]bool{
		"PK":  true,
		"KEK": true,
		"db":  true,
		"dbx": true,
	}

	for name, values := range map[string][]string{
		"requiredPresent":         policy.RequiredPresent,
		"requiredAbsent":          policy.RequiredAbsent,
		"preserveFirmwareBuiltin": policy.PreserveFirmwareBuiltin,
		"untouched":               policy.Untouched,
	} {
		seen := map[string]bool{}
		for _, variable := range values {
			if !allowed[variable] {
				return fmt.Errorf(
					"%s contains unsupported EFI variable %q",
					name,
					variable,
				)
			}
			if seen[variable] {
				return fmt.Errorf(
					"%s contains duplicate EFI variable %q",
					name,
					variable,
				)
			}
			seen[variable] = true
		}
	}

	if policy.FirmwareName == "" {
		return fmt.Errorf("Secure Boot firmware name is empty")
	}

	if len(policy.Instructions) == 0 {
		return fmt.Errorf("Secure Boot firmware instructions are empty")
	}

	return nil
}

func EnsureFirmwarePolicySnapshot(
	ctx context.Context,
	snapshot FirmwarePolicySnapshot,
) error {
	if err := ValidateFirmwarePolicySnapshot(snapshot); err != nil {
		return fmt.Errorf("validate Secure Boot firmware policy snapshot: %w", err)
	}

	exists, err := sudoTest(ctx, "-f", FirmwarePolicyPath)
	if err != nil {
		return fmt.Errorf("inspect existing Secure Boot firmware policy snapshot: %w", err)
	}

	if exists {
		data, err := sudoReadFile(ctx, FirmwarePolicyPath)
		if err != nil {
			return fmt.Errorf("read existing Secure Boot firmware policy snapshot: %w", err)
		}

		existing, err := decodeFirmwarePolicySnapshot(data)
		if err != nil {
			return fmt.Errorf(
				"existing Secure Boot firmware policy snapshot is invalid: %w",
				err,
			)
		}

		if !reflect.DeepEqual(existing, snapshot) {
			return fmt.Errorf(
				"existing Secure Boot firmware policy snapshot does not match the currently detected device policy; refusing to replace an active transaction",
			)
		}

		return nil
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Secure Boot firmware policy snapshot: %w", err)
	}
	data = append(data, '\n')

	if err := run(
		ctx,
		"sudo", "install", "-d", "-m", "0700",
		filepath.Dir(FirmwarePolicyPath),
	); err != nil {
		return fmt.Errorf("create Secure Boot policy transaction directory: %w", err)
	}

	if err := writeRootFile(ctx, data, "0600", FirmwarePolicyPath); err != nil {
		return fmt.Errorf("install Secure Boot firmware policy snapshot: %w", err)
	}

	return nil
}

func decodeFirmwarePolicySnapshot(data []byte) (FirmwarePolicySnapshot, error) {
	var snapshot FirmwarePolicySnapshot

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&snapshot); err != nil {
		return FirmwarePolicySnapshot{}, fmt.Errorf(
			"decode Secure Boot firmware policy snapshot: %w",
			err,
		)
	}

	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return FirmwarePolicySnapshot{}, fmt.Errorf(
			"decode Secure Boot firmware policy snapshot: trailing JSON value",
		)
	}

	if err := ValidateFirmwarePolicySnapshot(snapshot); err != nil {
		return FirmwarePolicySnapshot{}, err
	}

	return snapshot, nil
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

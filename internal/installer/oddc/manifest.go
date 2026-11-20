package oddc

import (
	"fmt"
	"slices"
	"strings"
)

type Identity struct {
	FormFactor     string
	SysVendor      string
	ProductName    string
	ProductVersion string
	BoardVendor    string
	BoardName      string
	BoardVersion   string
}

type Validation struct {
	LastValidatedNixOS             string `json:"lastValidatedNixOS,omitempty"`
	LastValidatedGjallarOSRevision string `json:"lastValidatedGjallarOSRevision,omitempty"`
	LastValidatedDeviceID          string `json:"lastValidatedDeviceID,omitempty"`
	LastValidatedODDCRevision      string `json:"lastValidatedODDCRevision,omitempty"`
	LastValidatedAt                string `json:"lastValidatedAt,omitempty"`
}

type ValidationTarget struct {
	NixOSRelease      string
	GjallarOSRevision string
	DeviceID          string
	ODDCRevision      string
}

func (v Validation) Matches(target ValidationTarget) bool {
	return strings.TrimSpace(v.LastValidatedNixOS) == strings.TrimSpace(target.NixOSRelease) &&
		strings.TrimSpace(v.LastValidatedGjallarOSRevision) == strings.TrimSpace(target.GjallarOSRevision) &&
		strings.TrimSpace(v.LastValidatedDeviceID) == strings.TrimSpace(target.DeviceID) &&
		strings.TrimSpace(v.LastValidatedODDCRevision) == strings.TrimSpace(target.ODDCRevision) &&
		strings.TrimSpace(v.LastValidatedAt) != ""
}

type GraphicsPolicy struct {
	IntegratedKernelDriver string `json:"integratedKernelDriver,omitempty"`
	DiscreteKernelDriver   string `json:"discreteKernelDriver,omitempty"`
	DriverBranch           string `json:"driverBranch,omitempty"`
}

type SecureBootFirmwarePolicy struct {
	Supported               bool     `json:"supported"`
	FirmwareName            string   `json:"firmwareName,omitempty"`
	SetupModeStrategy       string   `json:"setupModeStrategy,omitempty"`
	EnrollmentBackend       string   `json:"enrollmentBackend,omitempty"`
	RequiredPresent         []string `json:"requiredPresent,omitempty"`
	RequiredAbsent          []string `json:"requiredAbsent,omitempty"`
	PreserveFirmwareBuiltin []string `json:"preserveFirmwareBuiltin,omitempty"`
	Untouched               []string `json:"untouched,omitempty"`
	FactoryOwnershipProof   string   `json:"factoryOwnershipProof,omitempty"`
	Instructions            []string `json:"instructions,omitempty"`
	UnsupportedReason       string   `json:"unsupportedReason,omitempty"`
	// Plane 388: factory-PK facts for firmware that does not expose a
	// PKDefault efivar (e.g. ThinkPad T480s). FactoryPKFingerprints holds
	// lowercase hex SHA-256 sums of accepted factory PK certificates;
	// PKDefaultAbsent says this model's firmware never exposes PKDefault,
	// so an unconfirmed PK on it still counts as factory.
	FactoryPKFingerprints []string `json:"factoryPKFingerprints,omitempty"`
	PKDefaultAbsent       bool     `json:"pkDefaultAbsent,omitempty"`
}

func validateSecureBootFirmwarePolicy(policy SecureBootFirmwarePolicy) error {
	if !policy.Supported {
		if policy.SetupModeStrategy != "unsupported" {
			return fmt.Errorf(
				"unsupported policy requires setupModeStrategy %q",
				"unsupported",
			)
		}

		if strings.TrimSpace(policy.UnsupportedReason) == "" {
			return fmt.Errorf("unsupported policy requires unsupportedReason")
		}

		if strings.TrimSpace(policy.FirmwareName) != "" ||
			strings.TrimSpace(policy.EnrollmentBackend) != "" ||
			len(policy.RequiredPresent) != 0 ||
			len(policy.RequiredAbsent) != 0 ||
			len(policy.PreserveFirmwareBuiltin) != 0 ||
			len(policy.Untouched) != 0 ||
			strings.TrimSpace(policy.FactoryOwnershipProof) != "" ||
			len(policy.Instructions) != 0 ||
			len(policy.FactoryPKFingerprints) != 0 ||
			policy.PKDefaultAbsent {
			return fmt.Errorf(
				"unsupported policy cannot contain operational Secure Boot fields",
			)
		}

		return nil
	}

	if strings.TrimSpace(policy.FirmwareName) == "" {
		return fmt.Errorf("supported policy requires firmwareName")
	}

	switch policy.SetupModeStrategy {
	case "clear-platform-key", "firmware-setup-mode":
	default:
		return fmt.Errorf(
			"invalid setupModeStrategy %q",
			policy.SetupModeStrategy,
		)
	}

	switch policy.EnrollmentBackend {
	case "sbctl":
	default:
		return fmt.Errorf(
			"invalid enrollmentBackend %q",
			policy.EnrollmentBackend,
		)
	}

	switch policy.FactoryOwnershipProof {
	case "pk-equals-pkdefault":
	default:
		return fmt.Errorf(
			"invalid factoryOwnershipProof %q",
			policy.FactoryOwnershipProof,
		)
	}

	// Plane 388: factory-PK facts must be usable — fingerprints are
	// lowercase hex SHA-256 sums of certificates.
	seenFingerprints := map[string]bool{}
	for _, fp := range policy.FactoryPKFingerprints {
		if len(fp) != 64 || strings.TrimRight(fp, "0123456789abcdef") != "" {
			return fmt.Errorf(
				"factoryPKFingerprints entry %q is not a lowercase hex SHA-256 sum",
				fp,
			)
		}
		if seenFingerprints[fp] {
			return fmt.Errorf("factoryPKFingerprints contains duplicate %q", fp)
		}
		seenFingerprints[fp] = true
	}

	allowedVariables := map[string]bool{
		"PK":  true,
		"KEK": true,
		"db":  true,
		"dbx": true,
	}

	seen := map[string]string{}

	sets := []struct {
		name   string
		values []string
	}{
		{"requiredPresent", policy.RequiredPresent},
		{"requiredAbsent", policy.RequiredAbsent},
		{"preserveFirmwareBuiltin", policy.PreserveFirmwareBuiltin},
		{"untouched", policy.Untouched},
	}

	for _, set := range sets {
		local := map[string]bool{}

		for _, variable := range set.values {
			if !allowedVariables[variable] {
				return fmt.Errorf(
					"%s contains invalid EFI variable %q",
					set.name,
					variable,
				)
			}

			if local[variable] {
				return fmt.Errorf(
					"%s contains duplicate EFI variable %q",
					set.name,
					variable,
				)
			}
			local[variable] = true

			if previous, ok := seen[variable]; ok &&
				((previous == "requiredPresent" && set.name == "requiredAbsent") ||
					(previous == "requiredAbsent" && set.name == "requiredPresent")) {
				return fmt.Errorf(
					"EFI variable %q cannot be both required present and required absent",
					variable,
				)
			}

			seen[variable] = set.name
		}
	}

	if len(policy.Instructions) == 0 {
		return fmt.Errorf("supported policy requires firmware instructions")
	}

	for _, instruction := range policy.Instructions {
		if strings.TrimSpace(instruction) == "" {
			return fmt.Errorf("firmware instructions cannot contain empty entries")
		}
	}

	return nil
}

// MatchesFactoryPK reports whether this model's factory-PK facts prove the
// active PK (given as the lowercase hex SHA-256 of its certificate) is the
// factory one even though the firmware exposes no PKDefault efivar
// (Plane 388, e.g. ThinkPad T480s): either the fingerprint list names it, or
// the model's firmware never exposes PKDefault.
func (p SecureBootFirmwarePolicy) MatchesFactoryPK(pkSHA256 string, pkDefaultPresent bool) bool {
	if !p.Supported || pkDefaultPresent {
		return false
	}
	if slices.Contains(p.FactoryPKFingerprints, pkSHA256) {
		return true
	}
	return p.PKDefaultAbsent
}

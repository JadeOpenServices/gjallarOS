package oddc

import (
	"fmt"
	"sort"
	"strings"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"
)

type EffectiveSecureBootFirmwarePolicy struct {
	Policy       SecureBootFirmwarePolicy
	SourceEntity string
}

func ResolveSecureBootFirmwarePolicy(
	resolved Resolved,
) (EffectiveSecureBootFirmwarePolicy, error) {
	if resolved.ModelID == "" ||
		resolved.Canonical.Resolved == nil {
		return unsupportedFirmwarePolicy(
			"No canonical ODDC model is resolved.",
		), nil
	}

	const base = "vendor.policy.secureBoot"

	// Canonical ODDC uses presence semantics here:
	// no Secure Boot policy means unsupported/unvalidated;
	// a present policy is an explicit supported policy.
	if _, exists := portable.Lookup(
		resolved.Canonical.Resolved,
		base,
	); !exists {
		return unsupportedFirmwarePolicy(
			"No trusted Secure Boot firmware policy is defined for the resolved canonical ODDC model.",
		), nil
	}

	policy := SecureBootFirmwarePolicy{
		Supported:         true,
		FirmwareName:      stringPath(resolved.Canonical.Resolved, base+".firmwareName"),
		SetupModeStrategy: stringPath(resolved.Canonical.Resolved, base+".setupModeStrategy"),
		EnrollmentBackend: stringPath(resolved.Canonical.Resolved, base+".enrollmentBackend"),
		FactoryOwnershipProof: stringPath(
			resolved.Canonical.Resolved,
			base+".factoryOwnershipProof",
		),
		UnsupportedReason: stringPath(
			resolved.Canonical.Resolved,
			base+".unsupportedReason",
		),
		RequiredPresent: enabledKeys(
			resolved.Canonical.Resolved,
			base+".requiredPresent",
		),
		RequiredAbsent: enabledKeys(
			resolved.Canonical.Resolved,
			base+".requiredAbsent",
		),
		PreserveFirmwareBuiltin: enabledKeys(
			resolved.Canonical.Resolved,
			base+".preserveFirmwareBuiltin",
		),
		Untouched: enabledKeys(
			resolved.Canonical.Resolved,
			base+".untouched",
		),
		Instructions: firmwareInstructions(
			resolved.Canonical.Resolved,
			base+".instructions",
		),
	}

	if err := validateSecureBootFirmwarePolicy(
		policy,
	); err != nil {
		return EffectiveSecureBootFirmwarePolicy{}, fmt.Errorf(
			"invalid canonical Secure Boot firmware policy: %w",
			err,
		)
	}

	source := ""
	if provenance, ok :=
		resolved.Canonical.Provenance[base+".setupModeStrategy"]; ok {
		source = strings.TrimPrefix(
			provenance.Source,
			"catalog:",
		)
	}

	return EffectiveSecureBootFirmwarePolicy{
		Policy:       policy,
		SourceEntity: source,
	}, nil
}

func unsupportedFirmwarePolicy(
	reason string,
) EffectiveSecureBootFirmwarePolicy {
	return EffectiveSecureBootFirmwarePolicy{
		Policy: SecureBootFirmwarePolicy{
			Supported:         false,
			SetupModeStrategy: "unsupported",
			UnsupportedReason: reason,
		},
	}
}

func stringPath(
	root map[string]any,
	path string,
) string {
	value, ok := portable.Lookup(root, path)
	if !ok {
		return ""
	}

	result, _ := value.(string)
	return result
}

func enabledKeys(
	root map[string]any,
	path string,
) []string {
	value, ok := portable.Lookup(root, path)
	if !ok {
		return nil
	}

	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	result := make([]string, 0)

	for key, value := range object {
		enabled, ok := value.(bool)
		if ok && enabled {
			result = append(result, key)
		}
	}

	sort.Strings(result)
	return result
}

func firmwareInstructions(
	root map[string]any,
	path string,
) []string {
	value, ok := portable.Lookup(root, path)
	if !ok {
		return nil
	}

	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	order := []string{
		"enterFirmware",
		"enterSetupMode",
		"preserveDatabases",
		"avoidResetAll",
	}

	result := make([]string, 0, len(object))
	used := map[string]bool{}

	for _, key := range order {
		text, ok := object[key].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}

		result = append(result, text)
		used[key] = true
	}

	extra := make([]string, 0)
	for key := range object {
		if !used[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)

	for _, key := range extra {
		if text, ok := object[key].(string); ok &&
			strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}

	return result
}

func cloneSecureBootFirmwarePolicy(
	policy SecureBootFirmwarePolicy,
) SecureBootFirmwarePolicy {
	policy.RequiredPresent = append(
		[]string(nil),
		policy.RequiredPresent...,
	)
	policy.RequiredAbsent = append(
		[]string(nil),
		policy.RequiredAbsent...,
	)
	policy.PreserveFirmwareBuiltin = append(
		[]string(nil),
		policy.PreserveFirmwareBuiltin...,
	)
	policy.Untouched = append(
		[]string(nil),
		policy.Untouched...,
	)
	policy.Instructions = append(
		[]string(nil),
		policy.Instructions...,
	)

	return policy
}

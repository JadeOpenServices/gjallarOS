package oddc

import (
	"fmt"
	"slices"
	"strings"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"
)

// Plane 391 / ODDC-128: on firmware without a PKDefault efivar (e.g.
// ThinkPad T480s) the factory Platform Key cannot be confirmed by
// comparison. ODDC lists the model's factory PK certificate sums as a fact
// and names, in a separate ownership policy, the proof that uses them. The
// sbctl recipe (vendor.policy.secureBoot) is independent of both.
const (
	factoryPKFingerprintsPath = "hardware.firmware.secureBoot.factoryPKFingerprints"
	ownershipPolicyPath       = "policy.secureBootOwnership"

	// OwnershipProofPKInFactoryList is the only proof this installer reads:
	// the SHA-256 of the DER certificate in the first entry of the active PK
	// signature list is in the factory list.
	OwnershipProofPKInFactoryList = "pk-sha256-in-factory-list"

	// OwnershipExperimental: the proof holds, but shim enrollment has not
	// been shown on this model; the user is asked once, unattended stops.
	OwnershipExperimental = "experimental"
	// OwnershipVerified: ODDC holds guided-enrollment evidence for this
	// exact model; no question.
	OwnershipVerified = "verified"
)

// SecureBootOwnership is the ODDC ownership policy with the fact it points
// at. The zero value is "absent": every gap reads as absent, never as
// proven (fail closed).
type SecureBootOwnership struct {
	Status                string
	FactoryPKFingerprints []string
	// Note says why a present but unusable entry was treated as absent.
	Note string
}

// Present reports a usable policy with a usable factory list.
func (o SecureBootOwnership) Present() bool {
	return o.Status != "" && len(o.FactoryPKFingerprints) != 0
}

// Verified reports a present policy that ODDC marked verified.
func (o SecureBootOwnership) Verified() bool {
	return o.Present() && o.Status == OwnershipVerified
}

// MatchesFactoryPK reports whether the active PK (lowercase hex SHA-256 of
// its certificate) is on the model's factory list, on firmware that exposes
// no PKDefault efivar. A present PKDefault keeps the strict comparison.
func (o SecureBootOwnership) MatchesFactoryPK(pkSHA256 string, pkDefaultPresent bool) bool {
	return o.Present() && !pkDefaultPresent && pkSHA256 != "" &&
		slices.Contains(o.FactoryPKFingerprints, pkSHA256)
}

// ResolveSecureBootOwnership reads the ownership policy and the factory PK
// fact. Missing or unknown status, an unknown proof, or a missing, empty or
// malformed list all give the absent value; Note says why when something
// was there.
func ResolveSecureBootOwnership(resolved Resolved) SecureBootOwnership {
	root := resolved.Canonical.Resolved
	if resolved.ModelID == "" || root == nil {
		return SecureBootOwnership{}
	}
	if _, ok := portable.Lookup(root, ownershipPolicyPath); !ok {
		return SecureBootOwnership{}
	}
	absent := func(format string, args ...any) SecureBootOwnership {
		return SecureBootOwnership{Note: "ODDC Secure Boot ownership entry ignored: " + fmt.Sprintf(format, args...)}
	}
	// Loophole 163: a project or host overlay may override any keyed path,
	// and the pinned ODDC does not stop that for any path, security findings
	// included (ODDC-129 plans the guard; not built). An ownership claim
	// raises trust, so it counts only straight from the catalog. This check
	// is the only guard until ODDC-129 lands.
	if source, ok := nonCatalogSource(resolved, ownershipPolicyPath, factoryPKFingerprintsPath); ok {
		return absent("set by %q, not by the ODDC catalog", source)
	}

	proof := stringPath(root, ownershipPolicyPath+".proof")
	if proof != OwnershipProofPKInFactoryList {
		return absent("unknown proof %q", proof)
	}
	status := stringPath(root, ownershipPolicyPath+".status")
	switch status {
	case OwnershipExperimental, OwnershipVerified:
	default:
		return absent("unknown status %q", status)
	}

	raw, ok := portable.Lookup(root, factoryPKFingerprintsPath)
	if !ok {
		return absent("the factory PK list is missing")
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return absent("the factory PK list is empty")
	}
	list := make([]string, 0, len(items))
	for _, item := range items {
		fp, ok := item.(string)
		if !ok || !isSHA256Hex(fp) {
			return absent("a factory PK entry is not a lowercase hex SHA-256 sum")
		}
		if slices.Contains(list, fp) {
			return absent("the factory PK list has a duplicate")
		}
		list = append(list, fp)
	}
	return SecureBootOwnership{Status: status, FactoryPKFingerprints: list}
}

// nonCatalogSource returns the first provenance source that is not the
// catalog for any path at, under or above one of paths.
func nonCatalogSource(resolved Resolved, paths ...string) (string, bool) {
	keys := make([]string, 0, len(resolved.Canonical.Provenance))
	for key := range resolved.Canonical.Provenance {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		source := resolved.Canonical.Provenance[key].Source
		if strings.HasPrefix(source, "catalog:") {
			continue
		}
		for _, path := range paths {
			if key == path || strings.HasPrefix(key, path+".") || strings.HasPrefix(path, key+".") {
				return source, true
			}
		}
	}
	return "", false
}

func isSHA256Hex(s string) bool {
	return len(s) == 64 && strings.TrimRight(s, "0123456789abcdef") == ""
}

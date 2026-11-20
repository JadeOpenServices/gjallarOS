package oddc

import "testing"

// Plane 388: factory-PK facts let a model's policy prove the active PK is
// the factory one when the firmware exposes no PKDefault efivar.
func TestMatchesFactoryPK(t *testing.T) {
	fp := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policy := SecureBootFirmwarePolicy{
		Supported:             true,
		FactoryPKFingerprints: []string{fp},
		PKDefaultAbsent:       true,
	}
	for _, tc := range []struct {
		name     string
		policy   SecureBootFirmwarePolicy
		pkSHA    string
		pkDef    bool
		want     bool
	}{
		{"fingerprint match", policy, fp, false, true},
		{"fingerprint mismatch, pkDefaultAbsent", policy, "bb", false, true},
		{"pkDefault present never matches", policy, fp, true, false},
		{"unsupported policy never matches", SecureBootFirmwarePolicy{Supported: false, PKDefaultAbsent: true}, fp, false, false},
		{"no facts no match", SecureBootFirmwarePolicy{Supported: true}, fp, false, false},
	} {
		if got := tc.policy.MatchesFactoryPK(tc.pkSHA, tc.pkDef); got != tc.want {
			t.Errorf("%s: MatchesFactoryPK=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidateSecureBootFirmwarePolicyFactoryPKFacts(t *testing.T) {
	base := SecureBootFirmwarePolicy{
		Supported:             true,
		FirmwareName:          "fw",
		SetupModeStrategy:     "clear-platform-key",
		EnrollmentBackend:     "sbctl",
		FactoryOwnershipProof: "pk-equals-pkdefault",
		Instructions:          []string{"do it"},
	}
	fp := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	ok := base
	ok.FactoryPKFingerprints = []string{fp}
	ok.PKDefaultAbsent = true
	if err := validateSecureBootFirmwarePolicy(ok); err != nil {
		t.Errorf("valid factory-PK facts rejected: %v", err)
	}

	bad := base
	bad.FactoryPKFingerprints = []string{"nothex"}
	if err := validateSecureBootFirmwarePolicy(bad); err == nil {
		t.Error("non-hex fingerprint accepted")
	}

	dup := base
	dup.FactoryPKFingerprints = []string{fp, fp}
	if err := validateSecureBootFirmwarePolicy(dup); err == nil {
		t.Error("duplicate fingerprint accepted")
	}

	unsup := SecureBootFirmwarePolicy{
		Supported:         false,
		SetupModeStrategy: "unsupported",
		UnsupportedReason: "none",
		PKDefaultAbsent:   true,
	}
	if err := validateSecureBootFirmwarePolicy(unsup); err == nil {
		t.Error("unsupported policy with pkDefaultAbsent accepted")
	}
}

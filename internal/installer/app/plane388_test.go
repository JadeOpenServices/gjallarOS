package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// Plane 388: on the shim path with no PKDefault efivar (T480s), the unknown
// PK owner question is asked only interactively, only with Secure Boot on
// and shim started, and only when ODDC fingerprints do not already name the
// active PK. Every refusal comes before disk work. Unattended (a preset
// answers file) gets no answer: N. A planted answers file cannot say yes;
// there is no config key for this, the answer lives in the run only.
func TestPlane388Classify(t *testing.T) {
	fp := strings.Repeat("a", 64)
	matching := oddc.EffectiveSecureBootFirmwarePolicy{Policy: oddc.SecureBootFirmwarePolicy{
		Supported: true, FactoryPKFingerprints: []string{fp},
	}}
	plain := oddc.EffectiveSecureBootFirmwarePolicy{}
	on := secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: true, PKSHA256: fp}
	off := secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: false, PKSHA256: fp}
	factoryFacts := secureboot.ShimOwnerFacts{FactoryPKFingerprints: []string{fp}}
	acceptedFacts := secureboot.ShimOwnerFacts{AcceptedUnknownPKSHA256: fp}

	for _, tc := range []struct {
		name        string
		insp        secureboot.Inspection
		policy      oddc.EffectiveSecureBootFirmwarePolicy
		shimStarted bool
		unattended  bool
		input       string
		want        secureboot.ShimOwnerFacts
		wantErr     string
		wantAsk     bool
	}{
		{"oddc fingerprint match skips the question", on, matching, true, false, "", factoryFacts, "", false},
		{"oddc fingerprint match with SB off", off, matching, false, false, "", factoryFacts, "", false},
		{"oddc match but shim not started refused early", on, matching, false, false, "", secureboot.ShimOwnerFacts{}, "does not boot through shim", false},
		{"interactive y binds the PK", on, plain, true, false, "y\n", acceptedFacts, "", true},
		{"interactive n refused", on, plain, true, false, "n\n", secureboot.ShimOwnerFacts{}, "once ODDC knows this model", true},
		{"unattended preset answers N", on, plain, true, true, "", secureboot.ShimOwnerFacts{}, "unattended", false},
		{"SB off refused before asking", off, plain, false, false, "y\n", secureboot.ShimOwnerFacts{}, "Secure Boot off", false},
		{"shim not started refused before asking", on, plain, false, false, "y\n", secureboot.ShimOwnerFacts{}, "does not boot through shim", false},
		{"unreadable PK refused before asking", secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: true}, plain, true, false, "y\n", secureboot.ShimOwnerFacts{}, "could not be read", false},
		{"PKDefault present stays strict, no question", secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: true, PKDefaultPresent: true, PKSHA256: fp}, matching, true, false, "", secureboot.ShimOwnerFacts{}, "", false},
		{"factory state needs no question", secureboot.Inspection{State: secureboot.StateOEMFactoryDerived, PKDefaultPresent: true}, plain, true, false, "", secureboot.ShimOwnerFacts{}, "", false},
	} {
		ui, out := validationTestUI(tc.input)
		got, err := plane388Classify(context.Background(), ui, out, tc.insp, tc.policy, tc.shimStarted, tc.unattended)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: error %v, want substring %q", tc.name, err, tc.wantErr)
		}
		if !slices.Equal(got.FactoryPKFingerprints, tc.want.FactoryPKFingerprints) || got.AcceptedUnknownPKSHA256 != tc.want.AcceptedUnknownPKSHA256 {
			t.Errorf("%s: facts %+v, want %+v", tc.name, got, tc.want)
		}
		asked := strings.Contains(out.String(), "Continue with the gjallarOS shim now?")
		if asked != tc.wantAsk {
			t.Errorf("%s: asked=%v, want %v\n%s", tc.name, asked, tc.wantAsk, out)
		}
	}
}

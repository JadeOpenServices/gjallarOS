package app

import (
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// Plane 388: on the shim path with no PKDefault efivar (T480s), the unknown
// PK owner question is asked only interactively and only when ODDC facts do
// not already prove the factory PK. Unattended (a preset answers file) gets
// no answer: N, refused before disk work. A planted answers file cannot say
// yes — there is no config key for this, the answer lives in the run only.
func TestPlane388Classify(t *testing.T) {
	fp := strings.Repeat("a", 64)
	matching := oddc.EffectiveSecureBootFirmwarePolicy{Policy: oddc.SecureBootFirmwarePolicy{
		Supported: true, FactoryPKFingerprints: []string{fp},
	}}
	plain := oddc.EffectiveSecureBootFirmwarePolicy{}
	foreignNoPKDefault := secureboot.Inspection{
		State: secureboot.StateForeignManaged, SecureBoot: true,
		PKDefaultPresent: false, PKSHA256: fp,
	}

	for _, tc := range []struct {
		name       string
		insp       secureboot.Inspection
		policy     oddc.EffectiveSecureBootFirmwarePolicy
		unattended bool
		input      string
		wantAccept bool
		wantFact   bool
		wantErr    string
		wantAsk    bool
	}{
		{"oddc fingerprint match skips the question", foreignNoPKDefault, matching, false, "", false, true, "", false},
		{"interactive y accepted", foreignNoPKDefault, plain, false, "y\n", true, false, "", true},
		{"interactive n refused", foreignNoPKDefault, plain, false, "n\n", false, false, "refusing the shim path", true},
		{"unattended preset answers N", foreignNoPKDefault, plain, true, "", false, false, "unattended", false},
		{"PKDefault present stays strict, no question", secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: true, PKDefaultPresent: true, PKSHA256: fp}, plain, false, "", false, false, "", false},
		{"factory state needs no question", secureboot.Inspection{State: secureboot.StateOEMFactoryDerived, PKDefaultPresent: true}, plain, false, "", false, false, "", false},
	} {
		ui, out := validationTestUI(tc.input)
		accepted, factoryMatch, err := plane388Classify(context.Background(), ui, out, tc.insp, tc.policy, tc.unattended)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: error %v, want substring %q", tc.name, err, tc.wantErr)
		}
		if accepted != tc.wantAccept || factoryMatch != tc.wantFact {
			t.Errorf("%s: accepted=%v factoryMatch=%v, want %v/%v", tc.name, accepted, factoryMatch, tc.wantAccept, tc.wantFact)
		}
		asked := strings.Contains(out.String(), "unknown PK owner")
		if asked != tc.wantAsk {
			t.Errorf("%s: asked=%v, want %v\n%s", tc.name, asked, tc.wantAsk, out)
		}
	}
}

package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// Plane 388 / 391: on the shim path with no PKDefault efivar (T480s), ODDC
// ownership decides first: a verified factory-list match needs no question,
// an experimental one asks once, a known list without the active PK asks the
// not-factory question, no usable entry asks the unknown-owner question.
// Every refusal comes before disk work. Unattended (a preset answers file)
// gets no answer: N. A planted answers file cannot say yes; there is no
// config key for this, the answer lives in the run only.
func TestPlane388Classify(t *testing.T) {
	fp := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	verified := oddc.SecureBootOwnership{Status: oddc.OwnershipVerified, FactoryPKFingerprints: []string{fp}}
	experimental := oddc.SecureBootOwnership{Status: oddc.OwnershipExperimental, FactoryPKFingerprints: []string{fp}}
	wrongPK := oddc.SecureBootOwnership{Status: oddc.OwnershipVerified, FactoryPKFingerprints: []string{other}}
	ignored := oddc.SecureBootOwnership{Note: "ODDC Secure Boot ownership entry ignored: unknown status \"trusted\""}
	absent := oddc.SecureBootOwnership{}
	on := secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: true, PKSHA256: fp}
	off := secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: false, PKSHA256: fp}
	factoryFacts := secureboot.ShimOwnerFacts{FactoryPKFingerprints: []string{fp}}
	acceptedFacts := secureboot.ShimOwnerFacts{AcceptedUnknownPKSHA256: fp}
	none := secureboot.ShimOwnerFacts{}
	const (
		askUnknown  = "ODDC does not know this model yet"
		askNotFact  = "is NOT one of them"
		askUntested = "has not been tested on this model yet"
	)

	for _, tc := range []struct {
		name        string
		insp        secureboot.Inspection
		own         oddc.SecureBootOwnership
		shimStarted bool
		unattended  bool
		input       string
		want        secureboot.ShimOwnerFacts
		wantErr     string
		wantAsk     string
		wantOut     string
	}{
		{"verified match skips the question", on, verified, true, false, "", factoryFacts, "", "", "tested on this model"},
		{"verified match with SB off", off, verified, false, false, "", factoryFacts, "", "", "tested on this model"},
		{"verified match unattended", on, verified, true, true, "", factoryFacts, "", "", "tested on this model"},
		{"verified match but shim not started refused early", on, verified, false, false, "", none, "does not boot through shim", "", ""},
		{"experimental match y", on, experimental, true, false, "y\n", factoryFacts, "", askUntested, "untested model accepted"},
		{"experimental match n", on, experimental, true, false, "n\n", none, "untested on this model; stopped", askUntested, ""},
		{"experimental match unattended refused", on, experimental, true, true, "", none, "unattended", "", ""},
		{"wrong PK y binds the PK", on, wrongPK, true, false, "y\n", acceptedFacts, "", askNotFact, "not the factory key accepted"},
		{"wrong PK n refused", on, wrongPK, true, false, "n\n", none, "not one of the factory keys", askNotFact, ""},
		{"wrong PK default is no", on, wrongPK, true, false, "\n", none, "not one of the factory keys", askNotFact, ""},
		{"wrong PK unattended refused", on, wrongPK, true, true, "", none, "unattended", "", ""},
		{"wrong PK SB off refused before asking", off, wrongPK, false, false, "y\n", none, "Secure Boot off", "", ""},
		{"ignored entry prints why and asks unknown", on, ignored, true, false, "y\n", acceptedFacts, "", askUnknown, "entry ignored: unknown status"},
		{"absent: interactive y binds the PK", on, absent, true, false, "y\n", acceptedFacts, "", askUnknown, "unknown PK owner accepted"},
		{"absent: interactive n refused", on, absent, true, false, "n\n", none, "once ODDC knows this model", askUnknown, ""},
		{"absent: unattended preset answers N", on, absent, true, true, "", none, "unattended", "", ""},
		{"absent: SB off refused before asking", off, absent, false, false, "y\n", none, "Secure Boot off", "", ""},
		{"absent: shim not started refused before asking", on, absent, false, false, "y\n", none, "does not boot through shim", "", ""},
		{"unreadable PK refused before asking", secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: true}, verified, true, false, "y\n", none, "could not be read", "", ""},
		{"PKDefault present stays strict, no question", secureboot.Inspection{State: secureboot.StateForeignManaged, SecureBoot: true, PKDefaultPresent: true, PKSHA256: fp}, verified, true, false, "", none, "", "", ""},
		{"factory state needs no question", secureboot.Inspection{State: secureboot.StateOEMFactoryDerived, PKDefaultPresent: true}, absent, true, false, "", none, "", "", ""},
	} {
		ui, out := validationTestUI(tc.input)
		got, err := plane388Classify(context.Background(), ui, out, tc.insp, tc.own, tc.shimStarted, tc.unattended)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: error %v, want substring %q", tc.name, err, tc.wantErr)
		}
		if !slices.Equal(got.FactoryPKFingerprints, tc.want.FactoryPKFingerprints) || got.AcceptedUnknownPKSHA256 != tc.want.AcceptedUnknownPKSHA256 {
			t.Errorf("%s: facts %+v, want %+v", tc.name, got, tc.want)
		}
		text := out.String()
		asked := strings.Contains(text, "Continue with the gjallarOS shim now?")
		if asked != (tc.wantAsk != "") || tc.wantAsk != "" && !strings.Contains(text, tc.wantAsk) {
			t.Errorf("%s: asked=%v, want %q\n%s", tc.name, asked, tc.wantAsk, text)
		}
		if tc.wantOut != "" && !strings.Contains(text, tc.wantOut) {
			t.Errorf("%s: output lacks %q\n%s", tc.name, tc.wantOut, text)
		}
	}
}

package secureboot

import (
	"strings"
	"testing"
)

// Plane 388: the shim path on firmware without a PKDefault efivar proceeds
// only when ODDC factory-PK fingerprints name the active PK, or the unknown
// PK owner was accepted for exactly this PK and shim starts this system with
// Secure Boot on.
func TestShimForeignProceed(t *testing.T) {
	pk := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	on := Inspection{State: StateForeignManaged, SecureBoot: true, PKSHA256: pk}
	off := Inspection{State: StateForeignManaged, SecureBoot: false, PKSHA256: pk}
	withDefault := Inspection{State: StateForeignManaged, SecureBoot: true, PKDefaultPresent: true, PKSHA256: pk}
	noPK := Inspection{State: StateForeignManaged, SecureBoot: true}
	factory := ShimOwnerFacts{FactoryPKFingerprints: []string{pk}}
	accepted := ShimOwnerFacts{AcceptedUnknownPKSHA256: pk}
	for _, tc := range []struct {
		name         string
		insp         Inspection
		facts        ShimOwnerFacts
		shimStarted  bool
		wantOK       bool
		wantAccepted bool
		wantReason   string
	}{
		{"no facts refused", on, ShimOwnerFacts{}, true, false, false, "neither its factory keys"},
		{"fingerprint match, SB on, shim started", on, factory, true, true, false, "ODDC facts"},
		{"fingerprint match, SB off", off, factory, false, true, false, "ODDC facts"},
		{"fingerprint match, SB on, shim not started", on, factory, false, false, false, "does not boot through shim"},
		{"fingerprint list names another PK", on, ShimOwnerFacts{FactoryPKFingerprints: []string{other}}, true, false, false, "neither its factory keys"},
		{"accepted for this PK", on, accepted, true, true, true, "unknown PK owner was accepted"},
		{"accepted for another PK", on, ShimOwnerFacts{AcceptedUnknownPKSHA256: other}, true, false, false, "neither its factory keys"},
		{"accepted with SB off", off, accepted, false, false, false, "neither its factory keys"},
		{"accepted, shim not started", on, accepted, false, false, false, "does not boot through shim"},
		{"PKDefault present stays strict", withDefault, ShimOwnerFacts{FactoryPKFingerprints: []string{pk}, AcceptedUnknownPKSHA256: pk}, true, false, false, "neither its factory keys"},
		{"unread PK refused", noPK, ShimOwnerFacts{AcceptedUnknownPKSHA256: ""}, true, false, false, "neither its factory keys"},
		{"non-foreign not handled here", Inspection{State: StateOEMFactoryDerived, PKSHA256: pk}, accepted, true, false, false, ""},
	} {
		ok, unknown, reason := shimForeignProceed(tc.insp, tc.facts, tc.shimStarted)
		if ok != tc.wantOK || unknown != tc.wantAccepted {
			t.Errorf("%s: proceed=%v unknown=%v, want %v/%v (reason %q)", tc.name, ok, unknown, tc.wantOK, tc.wantAccepted, reason)
		}
		if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
			t.Errorf("%s: reason %q misses %q", tc.name, reason, tc.wantReason)
		}
	}
}

// Plane 388: a pending shim record on firmware without PKDefault stays
// pending only for the PK it was written for. A PK swapped after the
// install question (or a record from another machine) does not count.
func TestShimRecordBindsPK(t *testing.T) {
	pk := strings.Repeat("a", 64)
	rec := ownershipRecord{FirmwarePKSHA256: pk}
	for _, tc := range []struct {
		name        string
		rec         ownershipRecord
		pkDefault   bool
		pkSHA       string
		wantPending bool
	}{
		{"same PK", rec, false, pk, true},
		{"PK changed", rec, false, strings.Repeat("c", 64), false},
		{"PK unreadable", rec, false, "", false},
		{"record without PK binding", ownershipRecord{}, false, pk, false},
		{"empty binding, empty PK", ownershipRecord{}, false, "", false},
		{"PKDefault appeared", rec, true, pk, false},
	} {
		if got := shimRecordBindsPK(tc.rec, tc.pkDefault, tc.pkSHA); got != tc.wantPending {
			t.Errorf("%s: pending=%v, want %v", tc.name, got, tc.wantPending)
		}
	}
}

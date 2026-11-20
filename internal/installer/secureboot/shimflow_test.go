package secureboot

import "testing"

// Plane 388: the shim path on firmware without a PKDefault efivar proceeds
// only when ODDC model facts match the factory PK, or the unknown PK owner
// was accepted and shim actually starts this system with Secure Boot on.
func TestShimForeignProceed(t *testing.T) {
	foreign := Inspection{State: StateForeignManaged, SecureBoot: true, PKDefaultPresent: false}
	for _, tc := range []struct {
		name       string
		insp       Inspection
		factory    bool
		accepted   bool
		shimStart  bool
		wantOK     bool
		wantReason string
	}{
		{"plain foreign refused", foreign, false, false, true, false, "neither its factory keys"},
		{"oddc factory match proceeds", foreign, true, false, false, true, "ODDC facts"},
		{"accepted + shim started proceeds", foreign, false, true, true, true, "unknown PK owner was accepted"},
		{"accepted but PKDefault present stays strict", Inspection{State: StateForeignManaged, SecureBoot: true, PKDefaultPresent: true}, false, true, true, false, "neither its factory keys"},
		{"accepted but Secure Boot off refused", Inspection{State: StateForeignManaged, SecureBoot: false, PKDefaultPresent: false}, false, true, false, false, "neither its factory keys"},
		{"accepted but shim not started refused", foreign, false, true, false, false, "neither its factory keys"},
		{"oddc match needs no PKDefault", Inspection{State: StateForeignManaged, SecureBoot: true, PKDefaultPresent: true}, true, false, false, false, ""},
		{"non-foreign not handled here", Inspection{State: StateOEMFactoryDerived}, true, true, true, false, ""},
	} {
		ok, reason := shimForeignProceed(tc.insp, tc.factory, tc.accepted, tc.shimStart)
		if ok != tc.wantOK {
			t.Errorf("%s: proceed=%v, want %v (reason %q)", tc.name, ok, tc.wantOK, reason)
		}
		if tc.wantReason != "" && !contains(reason, tc.wantReason) {
			t.Errorf("%s: reason %q misses %q", tc.name, reason, tc.wantReason)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || index(s, sub) >= 0)
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

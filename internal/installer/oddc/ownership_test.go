package oddc_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc/oddctest"
	portable "github.com/JadeOpenServices/oddc/pkg/oddc"
)

// Plane 391 / ODDC-128: the factory PK list is a hardware fact, the
// ownership policy names the proof that uses it. Every gap reads as absent.
const (
	fpA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fpB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func ownershipResolved(fact any, policy any) oddc.Resolved {
	root := map[string]any{}
	if fact != nil {
		root["hardware"] = map[string]any{"firmware": map[string]any{
			"secureBoot": map[string]any{"factoryPKFingerprints": fact},
		}}
	}
	if policy != nil {
		root["policy"] = map[string]any{"secureBootOwnership": policy}
	}
	return oddc.Resolved{ModelID: "model", Canonical: portable.Resolved{Resolved: root}}
}

func proofPolicy(status string) map[string]any {
	return map[string]any{"proof": oddc.OwnershipProofPKInFactoryList, "status": status}
}

func TestResolveSecureBootOwnership(t *testing.T) {
	both := ownershipResolved([]any{fpA, fpB}, proofPolicy("experimental"))
	for _, tc := range []struct {
		name     string
		resolved oddc.Resolved
		status   string
		note     string
	}{
		{"no model", oddc.Resolved{}, "", ""},
		{"fact only", ownershipResolved([]any{fpA}, nil), "", ""},
		{"policy only", ownershipResolved(nil, proofPolicy("verified")), "", "list is missing"},
		{"both experimental", both, "experimental", ""},
		{"both verified", ownershipResolved([]any{fpA}, proofPolicy("verified")), "verified", ""},
		{"unknown status", ownershipResolved([]any{fpA}, proofPolicy("trusted")), "", "unknown status"},
		{"status missing", ownershipResolved([]any{fpA}, map[string]any{"proof": oddc.OwnershipProofPKInFactoryList}), "", "unknown status"},
		{"unknown proof", ownershipResolved([]any{fpA}, map[string]any{"proof": "pk-equals-pkdefault", "status": "verified"}), "", "unknown proof"},
		{"policy not an object", ownershipResolved([]any{fpA}, "verified"), "", "unknown proof"},
		{"empty list", ownershipResolved([]any{}, proofPolicy("verified")), "", "list is empty"},
		{"list not a list", ownershipResolved(fpA, proofPolicy("verified")), "", "list is empty"},
		{"non-string entry", ownershipResolved([]any{fpA, 7}, proofPolicy("verified")), "", "not a lowercase hex"},
		{"upper-case hex", ownershipResolved([]any{strings.ToUpper(fpA)}, proofPolicy("verified")), "", "not a lowercase hex"},
		{"short sum", ownershipResolved([]any{fpA[:63]}, proofPolicy("verified")), "", "not a lowercase hex"},
		{"duplicate", ownershipResolved([]any{fpA, fpA}, proofPolicy("verified")), "", "duplicate"},
	} {
		got := oddc.ResolveSecureBootOwnership(tc.resolved)
		if got.Status != tc.status {
			t.Errorf("%s: status %q, want %q", tc.name, got.Status, tc.status)
		}
		if tc.status == "" && (got.Present() || len(got.FactoryPKFingerprints) != 0) {
			t.Errorf("%s: absent entry still carries a list: %+v", tc.name, got)
		}
		if tc.note == "" && got.Note != "" || tc.note != "" && !strings.Contains(got.Note, tc.note) {
			t.Errorf("%s: note %q, want %q", tc.name, got.Note, tc.note)
		}
	}
	if got := oddc.ResolveSecureBootOwnership(both); !slices.Equal(got.FactoryPKFingerprints, []string{fpA, fpB}) {
		t.Errorf("list %v, want both sums", got.FactoryPKFingerprints)
	}
}

// The sbctl recipe and the ownership proof are independent: the ownership
// entry resolves without a recipe, and the old recipe-side list is not read.
func TestSecureBootOwnershipIndependentOfRecipe(t *testing.T) {
	recipeAbsent := ownershipResolved([]any{fpA}, proofPolicy("verified"))
	if got := oddc.ResolveSecureBootOwnership(recipeAbsent); !got.Verified() {
		t.Fatalf("ownership without a recipe not resolved: %+v", got)
	}
	policy, err := oddc.ResolveSecureBootFirmwarePolicy(recipeAbsent)
	if err != nil || policy.Policy.Supported {
		t.Fatalf("recipe appeared from the ownership entry: %+v %v", policy, err)
	}

	oldPlace := oddc.Resolved{ModelID: "model", Canonical: portable.Resolved{Resolved: map[string]any{
		"vendor": map[string]any{"policy": map[string]any{"secureBoot": map[string]any{
			"factoryPKFingerprints": []any{fpA},
		}}},
		"policy": map[string]any{"secureBootOwnership": proofPolicy("verified")},
	}}}
	if got := oddc.ResolveSecureBootOwnership(oldPlace); got.Present() {
		t.Fatalf("list under vendor.policy.secureBoot was read: %+v", got)
	}
}

func TestSecureBootOwnershipMatchesFactoryPK(t *testing.T) {
	known := oddc.ResolveSecureBootOwnership(ownershipResolved([]any{fpA}, proofPolicy("experimental")))
	for _, tc := range []struct {
		name  string
		own   oddc.SecureBootOwnership
		pk    string
		pkDef bool
		want  bool
	}{
		{"match", known, fpA, false, true},
		{"wrong PK", known, fpB, false, false},
		{"empty PK", known, "", false, false},
		{"PKDefault present stays strict", known, fpA, true, false},
		{"absent policy", oddc.SecureBootOwnership{}, fpA, false, false},
	} {
		if got := tc.own.MatchesFactoryPK(tc.pk, tc.pkDef); got != tc.want {
			t.Errorf("%s: MatchesFactoryPK=%v, want %v", tc.name, got, tc.want)
		}
	}
	if known.Verified() {
		t.Error("experimental entry reported as verified")
	}
}

// Loophole 163: an overlay must not raise an ownership claim.
func TestSecureBootOwnershipIgnoresOverlaySources(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"status", "policy.secureBootOwnership.status"},
		{"whole policy", "policy.secureBootOwnership"},
		{"parent", "policy"},
		{"list", "hardware.firmware.secureBoot.factoryPKFingerprints"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := ownershipResolved([]any{fpA}, proofPolicy("verified"))
			resolved.Canonical.Provenance = map[string]portable.Provenance{
				"policy.secureBootOwnership.proof": {Source: "catalog:policy/x"},
				tc.path:                            {Source: "host:host/test"},
			}
			got := oddc.ResolveSecureBootOwnership(resolved)
			if got.Present() || !strings.Contains(got.Note, `"host:host/test"`) {
				t.Fatalf("overlay source accepted: %+v", got)
			}
		})
	}

	resolved := ownershipResolved([]any{fpA}, proofPolicy("verified"))
	resolved.Canonical.Provenance = map[string]portable.Provenance{
		"policy.secureBootOwnership.status": {Source: "catalog:policy/x"},
		"policy.graphics":                   {Source: "host:host/test"},
	}
	if got := oddc.ResolveSecureBootOwnership(resolved); !got.Verified() {
		t.Fatalf("unrelated overlay path blocked the catalog entry: %+v", got)
	}
}

// The real resolver tags an overlay-set path with its "host:" source, which
// is what the guard above reads.
func TestHostOverlayOwnershipIsIgnored(t *testing.T) {
	for _, answer := range oddctest.Answers(t) {
		source := oddc.EmbeddedSource{Root: answer.Root, Repository: "embedded:test"}
		host := oddc.HostOverlay{
			APIVersion:  portable.EntityAPIVersion,
			ID:          "host/test",
			Kind:        "host",
			TargetModel: answer.Model,
			Overrides: map[string]any{"policy": map[string]any{
				"secureBootOwnership": proofPolicy("verified"),
			}},
		}
		resolved, err := source.ResolveWithHost(oddc.Identity(answer.Identity), []oddc.HostOverlay{host})
		if err != nil {
			t.Fatal(err)
		}
		if got := resolved.Canonical.Provenance["policy.secureBootOwnership.status"].Source; got != "host:host/test" {
			t.Fatalf("%s: status provenance %q", answer.Model, got)
		}
		got := oddc.ResolveSecureBootOwnership(resolved)
		if got.Present() || !strings.Contains(got.Note, "not by the ODDC catalog") {
			t.Fatalf("%s: host ownership accepted: %+v", answer.Model, got)
		}
	}
}

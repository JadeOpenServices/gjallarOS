package usbtrust

import (
	"testing"
	"time"
)

// SYNTHETIC: the dock from dock_security_test.go, accepted 2026-09-17.
var lapseNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func thirtyDays() Lapse { return Lapse{After: 30 * 24 * time.Hour, Now: lapseNow} }

func TestLapseTimes(t *testing.T) {
	dock := syntheticDock()
	if _, ok := (Lapse{Now: lapseNow}).LapsesAt(dock); ok {
		t.Fatal("lapse without a number of days")
	}
	internal := internalTrusted()
	if _, ok := thirtyDays().LapsesAt(internal); ok {
		t.Fatal("built-in part lapses")
	}
	if !thirtyDays().Lapsed(dock) {
		t.Fatal("record without lastUsed falls back to lastAccepted, 32 days ago")
	}
	cases := []struct {
		lastUsed string
		lapsed   bool
	}{
		{"2026-10-18T12:00:00Z", false},
		{"2026-09-19T12:00:01Z", false},
		{"2026-09-19T12:00:00Z", true},
		{"2026-10-20T11:00:00Z", false},
		{"2026-10-20T12:00:01Z", true},
		{"yesterday", true},
	}
	for _, c := range cases {
		dock.LastUsed = c.lastUsed
		if got := thirtyDays().Lapsed(dock); got != c.lapsed {
			t.Errorf("lastUsed %s: lapsed %v, want %v", c.lastUsed, got, c.lapsed)
		}
	}
}

func TestLapsedTrustAsksOnNewConnectionOnly(t *testing.T) {
	dock := syntheticDock()
	dock.LastUsed = "2026-09-20T13:00:00Z"
	observed := []ObservedDevice{{RuntimeID: "7", Identity: dock.Identity}}
	input := AuditInput{Trusted: trustedDocument(dock), Observed: observed, Lapse: thirtyDays()}
	s := &Sessions{}
	if err := s.Observe(observed); err != nil {
		t.Fatal(err)
	}
	live, err := s.Derive(input)
	if err != nil || live[0].Target != TargetAllow || live[0].Reason != string(CodeTrustedExternal) {
		t.Fatalf("still trusted: %v %v", live, err)
	}
	input.Lapse.Now = lapseNow.Add(48 * time.Hour)
	kept, _ := s.Derive(input)
	if kept[0].Target != TargetAllow || kept[0].Reason != string(CodeTrustedExternalLapsed) {
		t.Fatalf("lapse cut off a connected device: %v", kept)
	}
	_ = s.Observe(nil)
	_ = s.Observe(observed)
	again, _ := s.Derive(input)
	if again[0].Target != TargetBlock || again[0].TrustedID != dock.ID {
		t.Fatalf("lapsed device allowed on reconnect: %v", again)
	}
	offered := map[Action]bool{}
	for _, action := range again[0].Actions {
		offered[action] = true
	}
	if !offered[ActionAcceptReplacement] || !offered[ActionAllowOnce] || offered[ActionTrustPermanent] {
		t.Fatalf("lapsed device actions: %v", again[0].Actions)
	}
}

func TestRefreshLastUsed(t *testing.T) {
	dock := syntheticDock()
	dock.LastUsed = "2026-10-18T11:00:00Z"
	observed := []ObservedDevice{{RuntimeID: "7", Identity: dock.Identity}}
	doc := trustedDocument(dock)
	input := AuditInput{Trusted: doc, Observed: observed, Lapse: thirtyDays()}
	changed, err := RefreshLastUsed(input, doc)
	if err != nil || !changed || doc.Devices[0].LastUsed != "2026-10-19T12:00:00Z" {
		t.Fatalf("refresh after a day: %v %v %s", changed, err, doc.Devices[0].LastUsed)
	}
	input.Lapse.Now = lapseNow.Add(23 * time.Hour)
	if changed, _ := RefreshLastUsed(input, doc); changed {
		t.Fatal("refreshed twice in a day")
	}

	lapsed := syntheticDock()
	lapsed.LastUsed = "2026-09-01T00:00:00Z"
	doc = trustedDocument(lapsed)
	input = AuditInput{Trusted: doc, Observed: observed, Lapse: thirtyDays()}
	if changed, _ := RefreshLastUsed(input, doc); changed {
		t.Fatal("lapsed device renewed by being plugged in")
	}

	future := syntheticDock()
	future.LastUsed = "2027-01-01T00:00:00Z"
	doc = trustedDocument(future)
	input = AuditInput{Trusted: doc, Observed: observed, Lapse: thirtyDays()}
	if changed, _ := RefreshLastUsed(input, doc); changed {
		t.Fatal("record from the future refreshed instead of asking")
	}
}

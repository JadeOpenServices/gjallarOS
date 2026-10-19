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

	// A one-day lapse refreshes twice a day, so a device that stays
	// connected never runs out.
	doc.Devices[0].LastUsed = "2026-10-19T00:00:00Z"
	input.Lapse = Lapse{After: 24 * time.Hour, Now: lapseNow}
	if changed, _ := RefreshLastUsed(input, doc); !changed {
		t.Fatal("one-day lapse not refreshed after 12 hours")
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

func TestRenewableOnlyInTheWarningWindow(t *testing.T) {
	dock := syntheticDock()
	cases := []struct {
		lastUsed  string
		renewable bool
	}{
		{"2026-10-18T12:00:00Z", false},
		{"2026-09-22T12:00:01Z", false},
		{"2026-09-22T12:00:00Z", true},
		{"2026-09-19T12:00:01Z", true},
		{"2026-09-19T12:00:00Z", false},
		{"2027-01-01T00:00:00Z", false},
		{"yesterday", false},
	}
	for _, c := range cases {
		dock.LastUsed = c.lastUsed
		if got := thirtyDays().Renewable(dock); got != c.renewable {
			t.Errorf("lastUsed %s: renewable %v, want %v", c.lastUsed, got, c.renewable)
		}
	}
	if (Lapse{Now: lapseNow}).Renewable(dock) {
		t.Fatal("renewable while lapse is off")
	}
	// A one-day lapse warns half a day ahead, so a fresh renewal is quiet.
	oneDay := Lapse{After: 24 * time.Hour, Now: lapseNow}
	for lastUsed, renewable := range map[string]bool{
		"2026-10-19T11:00:00Z": false,
		"2026-10-19T00:00:01Z": false,
		"2026-10-19T00:00:00Z": true,
		"2026-10-18T12:00:01Z": true,
		"2026-10-18T12:00:00Z": false,
	} {
		dock.LastUsed = lastUsed
		if got := oneDay.Renewable(dock); got != renewable {
			t.Errorf("one-day lapse, lastUsed %s: renewable %v, want %v", lastUsed, got, renewable)
		}
	}
}

func TestLapseClockStartsWhenSwitchedOn(t *testing.T) {
	dock := syntheticDock()
	dock.LastUsed = "2026-08-01T00:00:00Z"
	l := thirtyDays()
	if !l.Lapsed(dock) {
		t.Fatal("record unused for 79 days kept without a start")
	}
	l.Since = lapseNow.Add(-24 * time.Hour)
	if l.Lapsed(dock) {
		t.Fatal("record lapsed the day after lapsing was switched on")
	}
	if at, _ := l.LapsesAt(dock); !at.Equal(l.Since.Add(l.After)) {
		t.Fatalf("lapses at %s, want 30 days after the start", at)
	}
	l.Since = lapseNow.Add(48 * time.Hour)
	if !l.Lapsed(dock) {
		t.Fatal("a start in the future bought extra days")
	}

	doc := trustedDocument()
	doc.LapseSince = "yesterday"
	if doc.Validate() == nil {
		t.Fatal("accepted a broken lapseSince")
	}
	doc.LapseSince = "2026-10-18T12:00:00Z"
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := NewLapse(30*24*time.Hour, lapseNow, doc).Since; !got.Equal(lapseNow.Add(-24 * time.Hour)) {
		t.Fatalf("since %s", got)
	}
	if got := NewLapse(0, lapseNow, doc); got.Enabled() || !got.Since.IsZero() {
		t.Fatalf("lapse off: %+v", got)
	}
}

// SYNTHETIC: input devices built from the dock record.
func inputDevice(id, lastUsed string, interfaces ...string) Device {
	device := syntheticDock()
	device.ID = id
	device.LastUsed = lastUsed
	device.Identity.Hash = id + "-hash"
	device.Identity.Interfaces = interfaces
	return device
}

func TestLastUsedKeyboardAndMouseNeverLapse(t *testing.T) {
	older := inputDevice("external:keyboard-a", "2026-08-01T00:00:00Z", "03:01:01")
	newer := inputDevice("external:keyboard-b", "2026-08-02T00:00:00Z", "03:01:01")
	mouse := inputDevice("external:mouse", "2026-07-01T00:00:00Z", "03:01:02")
	pad := inputDevice("external:pad", "2026-08-03T00:00:00Z", "03:00:00")
	doc := trustedDocument(older, newer, mouse, pad)
	kept := KeptInput(*doc)
	if len(kept) != 2 || !kept[newer.ID] || !kept[mouse.ID] {
		t.Fatalf("kept %v, want the newer keyboard and the mouse", kept)
	}
	l := NewLapse(30*24*time.Hour, lapseNow, doc)
	if l.Lapsed(newer) || l.Lapsed(mouse) || !l.Lapsed(older) || !l.Lapsed(pad) {
		t.Fatal("wrong records lapsed")
	}
	if l.Renewable(newer) {
		t.Fatal("kept keyboard offered for renewal")
	}

	builtIn := internalTrusted()
	builtIn.Identity.Interfaces = []string{"03:01:01"}
	doc.Devices = append(doc.Devices, builtIn)
	if kept := KeptInput(*doc); len(kept) != 0 {
		t.Fatalf("kept %v beside a built-in keyboard", kept)
	}
	if !NewLapse(30*24*time.Hour, lapseNow, doc).Lapsed(newer) {
		t.Fatal("external keyboard kept beside a built-in one")
	}
}

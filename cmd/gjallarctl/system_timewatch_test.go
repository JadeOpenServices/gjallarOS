package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// SYNTHETIC times for the watch tests.
var (
	watchStart = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	watchStamp = watchStart.Add(-30 * time.Hour)
	badLines   = []string{"Time: not confirmed since 2026-10-20 03:00 UTC"}
	goodLines  = []string{"Time: confirmed by NTS at 2026-10-21 08:55 UTC"}
)

func TestTimeWatchConfirmedHidesIcon(t *testing.T) {
	var w timeWatch
	v := w.step(goodLines, false, watchStamp, true, watchStart, "")
	if v.Kind != "hidden" || v.Card {
		t.Fatalf("view = %+v", v)
	}
}

func TestTimeWatchGraceBeforeIcon(t *testing.T) {
	var w timeWatch
	if v := w.step(badLines, false, watchStamp, false, watchStart, ""); v.Kind != "hidden" {
		t.Fatalf("first poll: %+v", v)
	}
	v := w.step(badLines, false, watchStamp, false, watchStart.Add(timeBarGrace), "")
	if v.Kind != "unconfirmed" || v.Card {
		t.Fatalf("after grace: %+v", v)
	}
	if len(v.Tooltip) > 90 || !strings.Contains(v.Tooltip, "since") {
		t.Fatalf("tooltip %q", v.Tooltip)
	}
	// Confirmed again: the grace starts over.
	w.step(goodLines, false, watchStamp, false, watchStart.Add(6*time.Minute), "")
	if v := w.step(badLines, false, watchStamp, false, watchStart.Add(7*time.Minute), ""); v.Kind != "hidden" {
		t.Fatalf("after confirm: %+v", v)
	}
}

func TestTimeWatchCardOnlineOnce(t *testing.T) {
	var w timeWatch
	now := watchStart
	// Offline: icon only.
	w.step(badLines, false, watchStamp, false, now, "")
	if v := w.step(badLines, false, watchStamp, false, now.Add(time.Hour), ""); v.Card {
		t.Fatalf("card while offline: %+v", v)
	}
	// Online, but not long enough for NTS to have had its chance.
	now = now.Add(2 * time.Hour)
	if v := w.step(badLines, false, watchStamp, true, now, ""); v.Card {
		t.Fatalf("card right after going online: %+v", v)
	}
	v := w.step(badLines, false, watchStamp, true, now.Add(timeCardOnline), "")
	if !v.Card || v.Key != "1792465200" {
		t.Fatalf("no card: %+v", v)
	}
	// The same spell never gets a second card.
	if v := w.step(badLines, false, watchStamp, true, now.Add(time.Hour), v.Key); v.Card {
		t.Fatalf("second card: %+v", v)
	}
	// A new spell after a later confirmation does.
	later := watchStamp.Add(time.Hour)
	if v := w.step(badLines, false, later, true, now.Add(time.Hour), "1792465200"); !v.Card {
		t.Fatalf("new spell: %+v", v)
	}
}

func TestTimeWatchCardWaits24Hours(t *testing.T) {
	var w timeWatch
	stamp := watchStart.Add(-23 * time.Hour)
	w.step(badLines, false, stamp, true, watchStart, "")
	if v := w.step(badLines, false, stamp, true, watchStart.Add(59*time.Minute), ""); v.Card {
		t.Fatalf("card before 24 hours: %+v", v)
	}
	if v := w.step(badLines, false, stamp, true, watchStart.Add(time.Hour), ""); !v.Card {
		t.Fatalf("no card at 24 hours: %+v", v)
	}
}

func TestTimeWatchNeverConfirmed(t *testing.T) {
	var w timeWatch
	w.step(badLines, false, time.Time{}, true, watchStart, "")
	v := w.step(badLines, false, time.Time{}, true, watchStart.Add(timeCardAfter-time.Minute), "")
	if v.Kind != "unconfirmed" || v.Card {
		t.Fatalf("view = %+v", v)
	}
	v = w.step(badLines, false, time.Time{}, true, watchStart.Add(timeCardAfter), "")
	if !v.Card || v.Key != "never" {
		t.Fatalf("view = %+v", v)
	}
}

func TestTimeWatchRouterIconNoCard(t *testing.T) {
	var w timeWatch
	routerLines := []string{"Time: set by the home router at 2026-10-21 08:55 UTC, not confirmed by NTS"}
	for _, d := range []time.Duration{0, time.Hour, 48 * time.Hour} {
		v := w.step(routerLines, true, watchStamp, true, watchStart.Add(d), "")
		if v.Kind != "router" || v.Card || len(v.Tooltip) > 90 {
			t.Fatalf("at %v: %+v", d, v)
		}
	}
}

func TestSystemTimePrintsLastConfirmation(t *testing.T) {
	useClock(t, clockState{Running: true, Synced: true, Ref: watchStart}, watchStart)
	old := readTimeStamp
	readTimeStamp = func() time.Time { return time.Time{} }
	t.Cleanup(func() { readTimeStamp = old })
	var out, errOut bytes.Buffer
	if rc := systemTime(nil, &out, &errOut); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errOut.String())
	}
	if !strings.Contains(out.String(), "Time: confirmed by NTS") || !strings.Contains(out.String(), "never on this install") {
		t.Fatalf("output:\n%s", out.String())
	}
}

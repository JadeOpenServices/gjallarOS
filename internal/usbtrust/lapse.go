package usbtrust

import (
	"slices"
	"time"
)

// ClockSlack is how far a stored time may lie in the future before the
// record counts as lapsed. More than that means the clock went back or the
// record is wrong, and neither may buy extra days.
const ClockSlack = 24 * time.Hour

// Lapse makes permanent external trust run out after After without a
// connection. The zero value never lapses anything.
type Lapse struct {
	After time.Duration
	Now   time.Time
	// Since is when the lapse was switched on. No clock starts earlier, so
	// switching it on never lapses anything at once.
	Since time.Time
	// Kept names the records that never lapse; see KeptInput.
	Kept map[string]bool
}

// NewLapse builds the lapse rule for the stored trust document.
func NewLapse(after time.Duration, now time.Time, trusted *Document) Lapse {
	l := Lapse{After: after, Now: now}
	if after <= 0 || trusted == nil {
		return l
	}
	if since, err := time.Parse(time.RFC3339, trusted.LapseSince); err == nil {
		l.Since = since
	}
	l.Kept = KeptInput(*trusted)
	return l
}

// Boot-protocol HID interfaces. Firmware setup and the disk unlock prompt
// drive only these, so they mark the keyboard and mouse a person needs.
const (
	bootKeyboard = "03:01:01"
	bootMouse    = "03:01:02"
)

// KeptInput picks the external keyboard and mouse that never lapse: the most
// recently used of each, unless a built-in keyboard is stored as an internal
// part. A built-in keyboard that is not on USB is not in the document and
// does not count; keeping one keyboard too many is safer than locking the
// owner out.
func KeptInput(doc Document) map[string]bool {
	for _, device := range doc.Devices {
		if device.Class == ClassInternal && slices.Contains(device.Identity.Interfaces, bootKeyboard) {
			return nil
		}
	}
	kept := map[string]bool{}
	for _, class := range []string{bootKeyboard, bootMouse} {
		best, bestUsed := "", time.Time{}
		for _, device := range doc.Devices {
			if device.Class != ClassExternal || !slices.Contains(device.Identity.Interfaces, class) {
				continue
			}
			used, ok := LastUsed(device)
			if !ok {
				continue
			}
			if best == "" || used.After(bestUsed) || used.Equal(bestUsed) && device.ID < best {
				best, bestUsed = device.ID, used
			}
		}
		if best != "" {
			kept[best] = true
		}
	}
	return kept
}

func (l Lapse) Enabled() bool { return l.After > 0 }

// LastUsed is when the device was last connected under its trust. Records
// written before lastUsed existed fall back to their last approval.
func LastUsed(device Device) (time.Time, bool) {
	stamp := device.LastUsed
	if stamp == "" {
		stamp = device.LastAccepted
	}
	t, err := time.Parse(time.RFC3339, stamp)
	return t, err == nil
}

// LapsesAt is when the device's trust runs out. ok is false when it never
// lapses; a zero time means it has lapsed already for a bad stored time.
func (l Lapse) LapsesAt(device Device) (at time.Time, ok bool) {
	if !l.Enabled() || device.Class != ClassExternal || l.Kept[device.ID] {
		return time.Time{}, false
	}
	used, valid := LastUsed(device)
	if valid && used.Before(l.Since) {
		used = l.Since
	}
	if !valid || used.After(l.Now.Add(ClockSlack)) {
		return time.Time{}, true
	}
	return used.Add(l.After), true
}

func (l Lapse) Lapsed(device Device) bool {
	at, ok := l.LapsesAt(device)
	return ok && !l.Now.Before(at)
}

// LapseWarning is how long before a lapse the owner is offered a renewal.
const LapseWarning = 72 * time.Hour

// Renewable reports whether the device's trust runs out within LapseWarning
// and has not run out yet. Once it has, the device must be connected and
// reviewed again.
func (l Lapse) Renewable(device Device) bool {
	at, ok := l.LapsesAt(device)
	return ok && !at.IsZero() && l.Now.Before(at) && !l.Now.Before(at.Add(-LapseWarning))
}

// refreshEvery is how often lastUsed moves while a device stays connected:
// once a day, or twice per lapse when that is shorter. Refreshing only at
// the full lapse time would let a one-day lapse run out first.
func (l Lapse) refreshEvery() time.Duration {
	if l.Enabled() && l.After/2 < 24*time.Hour {
		return l.After / 2
	}
	return 24 * time.Hour
}

// RefreshLastUsed moves lastUsed to now for external devices that are
// connected under live trust, at most once per refreshEvery. Lapsed devices
// are left alone: only renewing them restarts their clock.
func RefreshLastUsed(input AuditInput, doc *Document) (bool, error) {
	audit, err := Audit(input)
	if err != nil {
		return false, err
	}
	now := input.Lapse.Now.UTC()
	changed := false
	for _, finding := range audit.Findings {
		if finding.Code != CodeTrustedExternal {
			continue
		}
		for i := range doc.Devices {
			device := &doc.Devices[i]
			if device.ID != finding.TrustedID {
				continue
			}
			if used, ok := LastUsed(*device); ok && now.Sub(used) < input.Lapse.refreshEvery() {
				continue
			}
			device.LastUsed = now.Format(time.RFC3339)
			changed = true
		}
	}
	return changed, nil
}

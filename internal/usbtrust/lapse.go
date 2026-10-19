package usbtrust

import "time"

// ClockSlack is how far a stored time may lie in the future before the
// record counts as lapsed. More than that means the clock went back or the
// record is wrong, and neither may buy extra days.
const ClockSlack = 24 * time.Hour

// Lapse makes permanent external trust run out after After without a
// connection. The zero value never lapses anything.
type Lapse struct {
	After time.Duration
	Now   time.Time
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
	if !l.Enabled() || device.Class != ClassExternal {
		return time.Time{}, false
	}
	used, valid := LastUsed(device)
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

// RefreshLastUsed moves lastUsed to now for external devices that are
// connected under live trust, at most once a day. Lapsed devices are left
// alone: only renewing them restarts their clock.
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
			if used, ok := LastUsed(*device); ok && now.Sub(used) < 24*time.Hour {
				continue
			}
			device.LastUsed = now.Format(time.RFC3339)
			changed = true
		}
	}
	return changed, nil
}

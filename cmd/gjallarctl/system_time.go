package main

import (
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// clockState is what chronyd says about the system clock.
type clockState struct {
	Running bool
	// Synced is false when chronyd reports the leap status "Not
	// synchronised".
	Synced bool
	// Ref is the last time a source updated the clock; zero when none has
	// since chronyd started.
	Ref time.Time
	// Started is when chronyd started, as systemd prints it.
	Started string
}

// clockFresh is how old the last update may be before the time counts as
// not confirmed. With four sources chronyd updates far more often.
const clockFresh = 2 * time.Hour

var (
	readClock = chronyClock
	clockNow  = time.Now
)

// chronyClock asks chronyd over its local socket, which every user may do.
func chronyClock() clockState {
	out, err := exec.Command("chronyc", "-c", "-n", "tracking").Output()
	if err != nil {
		return clockState{}
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(fields) < 14 {
		return clockState{}
	}
	state := clockState{Running: true, Synced: fields[13] != "Not synchronised"}
	if ref, err := strconv.ParseFloat(fields[3], 64); err == nil && ref > 0 {
		state.Ref = time.Unix(int64(ref), 0)
	}
	if out, err := exec.Command("systemctl", "show", "chronyd.service", "-p", "ActiveEnterTimestamp", "--value").Output(); err == nil {
		state.Started = strings.TrimSpace(string(out))
	}
	return state
}

// timeLine says whether an NTS source has confirmed the clock lately. Only
// NTS sources can be selected (system/services/time.nix), so any update
// counts as confirmed.
func timeLine(state clockState, now time.Time) string {
	switch {
	case !state.Running:
		return "Time: not confirmed, chronyd is not running"
	case state.Ref.IsZero() && state.Started != "":
		return "Time: not confirmed since chronyd started at " + state.Started
	case state.Ref.IsZero():
		return "Time: not confirmed since chronyd started"
	case !state.Synced || now.Sub(state.Ref) > clockFresh:
		return "Time: not confirmed since " + state.Ref.Local().Format("2006-01-02 15:04 MST")
	default:
		return "Time: confirmed by NTS at " + state.Ref.Local().Format("2006-01-02 15:04 MST")
	}
}

// timeLines adds what gjallar-time-check did this boot. A clock set with
// the certificate date check off is never shown as confirmed until a
// source confirmed it with the check on.
func timeLines(state clockState, boot timeBoot, now time.Time) []string {
	line := timeLine(state, now)
	behind := spanText(time.Duration(boot.Step) * time.Second)
	switch boot.State {
	case "bootstrap":
		if strings.HasPrefix(line, "Time: confirmed") {
			line = "Time: not confirmed, set by NTS with the certificate date check off"
		}
		return []string{line, "  The clock is behind every NTS certificate; it is being set once with the date check off."}
	case "unconfirmed":
		if strings.HasPrefix(line, "Time: confirmed") {
			line = "Time: not confirmed, set by NTS with the certificate date check off"
		}
		return []string{line, "  The clock was " + behind + " behind at boot; set with the date check off, not yet confirmed with it on."}
	case "confirmed":
		return []string{line, "  The clock was " + behind + " behind at boot; set with the date check off, then confirmed with it on."}
	case "failed":
		return []string{line, "  The clock looks behind every NTS certificate; no server answered with the date check off either."}
	case "refused":
		return []string{line, "  An NTS update tried to move the clock back; refused and put back (journalctl -u gjallar-time-check)."}
	}
	return []string{line}
}

// spanText rounds a clock step for people: days from two days on.
func spanText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	case d >= 2*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " hours"
	default:
		return strconv.Itoa(int(d/time.Minute)) + " minutes"
	}
}

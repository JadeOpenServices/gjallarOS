package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

// pauseFixture uses SYNTHETIC network keys and user names.
func pauseFixture(t *testing.T) (*fixture, map[string]nettrust.Pause) {
	t.Helper()
	f := newFixture(t)
	written := map[string]nettrust.Pause{}
	f.svc.pausePath = filepath.Join(t.TempDir(), "pause.json")
	f.svc.writePause = func(path string, p nettrust.Pause) error {
		written[path] = p
		return nil
	}
	f.svc.userName = func(uid uint32) (string, error) { return "synthetic-user", nil }
	return f, written
}

func blocked(network string) nettrust.Verdict {
	return nettrust.Verdict{Trust: nettrust.TrustUntrusted, Blocked: nettrust.BlockExitNodeOffline, Network: network}
}

func TestPauseVPNAsksThePasswordAndWritesOneHour(t *testing.T) {
	f, written := pauseFixture(t)
	f.sys.verdicts = []nettrust.Verdict{blocked("synthetic-net-a")}
	state, err := f.svc.PauseVPN(t.Context(), ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sys.asked) != 1 || !strings.HasPrefix(f.sys.asked[0], pauseAction) {
		t.Fatalf("asked %v, want one %s check", f.sys.asked, pauseAction)
	}
	p := written[f.svc.pausePath]
	if p.Network != "synthetic-net-a" || p.UID != 1000 || p.User != "synthetic-user" ||
		!p.Started.Equal(f.now) || p.Until.Sub(p.Started) != nettrust.MaxPause {
		t.Fatalf("pause = %+v", p)
	}
	if !p.Holds("synthetic-net-a", f.now.Add(59*time.Minute)) || p.Holds("synthetic-net-a", f.now.Add(time.Hour)) {
		t.Fatalf("pause %+v does not hold for exactly one hour", p)
	}
	if state.Blocked != nettrust.BlockExitNodeOffline || !state.Until.Equal(p.Until) {
		t.Fatalf("state = %+v", state)
	}
	if f.sys.reapplied != 1 {
		t.Fatalf("reapplied %d times, want 1", f.sys.reapplied)
	}
}

func TestPauseVPNRefusals(t *testing.T) {
	paused := blocked("synthetic-net-a")
	paused.Blocked = ""
	paused.Paused = &nettrust.PauseState{User: "synthetic-user", Until: time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)}
	unnamed := blocked("")
	open := nettrust.Verdict{Trust: nettrust.TrustUntrusted, Network: "synthetic-net-a"}
	cases := map[string]struct {
		verdicts []nettrust.Verdict
		sender   string
		deny     bool
		asked    int
		want     error
	}{
		"no session":               {[]nettrust.Verdict{blocked("synthetic-net-a")}, ":1.99", false, 0, errRefused},
		"no verdict":               {nil, ":1.10", false, 0, errRefused},
		"not blocked":              {[]nettrust.Verdict{open}, ":1.10", false, 0, errRefused},
		"already paused":           {[]nettrust.Verdict{paused}, ":1.10", false, 0, errRefused},
		"no network key":           {[]nettrust.Verdict{unnamed}, ":1.10", false, 0, errRefused},
		"password refused":         {[]nettrust.Verdict{blocked("synthetic-net-a")}, ":1.10", true, 1, errDenied},
		"network changed meantime": {[]nettrust.Verdict{blocked("synthetic-net-a"), blocked("synthetic-net-b")}, ":1.10", false, 1, errRefused},
		"unblocked meantime":       {[]nettrust.Verdict{blocked("synthetic-net-a"), open}, ":1.10", false, 1, errRefused},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, written := pauseFixture(t)
			f.sys.verdicts = c.verdicts
			f.sys.allow = !c.deny
			_, err := f.svc.PauseVPN(t.Context(), c.sender)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(f.sys.asked) != c.asked {
				t.Fatalf("asked %v, want %d checks", f.sys.asked, c.asked)
			}
			if len(written) != 0 || f.sys.reapplied != 0 {
				t.Fatalf("wrote %v and reapplied %d times after a refusal", written, f.sys.reapplied)
			}
		})
	}
}

func TestResumeVPNNeedsNoPassword(t *testing.T) {
	f, _ := pauseFixture(t)
	if err := os.WriteFile(f.svc.pausePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ResumeVPN(t.Context(), ":1.12"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.svc.pausePath); !os.IsNotExist(err) {
		t.Fatalf("pause file still there: %v", err)
	}
	if len(f.sys.asked) != 0 || f.sys.reapplied != 1 {
		t.Fatalf("asked %v, reapplied %d", f.sys.asked, f.sys.reapplied)
	}
	if err := f.svc.ResumeVPN(t.Context(), ":1.99"); !errors.Is(err, errRefused) {
		t.Fatalf("resume outside a session: %v", err)
	}
}

func TestPauseMessageFitsTheDialog(t *testing.T) {
	for _, b := range []string{nettrust.BlockExitNodeOffline, nettrust.BlockNoExitNode, nettrust.BlockTailscaleDown} {
		m := pauseMessage(b)
		if len(m) > 230 || !strings.Contains(m, nettrust.BlockCause(b)) || !strings.Contains(m, "60 min") {
			t.Fatalf("%d characters: %q", len(m), m)
		}
	}
}

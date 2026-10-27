package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

// SYNTHETIC verdict: a home network proven by a tailnet router.
func homeVerdict() nettrust.Verdict {
	return nettrust.Verdict{
		Trust:         nettrust.TrustHome,
		Router:        "synthetic-router via 192.168.8.1",
		RouterVia:     "192.168.8.1",
		RouterTailnet: []string{"fd7a:115c:a1e0::9", "100.64.0.9"},
		Network:       "synthetic-net-a",
		Bypass:        []string{},
	}
}

func TestRouterCandidates(t *testing.T) {
	name, addrs, reason := routerCandidates(homeVerdict(), "synthetic-net-a")
	if reason != "" || name != "synthetic-router" || !slices.Equal(addrs, []string{"100.64.0.9", "fd7a:115c:a1e0::9", "192.168.8.1"}) {
		t.Fatalf("%q %q %q", name, addrs, reason)
	}
	site := homeVerdict()
	site.Trust = nettrust.TrustSite
	site.RouterTailnet = []string{"not an address\nserver 203.0.113.1"}
	if _, addrs, reason := routerCandidates(site, "synthetic-net-a"); reason != "" || !slices.Equal(addrs, []string{"192.168.8.1"}) {
		t.Fatalf("site: %q %q", addrs, reason)
	}
	for _, c := range []struct {
		edit    func(*nettrust.Verdict)
		network string
		want    string
	}{
		{func(v *nettrust.Verdict) { v.Trust = nettrust.TrustUntrusted }, "synthetic-net-a", "not a proven home"},
		{func(v *nettrust.Verdict) { v.Trust = nettrust.TrustTrustedWifi }, "synthetic-net-a", "not a proven home"},
		// Home from a trusted Wi-Fi on the home subnet, no router proof.
		{func(v *nettrust.Verdict) { v.RouterVia, v.RouterTailnet = "", nil }, "synthetic-net-a", "no tailnet router"},
		{func(*nettrust.Verdict) {}, "synthetic-net-b", "not for the current network"},
		{func(*nettrust.Verdict) {}, "", "not for the current network"},
		{func(v *nettrust.Verdict) { v.RouterVia, v.RouterTailnet = "garbage", nil }, "synthetic-net-a", "no usable address"},
	} {
		v := homeVerdict()
		c.edit(&v)
		if _, addrs, reason := routerCandidates(v, c.network); !strings.Contains(reason, c.want) || addrs != nil {
			t.Errorf("want %q, got %q %q", c.want, reason, addrs)
		}
	}
}

func ntpReply(req []byte) []byte {
	resp := make([]byte, 48)
	resp[0] = 4<<3 | 4
	resp[1] = 2
	copy(resp[24:32], req[40:48])
	copy(resp[40:44], []byte{0xec, 0x00, 0x00, 0x00})
	return resp
}

func TestCheckNTPReply(t *testing.T) {
	req := make([]byte, 48)
	req[0] = 4<<3 | 3
	copy(req[40:48], "SYNTHETI")
	if err := checkNTPReply(req, ntpReply(req)); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func([]byte) []byte{
		"short":        func(r []byte) []byte { return r[:40] },
		"client mode":  func(r []byte) []byte { r[0] = 4<<3 | 3; return r },
		"unsynced":     func(r []byte) []byte { r[0] |= 3 << 6; return r },
		"stratum 0":    func(r []byte) []byte { r[1] = 0; return r },
		"stratum 16":   func(r []byte) []byte { r[1] = 16; return r },
		"other origin": func(r []byte) []byte { r[24] ^= 1; return r },
		"no time":      func(r []byte) []byte { clear(r[40:48]); return r },
	} {
		if err := checkNTPReply(req, edit(ntpReply(req))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCountReachable(t *testing.T) {
	out := "^,*,192.0.2.10,1,6,377,12,0.000001,0.000002,0.000003\n" +
		"^,?,192.0.2.11,0,6,0,-,0.0,0.0,0.0\n" +
		"^,+,192.0.2.12,1,6,1,40,0.0,0.0,0.0\n" +
		"^,*,192.168.8.1,2,6,377,3,0.0,0.0,0.0\n" +
		"#,*,PPS0,0,4,377,1,0.0,0.0,0.0\n"
	if got := countReachable(out, "192.168.8.1"); got != 2 {
		t.Fatalf("reachable %d", got)
	}
}

func TestTimeLinesRouter(t *testing.T) {
	now := day("2026-10-21").Add(12 * time.Hour)
	router := &timeRouter{Name: "synthetic-router", Addr: "100.64.0.9"}
	state := clockState{Running: true, Synced: true, Ref: now.Add(-time.Minute), Source: "100.64.0.9"}
	got := timeLines(state, timeBoot{}, router, now)
	if len(got) != 2 || !strings.HasPrefix(got[0], "Time: set by the home router synthetic-router at ") || !strings.HasSuffix(got[0], "not confirmed by NTS") {
		t.Fatalf("%q", got)
	}
	state.Source = "192.0.2.10"
	if got := timeLines(state, timeBoot{}, router, now); !strings.HasPrefix(got[0], "Time: set by one NTS source at ") {
		t.Fatalf("%q", got)
	}
	state.Synced = false
	if got := timeLines(state, timeBoot{}, router, now); !strings.HasPrefix(got[0], "Time: not confirmed since ") {
		t.Fatalf("%q", got)
	}
}

// routerCheck is a check that found no NTS this boot (the exception
// already ran) on a SYNTHETIC home network.
func routerCheck(t *testing.T, f *fakeTime) (*timeCheck, *bytes.Buffer) {
	t.Helper()
	ca := newTestCA(t, "SYNTHETIC Root A")
	c, log := newTimeCheck(t, f, behindChains(t, ca), ca.pem())
	c.writeState(timeBoot{State: "failed"})
	c.verdictPath = filepath.Join(t.TempDir(), "vpn-trust.json")
	data, _ := json.Marshal(homeVerdict())
	if err := os.WriteFile(c.verdictPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	c.networkKey = func() string { return "synthetic-net-a" }
	c.ntpProbe = func(addr string) error {
		if addr == "192.168.8.1" {
			return nil
		}
		return errors.New("timeout")
	}
	c.ntsReachable = func(string) int { return 0 }
	return c, log
}

func TestTimeCheckStartsRouter(t *testing.T) {
	f := &fakeTime{wall: day("2026-10-21")}
	c, log := routerCheck(t, f)
	if code := c.run(); code != 0 {
		t.Fatalf("code %d: %s", code, log)
	}
	conf, err := os.ReadFile(c.routerConf())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"authselectmode ignore", "minsources 1", "makestep 1 0", "maxchange 60 0 -1", "maxslewrate 1000", "server 192.168.8.1 "} {
		if !strings.Contains(string(conf), "\n"+line) {
			t.Errorf("router.conf misses %q:\n%s", line, conf)
		}
	}
	active := c.readRouter()
	if active == nil || active.Addr != "192.168.8.1" || active.Name != "synthetic-router" || active.Network != "synthetic-net-a" {
		t.Fatalf("router state %+v", active)
	}
	if !slices.Equal(f.actions, []string{"restart"}) {
		t.Errorf("actions %v", f.actions)
	}
	if !c.readFloor().IsZero() {
		t.Error("router time stamped as confirmed")
	}
}

func TestTimeCheckPrefersTailnetRouter(t *testing.T) {
	f := &fakeTime{wall: day("2026-10-21")}
	c, log := routerCheck(t, f)
	c.ntpProbe = func(string) error { return nil }
	c.run()
	if active := c.readRouter(); active == nil || active.Addr != "100.64.0.9" {
		t.Fatalf("router state %+v: %s", active, log)
	}
}

func TestTimeCheckNoRouterWhenSilent(t *testing.T) {
	f := &fakeTime{wall: day("2026-10-21")}
	c, log := routerCheck(t, f)
	c.ntpProbe = func(string) error { return errors.New("timeout") }
	c.run()
	if c.readRouter() != nil || len(f.actions) != 0 || !strings.Contains(log.String(), "does not answer NTP") {
		t.Fatalf("actions %v: %s", f.actions, log)
	}
}

func TestTimeCheckKeepsRouter(t *testing.T) {
	f := &fakeTime{wall: day("2026-10-21")}
	c, log := routerCheck(t, f)
	c.run()
	f.actions = nil
	c.ntsReachable = func(exclude string) int {
		if exclude != "192.168.8.1" {
			t.Errorf("exclude %q", exclude)
		}
		return 1
	}
	if code := c.run(); code != 0 || len(f.actions) != 0 || c.readRouter() == nil {
		t.Fatalf("code %d actions %v: %s", code, f.actions, log)
	}
}

func TestTimeCheckEndsRouterWhenNTSWorks(t *testing.T) {
	f := &fakeTime{wall: day("2026-10-21")}
	c, log := routerCheck(t, f)
	c.run()
	f.actions = nil
	c.ntsReachable = func(string) int { return 2 }
	f.onChronyd = func(f *fakeTime, action string) {
		if action == "restart" {
			f.wall = f.wall.Add(time.Second)
			f.synced, f.ref = true, f.wall.Add(time.Second)
		}
	}
	if code := c.run(); code != 0 {
		t.Fatalf("code %d: %s", code, log)
	}
	if c.readRouter() != nil {
		t.Fatal("router state left")
	}
	if _, err := os.Stat(c.routerConf()); err == nil {
		t.Fatal("router.conf left")
	}
	if !slices.Equal(f.actions, []string{"restart"}) || c.readFloor().IsZero() {
		t.Fatalf("actions %v floor %s", f.actions, c.readFloor())
	}
}

func TestTimeCheckEndsRouterOnNetworkChange(t *testing.T) {
	f := &fakeTime{wall: day("2026-10-21")}
	c, log := routerCheck(t, f)
	c.run()
	f.actions = nil
	c.networkKey = func() string { return "synthetic-net-b" }
	c.run()
	if c.readRouter() != nil || !slices.Equal(f.actions, []string{"restart"}) {
		t.Fatalf("actions %v: %s", f.actions, log)
	}
	if !strings.Contains(log.String(), "router fallback off: the network verdict is not for the current network") {
		t.Fatalf("log: %s", log)
	}
}

func TestTimeCheckEndsRouterWhenRouterChanges(t *testing.T) {
	f := &fakeTime{wall: day("2026-10-21")}
	c, log := routerCheck(t, f)
	c.run()
	f.actions = nil
	other := homeVerdict()
	other.Router, other.RouterVia, other.RouterTailnet = "synthetic-other via 192.168.8.2", "192.168.8.2", nil
	data, _ := json.Marshal(other)
	if err := os.WriteFile(c.verdictPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	c.ntpProbe = func(string) error { return nil }
	c.run()
	if active := c.readRouter(); active == nil || active.Addr != "192.168.8.2" {
		t.Fatalf("router state %+v: %s", active, log)
	}
	if !slices.Equal(f.actions, []string{"restart", "restart"}) {
		t.Fatalf("actions %v", f.actions)
	}
}

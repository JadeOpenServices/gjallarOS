package nettrust

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNetworkKeyFollowsEveryPart(t *testing.T) {
	base := Network{
		Device:     "wlan0",
		Gateway:    netip.MustParseAddr("192.168.5.1"),
		Subnets:    []netip.Prefix{netip.MustParsePrefix("192.168.5.20/24"), netip.MustParsePrefix("fd00:5::20/64")},
		SSID:       "SYNTHETIC-cafe",
		KeyMgmt:    "wpa-psk",
		GatewayMAC: "02:00:00:00:05:01",
	}
	key := NetworkKey(&base)
	if key == "" {
		t.Fatal("no key")
	}
	same := base
	same.Subnets = []netip.Prefix{base.Subnets[1], netip.MustParsePrefix("192.168.5.99/24")}
	same.GatewayMAC = "02:00:00:00:05:01"
	if NetworkKey(&same) != key {
		t.Fatal("subnet order or host bits changed the key")
	}
	for name, change := range map[string]func(*Network){
		"device":   func(n *Network) { n.Device = "wlan1" },
		"ssid":     func(n *Network) { n.SSID = "SYNTHETIC-other" },
		"security": func(n *Network) { n.KeyMgmt = "" },
		"gateway":  func(n *Network) { n.Gateway = netip.MustParseAddr("192.168.5.2") },
		"mac":      func(n *Network) { n.GatewayMAC = "02:00:00:00:05:02" },
		"subnet":   func(n *Network) { n.Subnets = n.Subnets[:1] },
	} {
		other := base
		change(&other)
		if NetworkKey(&other) == key {
			t.Errorf("%s: key did not change", name)
		}
	}
	noMAC := base
	noMAC.GatewayMAC = ""
	if NetworkKey(&noMAC) != "" || NetworkKey(nil) != "" {
		t.Fatal("a network without gateway hardware address got a key")
	}
}

func TestPauseHoldsOnlyOnItsNetworkAndInTime(t *testing.T) {
	start := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	p := Pause{Network: "k1", Started: start, Until: start.Add(MaxPause)}
	cases := []struct {
		name string
		key  string
		at   time.Time
		want bool
	}{
		{"inside", "k1", start.Add(30 * time.Minute), true},
		{"other network", "k2", start.Add(time.Minute), false},
		{"no network", "", start.Add(time.Minute), false},
		{"over", "k1", start.Add(MaxPause), false},
		{"clock before start", "k1", start.Add(-time.Minute), false},
	}
	for _, c := range cases {
		if got := p.Holds(c.key, c.at); got != c.want {
			t.Errorf("%s: Holds = %v", c.name, got)
		}
	}
	long := Pause{Network: "k1", Started: start, Until: start.Add(MaxPause + time.Second)}
	if long.Holds("k1", start.Add(time.Minute)) {
		t.Fatal("a pause longer than MaxPause held")
	}
}

func TestLoadPauseRefusesWritableFiles(t *testing.T) {
	old := pauseOwner
	pauseOwner = uint32(os.Getuid())
	t.Cleanup(func() { pauseOwner = old })

	path := filepath.Join(t.TempDir(), "vpn-pause.json")
	if _, ok, err := LoadPause(path); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	want := Pause{Network: "k1", User: "tester", UID: 1000, Started: time.Unix(100, 0).UTC(), Until: time.Unix(200, 0).UTC()}
	if err := WritePause(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadPause(path)
	if err != nil || !ok || got != want {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadPause(path); ok || err == nil {
		t.Fatal("a world-writable pause file counted")
	}
	if err := RemovePause(path); err != nil {
		t.Fatal(err)
	}
	if err := RemovePause(path); err != nil {
		t.Fatal("removing a missing pause failed:", err)
	}
}

func TestParseNeighMACSkipsFailedEntries(t *testing.T) {
	if mac := parseNeighMAC([]byte(`[{"dst":"192.168.5.1","dev":"wlan0","state":["FAILED"]}]`)); mac != "" {
		t.Fatalf("failed entry gave %q", mac)
	}
	if mac := parseNeighMAC([]byte(`[{"dst":"192.168.5.1","dev":"wlan0","lladdr":"02:00:00:00:05:01","state":["STALE"]}]`)); mac != "02:00:00:00:05:01" {
		t.Fatalf("got %q", mac)
	}
}

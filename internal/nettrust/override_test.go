package nettrust

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func exitOption(name, route string, online bool) Peer {
	p := router(name, route)
	p.Online, p.ExitNodeOption = online, true
	return p
}

func TestAutoExitNodePrefersTheHomeRouter(t *testing.T) {
	p := policy
	p.ExitNode = ExitNodeAuto
	tn := Tailnet{Running: true, Peers: []Peer{
		exitOption("a-cloud", "10.9.0.0/24", true),
		exitOption("b-offline-home", "192.168.8.0/24", false),
		exitOption("c-home", "192.168.8.0/24", true),
	}}
	v := decide(t, p, wifi("cafe", "wpa-psk", "172.16.5.0/24"), tn, fakeProber{})
	if v.Trust != TrustUntrusted || v.ExitNode != "c-home" || v.ExitNodeError != "" {
		t.Fatalf("verdict = %+v", v)
	}

	tn.Peers = tn.Peers[:2] // the home router is gone: any online exit node
	if v := decide(t, p, wifi("cafe", "wpa-psk", "172.16.5.0/24"), tn, fakeProber{}); v.ExitNode != "a-cloud" {
		t.Fatalf("verdict = %+v", v)
	}

	tn.Peers = tn.Peers[1:]
	v = decide(t, p, wifi("cafe", "wpa-psk", "172.16.5.0/24"), tn, fakeProber{})
	if !v.ManageExitNode || v.ExitNode != "b-offline-home" || v.Blocked != BlockExitNodeOffline {
		t.Fatalf("no online exit node: want the offline home router set, got %+v", v)
	}
}

func TestAutoExitNodeKeepsTheCurrentNodeWhenNoneIsOnline(t *testing.T) {
	p := policy
	p.ExitNode = ExitNodeAuto
	current := exitOption("z-cloud", "10.9.0.0/24", false)
	current.ExitNode = true
	tn := Tailnet{Running: true, Peers: []Peer{exitOption("a-home", "192.168.8.0/24", false), current}}
	v := decide(t, p, wifi("cafe", "wpa-psk", "172.16.5.0/24"), tn, fakeProber{})
	if v.ExitNode != "z-cloud" || v.Blocked != BlockExitNodeOffline || len(v.Warnings) != 1 {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestAutoWithoutAnyExitNodeIsBlocked(t *testing.T) {
	p := policy
	p.ExitNode = ExitNodeAuto
	tn := Tailnet{Running: true, Peers: []Peer{router("home-router", "192.168.8.0/24")}}
	v := decide(t, p, wifi("cafe", "", "172.16.5.0/24"), tn, fakeProber{})
	if v.ExitNode != "" || v.ExitNodeError == "" || v.Blocked != BlockNoExitNode {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestExitNodeOffStaysOffOnUntrustedNetworks(t *testing.T) {
	p := policy
	p.ExitNode = ExitNodeOff
	v := decide(t, p, wifi("cafe", "wpa-psk", "172.16.5.0/24"), Tailnet{Running: true}, fakeProber{})
	if v.Trust != TrustUntrusted || !v.ManageExitNode || v.ExitNode != "" || v.Blocked != "" {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestOverrideTrustsAndUntrustsWifis(t *testing.T) {
	base := Policy{TrustedWifis: []string{"home", "sister"}, ExitNode: "home-router"}
	var o Override
	o.Trust(base, "Shi 2,4")
	o.Untrust(base, "sister")
	o.Trust(base, "home") // already trusted by the system: no entry
	got := o.Apply(base)
	if !slices.Equal(got.TrustedWifis, []string{"home", "Shi 2,4"}) {
		t.Fatalf("trusted = %q", got.TrustedWifis)
	}
	if !slices.Equal(o.TrustWifis, []string{"Shi 2,4"}) || !slices.Equal(o.UntrustWifis, []string{"sister"}) {
		t.Fatalf("override = %+v", o)
	}

	o.Trust(base, "sister")
	o.Untrust(base, "Shi 2,4")
	if !o.Empty() {
		t.Fatalf("override not back to empty: %+v", o)
	}
}

func TestOverrideExitNode(t *testing.T) {
	base := Policy{ExitNode: "home-router"}
	var o Override
	if err := o.SetExitNode(base, "two words"); err == nil {
		t.Fatal("accepted a name with a space")
	}
	if err := o.SetExitNode(base, ExitNodeOff); err != nil || o.Apply(base).ExitNode != ExitNodeOff {
		t.Fatalf("off: %v %+v", err, o)
	}
	if err := o.SetExitNode(base, "default"); err != nil || o.ExitNode != nil {
		t.Fatalf("default: %v %+v", err, o)
	}
	if err := o.SetExitNode(base, "home-router"); err != nil || o.ExitNode != nil {
		t.Fatalf("same as base: %v %+v", err, o)
	}
}

func TestOverrideFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gjallar", "override.json")
	if o, err := LoadOverride(path); err != nil || !o.Empty() {
		t.Fatalf("missing file: %+v %v", o, err)
	}
	node := ExitNodeAuto
	want := Override{TrustWifis: []string{"Shi 2,4"}, ExitNode: &node}
	if err := SaveOverride(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOverride(path)
	if err != nil || !slices.Equal(got.TrustWifis, want.TrustWifis) || *got.ExitNode != node {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := SaveOverride(path, Override{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty override left a file: %v", err)
	}
}

func TestNixStateQuotesNames(t *testing.T) {
	got := NixState(Policy{
		HomeSubnets:   []string{"192.168.8.0/24"},
		TrustedWifis:  []string{`Shi 2,4`, `a"b`, "${x}"},
		ExitNode:      ExitNodeAuto,
		WifiExitNodes: map[string]string{"Shi 2,4": "OpenWrt", `a"b`: ExitNodeAuto},
	})
	want := `tailscale = { enable = true; homeSubnets = [ "192.168.8.0/24" ]; trustedWifis = [ "Shi 2,4" "a\"b" "\${x}" ]; exitNode = "auto"; wifiExitNodes = { "Shi 2,4" = "OpenWrt"; "a\"b" = "auto"; }; routerHome = false; siteRouterTrust = false; siteRouterTargets = [ ]; };`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestParseTailnetExitNodeOption(t *testing.T) {
	tn, err := parseTailnet([]byte(`{"BackendState":"Running","Peer":{"k":{"HostName":"OpenWrt",
		"TailscaleIPs":["100.64.0.1"],"Online":true,"ExitNodeOption":true}}}`))
	if err != nil || len(tn.Peers) != 1 || !tn.Peers[0].ExitNodeOption {
		t.Fatalf("tailnet = %+v %v", tn, err)
	}
	if autoExitNode(tn, []netip.Prefix{netip.MustParsePrefix("192.168.8.0/24")}, true) == nil {
		t.Fatal("no auto exit node")
	}
}

func TestTrustedWifiWithExitNodeKeepsLANLocal(t *testing.T) {
	p := policy
	p.WifiExitNodes = map[string]string{"fizzlipuzzli": ExitNodeAuto}
	tn := Tailnet{Running: true, Peers: []Peer{exitOption("home-router", "192.168.8.0/24", true)}}
	v := decide(t, p, wifi("fizzlipuzzli", "wpa-psk", "10.1.0.0/24"), tn, fakeProber{})
	if v.Trust != TrustTrustedWifi || !v.ManageExitNode || v.ExitNode != "home-router" ||
		!slices.Equal(v.Bypass, []string{"10.1.0.0/24"}) {
		t.Fatalf("verdict = %+v", v)
	}

	// Other trusted Wi-Fis keep the exit node off.
	if v := decide(t, p, wifi("bakasifu-5Ghz", "wpa-psk", "10.2.0.0/24"), tn, fakeProber{}); v.ExitNode != "" || !v.ManageExitNode {
		t.Fatalf("verdict = %+v", v)
	}

	// A per-Wi-Fi exit node alone makes the exit node managed: off elsewhere.
	p = Policy{TrustedWifis: []string{"mum"}, WifiExitNodes: map[string]string{"mum": "home-router"}}
	if v := decide(t, p, wifi("cafe", "wpa-psk", "172.16.5.0/24"), tn, fakeProber{}); v.Trust != TrustUntrusted || !v.ManageExitNode || v.ExitNode != "" {
		t.Fatalf("verdict = %+v", v)
	}
	if v := decide(t, p, wifi("mum", "wpa-psk", "172.16.6.0/24"), tn, fakeProber{}); v.ExitNode != "home-router" {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestOverrideWifiExitNodes(t *testing.T) {
	base := Policy{TrustedWifis: []string{"home", "mum"}, WifiExitNodes: map[string]string{"mum": "OpenWrt"}}
	var o Override
	if err := o.SetWifiExitNode(base, "home", "two words"); err == nil {
		t.Fatal("accepted a name with a space")
	}
	if err := o.SetWifiExitNode(base, "home", ExitNodeAuto); err != nil {
		t.Fatal(err)
	}
	if err := o.SetWifiExitNode(base, "mum", "default"); err != nil {
		t.Fatal(err)
	}
	got := o.Apply(base).WifiExitNodes
	if len(got) != 1 || got["home"] != ExitNodeAuto {
		t.Fatalf("wifiExitNodes = %v", got)
	}

	// Only trusted Wi-Fis keep one.
	o.Untrust(base, "home")
	if got := o.Apply(base).WifiExitNodes; len(got) != 0 {
		t.Fatalf("wifiExitNodes = %v", got)
	}
	o.Trust(base, "home")
	if err := o.SetWifiExitNode(base, "mum", "OpenWrt"); err != nil || !o.Empty() {
		t.Fatalf("override not back to empty: %v %+v", err, o)
	}
}

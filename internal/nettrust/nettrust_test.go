package nettrust

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeProber struct {
	via     map[string]string // host name -> answering LAN address
	reach   map[string]bool
	gateway map[string]bool // addresses the local gateway answers for
}

func (f fakeProber) GatewayOwns(_ context.Context, _ *Network, addr netip.Addr) bool {
	return f.gateway[addr.String()]
}

func (f fakeProber) DirectVia(_ context.Context, p Peer) (netip.Addr, bool) {
	raw, ok := f.via[p.HostName]
	if !ok {
		return netip.Addr{}, false
	}
	return netip.MustParseAddr(raw), true
}

func (f fakeProber) Reach(_ context.Context, target string) error {
	if f.reach[target] {
		return nil
	}
	return errors.New("refused")
}

var policy = Policy{
	HomeSubnets:       []string{"192.168.8.0/24"},
	TrustedWifis:      []string{"synthetic-5Ghz", "synthetic-2.4Ghz", "synthetic-guest"},
	ExitNode:          "home-router",
	SiteRouterTrust:   true,
	SiteRouterTargets: []string{"192.168.8.1:53"},
}

func wifi(ssid, keyMgmt, subnet string) *Network {
	return &Network{Device: "wlan0", SSID: ssid, KeyMgmt: keyMgmt, Subnets: []netip.Prefix{netip.MustParsePrefix(subnet)}}
}

func router(name, route string) Peer {
	return Peer{HostName: name, Online: true, IPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")}, Routes: []netip.Prefix{netip.MustParsePrefix(route)}}
}

func decide(t *testing.T, p Policy, n *Network, tn Tailnet, pr Prober) Verdict {
	t.Helper()
	v, err := Decide(context.Background(), p, n, tn, pr)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTrustedHomeWifiKeepsHomeLocal(t *testing.T) {
	v := decide(t, policy, wifi("synthetic-5Ghz", "wpa-psk", "192.168.8.0/24"), Tailnet{Running: true}, fakeProber{})
	if v.Trust != TrustHome || v.ExitNode != "" || !slices.Equal(v.Bypass, []string{"192.168.8.0/24"}) {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestClonedOpenHomeWifiIsUntrusted(t *testing.T) {
	v := decide(t, policy, wifi("synthetic-5Ghz", "", "192.168.8.0/24"), Tailnet{Running: true}, fakeProber{})
	if v.Trust != TrustUntrusted || v.ExitNode != "home-router" || len(v.Bypass) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
	if len(v.Warnings) != 2 {
		t.Fatalf("want open-wifi and subnet-collision warnings, got %q", v.Warnings)
	}
}

func TestHomeRouterProofWorksOnEthernet(t *testing.T) {
	n := &Network{Device: "enp1s0", Subnets: []netip.Prefix{netip.MustParsePrefix("192.168.8.0/24")}}
	tn := Tailnet{Running: true, Peers: []Peer{router("home-router", "192.168.8.0/24")}}
	v := decide(t, policy, n, tn, fakeProber{via: map[string]string{"home-router": "192.168.8.1"}})
	if v.Trust != TrustHome || v.Router != "home-router via 192.168.8.1" {
		t.Fatalf("verdict = %+v", v)
	}
	if v.RouterVia != "192.168.8.1" || !slices.Equal(v.RouterTailnet, []string{"100.64.0.9"}) {
		t.Fatalf("router addresses = %q %q", v.RouterVia, v.RouterTailnet)
	}
}

func TestRelayedRouterIsNoProof(t *testing.T) {
	// A foreign LAN reusing 192.168.8.0/24 while the real router answers
	// only from elsewhere.
	n := &Network{Device: "enp1s0", Subnets: []netip.Prefix{netip.MustParsePrefix("192.168.8.0/24")}}
	tn := Tailnet{Running: true, Peers: []Peer{router("home-router", "192.168.8.0/24")}}
	v := decide(t, policy, n, tn, fakeProber{via: map[string]string{"home-router": "203.0.113.7"}})
	if v.Trust != TrustUntrusted || len(v.Bypass) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestSiteRouterNeedsEveryTarget(t *testing.T) {
	n := &Network{Device: "enp1s0", Subnets: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/24")}}
	tn := Tailnet{Running: true, Peers: []Peer{router("mum-router", "10.20.0.0/24")}}
	pr := fakeProber{via: map[string]string{"mum-router": "10.20.0.1"}}

	v := decide(t, policy, n, tn, pr)
	if v.Trust != TrustUntrusted || v.ExitNode != "home-router" {
		t.Fatalf("router without home access must not be trusted: %+v", v)
	}

	pr.reach = map[string]bool{"192.168.8.1:53": true}
	v = decide(t, policy, n, tn, pr)
	if v.Trust != TrustSite || v.ExitNode != "" || !slices.Equal(v.Bypass, []string{"10.20.0.0/24"}) {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestExitNodeRouteIsNoSiteProof(t *testing.T) {
	n := &Network{Device: "enp1s0", Subnets: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/24")}}
	tn := Tailnet{Running: true, Peers: []Peer{router("exit", "0.0.0.0/0")}}
	pr := fakeProber{via: map[string]string{"exit": "10.20.0.5"}, reach: map[string]bool{"192.168.8.1:53": true}}
	if v := decide(t, policy, n, tn, pr); v.Trust != TrustUntrusted {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestSiteRouterTrustOffFallsBackToWifi(t *testing.T) {
	p := policy
	p.SiteRouterTrust = false
	tn := Tailnet{Running: true, Peers: []Peer{router("sis-router", "192.168.1.0/24")}}
	pr := fakeProber{via: map[string]string{"sis-router": "192.168.1.1"}, reach: map[string]bool{"192.168.8.1:53": true}}
	v := decide(t, p, wifi("synthetic-guest", "sae", "192.168.1.0/24"), tn, pr)
	if v.Trust != TrustTrustedWifi || v.ExitNode != "" {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestRouterHomeTrustsAnyProvenTailnetRouter(t *testing.T) {
	p := Policy{ExitNode: "exit-1", RouterHome: true}
	n := wifi("cafe-net", "wpa-psk", "10.30.0.0/24")
	tn := Tailnet{Running: true, Peers: []Peer{router("friend-router", "10.30.0.0/24")}}

	v := decide(t, p, n, tn, fakeProber{via: map[string]string{"friend-router": "10.30.0.1"}})
	if v.Trust != TrustHome || v.ExitNode != "" || !v.ManageExitNode || !slices.Equal(v.Bypass, []string{"10.30.0.0/24"}) {
		t.Fatalf("verdict = %+v", v)
	}

	// The same subnet elsewhere: the router answers from its real place.
	v = decide(t, p, n, tn, fakeProber{via: map[string]string{"friend-router": "203.0.113.7"}})
	if v.Trust != TrustUntrusted || v.ExitNode != "exit-1" || len(v.Bypass) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestRouterHomeIgnoresExitNodeRoute(t *testing.T) {
	p := Policy{ExitNode: "exit-1", RouterHome: true}
	tn := Tailnet{Running: true, Peers: []Peer{router("exit-1", "0.0.0.0/0")}}
	v := decide(t, p, wifi("cafe-net", "wpa-psk", "10.30.0.0/24"), tn, fakeProber{via: map[string]string{"exit-1": "10.30.0.5"}})
	if v.Trust != TrustUntrusted || v.ExitNode != "exit-1" {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestOfflineExitNodeStaysSet(t *testing.T) {
	exit := router("exit-1", "0.0.0.0/0")
	exit.Online = false
	v := decide(t, Policy{ExitNode: "exit-1", RouterHome: true}, wifi("cafe-net", "", "10.30.0.0/24"), Tailnet{Running: true, Peers: []Peer{exit}}, fakeProber{})
	if v.ExitNode != "exit-1" || len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "offline") ||
		v.Blocked != BlockExitNodeOffline {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestUnmanagedPolicyKeepsLegacyBypass(t *testing.T) {
	v := decide(t, Policy{HomeSubnets: []string{"192.168.8.0/24"}}, nil, Tailnet{}, fakeProber{})
	if v.Trust != TrustLegacy || !slices.Equal(v.Bypass, []string{"192.168.8.0/24"}) || v.ManageExitNode {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestStoppedTailscaleLeavesExitNodeAloneButBlocks(t *testing.T) {
	v := decide(t, policy, wifi("cafe", "", "172.16.0.0/16"), Tailnet{}, fakeProber{})
	if v.Trust != TrustUntrusted || v.ManageExitNode || v.Blocked != BlockTailscaleDown {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestTrustedHomeIsNeverBlocked(t *testing.T) {
	v := decide(t, policy, wifi("synthetic-5Ghz", "wpa-psk", "192.168.8.0/24"), Tailnet{}, fakeProber{})
	if v.Trust != TrustHome || v.Blocked != "" {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestParseDefaultRoutePrefersLowestMetric(t *testing.T) {
	dev, gw, ok, err := parseDefaultRoute([]byte(`[
		{"dst":"default","gateway":"10.0.0.1","dev":"wlan0","metric":600},
		{"dst":"default","gateway":"192.168.8.1","dev":"enp1s0","metric":100}]`))
	if err != nil || !ok || dev != "enp1s0" || gw.String() != "192.168.8.1" {
		t.Fatalf("got %s %s %v %v", dev, gw, ok, err)
	}
}

func TestParseSubnetsMasksGlobalAddresses(t *testing.T) {
	got, err := parseSubnets([]byte(`[{"addr_info":[
		{"family":"inet","local":"192.168.8.23","prefixlen":24,"scope":"global"},
		{"family":"inet6","local":"fe80::1","prefixlen":64,"scope":"link"}]}]`))
	if err != nil || len(got) != 1 || got[0].String() != "192.168.8.0/24" {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestParsePongVia(t *testing.T) {
	out := []byte("pong from r (100.64.0.9) via DERP(fra) in 30ms\npong from r (100.64.0.9) via 192.168.8.1:41641 in 2ms\n")
	if via, ok := parsePongVia(out); !ok || via.String() != "192.168.8.1" {
		t.Fatalf("got %s %v", via, ok)
	}
	if _, ok := parsePongVia([]byte("pong from r (100.64.0.9) via DERP(fra) in 30ms\n")); ok {
		t.Fatal("DERP path counted as direct")
	}
}

func TestParseTailnet(t *testing.T) {
	tn, err := parseTailnet([]byte(`{"BackendState":"Running","Peer":{"k":{
		"HostName":"home-router","DNSName":"home-router.tail1234.ts.net.","TailscaleIPs":["100.64.0.9"],
		"Online":true,"PrimaryRoutes":["192.168.8.0/24"],"ExitNode":true}}}`))
	if err != nil || !tn.Running || len(tn.Peers) != 1 {
		t.Fatalf("got %+v %v", tn, err)
	}
	cur := tn.CurrentExitNode()
	if cur == nil || !cur.Matches("home-router") || !cur.Matches("100.64.0.9") || !cur.Matches("home-router.tail1234.ts.net") {
		t.Fatalf("exit node match failed: %+v", cur)
	}
}

func TestApplyExitNodeOnlyChangesOnDifference(t *testing.T) {
	var calls [][]string
	s := System{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}}
	tn, _ := parseTailnet([]byte(`{"BackendState":"Running","Peer":{"k":{"HostName":"home-router","ExitNode":true}}}`))
	if changed, _ := s.ApplyExitNode(context.Background(), tn, "home-router"); changed || len(calls) != 0 {
		t.Fatalf("unchanged exit node was set again: %v", calls)
	}
	if changed, _ := s.ApplyExitNode(context.Background(), tn, ""); !changed || !slices.Equal(calls[0], []string{"tailscale", "set", "--exit-node="}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRouterProofViaGatewayWANAddress(t *testing.T) {
	n := wifi("synthetic-5ghz", "wpa-psk", "192.168.8.0/24")
	n.Gateway = netip.MustParseAddr("192.168.8.1")
	tn := Tailnet{Running: true, Peers: []Peer{router("OpenWrt", "192.168.8.0/24")}}
	pr := fakeProber{via: map[string]string{"OpenWrt": "192.168.9.1"}, gateway: map[string]bool{"192.168.9.1": true}}
	v := decide(t, Policy{HomeSubnets: []string{"192.168.8.0/24"}, ExitNode: "OpenWrt"}, n, tn, pr)
	if v.Trust != TrustHome || v.Router != "OpenWrt via 192.168.9.1" || !v.ManageExitNode || v.ExitNode != "" {
		t.Fatalf("verdict = %+v", v)
	}
	pr.gateway = nil // a direct path to some other host behind the gateway
	if v := decide(t, Policy{HomeSubnets: []string{"192.168.8.0/24"}, ExitNode: "OpenWrt"}, n, tn, pr); v.Trust != TrustUntrusted {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestOfflineLeavesExitNodeAlone(t *testing.T) {
	v := decide(t, policy, nil, Tailnet{Running: true}, fakeProber{})
	if v.Trust != TrustOffline || v.ManageExitNode {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestApplyExitNodeUsesTailscaleAddress(t *testing.T) {
	var calls [][]string
	s := System{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}}
	tn, _ := parseTailnet([]byte(`{"BackendState":"Running","Peer":{"k":{"HostName":"OpenWrt","DNSName":"openwrt.tail1.ts.net.","TailscaleIPs":["100.64.0.1","fd7a::1"]}}}`))
	if changed, err := s.ApplyExitNode(context.Background(), tn, "OpenWrt"); !changed || err != nil ||
		!slices.Equal(calls[0], []string{"tailscale", "set", "--exit-node=100.64.0.1", "--exit-node-allow-lan-access=true"}) {
		t.Fatalf("changed=%v err=%v calls=%v", changed, err, calls)
	}
	if _, err := s.ApplyExitNode(context.Background(), tn, "gone"); err == nil || len(calls) != 1 {
		t.Fatalf("unknown exit node: err=%v calls=%v", err, calls)
	}
}

func TestTailnetWaitsForStartingTailscaled(t *testing.T) {
	old := tailnetSettle
	tailnetSettle = 5 * time.Second
	defer func() { tailnetSettle = old }()
	answers := []string{"", `{"BackendState":"Starting"}`, `{"BackendState":"Running"}`}
	s := System{run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return []byte("activating\n"), errors.New("synthetic exit status 3")
		}
		a := answers[0]
		answers = answers[1:]
		if a == "" {
			return nil, errors.New("no tailscaled socket")
		}
		return []byte(a), nil
	}}
	tn, err := s.Tailnet(context.Background())
	if err != nil || !tn.Running {
		t.Fatalf("tailnet = %+v, %v", tn, err)
	}
	stopped := System{run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"BackendState":"NeedsLogin"}`), nil
	}}
	if tn, _ := stopped.Tailnet(context.Background()); tn.Running {
		t.Fatal("NeedsLogin reported running")
	}
	// A failed unit has no start coming: no wait at all.
	var calls []string
	failed := System{run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		calls = append(calls, name)
		if name == "systemctl" {
			return []byte("failed\n"), errors.New("synthetic exit status 3")
		}
		return nil, errors.New("no tailscaled socket")
	}}
	begin := time.Now()
	if tn, err := failed.Tailnet(context.Background()); err != nil || tn.Running || time.Since(begin) > time.Second {
		t.Fatalf("failed unit: tailnet %+v, err %v after %v, calls %v", tn, err, time.Since(begin), calls)
	}
	if !slices.Equal(calls, []string{"tailscale", "systemctl"}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestGatewayOwnsProbesOutsideTheTunnel(t *testing.T) {
	var calls []string
	s := System{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "ip" {
			return []byte(`[{"dst":"192.168.9.1","gateway":"192.168.8.1","dev":"wlan0"}]`), nil
		}
		return nil, nil
	}}
	n := &Network{Device: "wlan0", Gateway: netip.MustParseAddr("192.168.8.1")}
	if !s.GatewayOwns(context.Background(), n, netip.MustParseAddr("192.168.9.1")) {
		t.Fatalf("calls = %q", calls)
	}
	want := []string{
		"ip -j route get 192.168.9.1 mark 524288",
		"ping -n -q -c 2 -W 1 -t 1 -m 524288 -I wlan0 192.168.9.1",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %q", calls)
	}
	n.Device = "eth0" // routed out of another device: not this gateway
	if s.GatewayOwns(context.Background(), n, netip.MustParseAddr("192.168.9.1")) {
		t.Fatal("route via another device accepted")
	}
}

func TestWriteStatusSkipsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	v := Verdict{Trust: TrustHome, Bypass: []string{}}
	if err := WriteStatus(path, v); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := WriteStatus(path, v); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("unchanged verdict rewritten: %v %v", info.ModTime(), err)
	}
	v.Trust = TrustUntrusted
	if err := WriteStatus(path, v); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.ModTime().Equal(old) {
		t.Fatalf("changed verdict not written: %v", err)
	}
}

func TestMayBlockOnlyWhenAnExitNodeIsWanted(t *testing.T) {
	for _, tc := range []struct {
		p    Policy
		want bool
	}{
		{Policy{}, false},
		{Policy{ExitNode: ExitNodeOff, TrustedWifis: []string{"home"}, RouterHome: true}, false},
		{Policy{ExitNode: ExitNodeAuto}, true},
		{Policy{ExitNode: "exit"}, true},
		{Policy{WifiExitNodes: map[string]string{"home": ""}}, false},
		{Policy{ExitNode: ExitNodeOff, WifiExitNodes: map[string]string{"home": ExitNodeOff, "work": "exit"}}, true},
	} {
		if got := tc.p.MayBlock(); got != tc.want {
			t.Errorf("%+v: MayBlock = %v", tc.p, got)
		}
	}
}

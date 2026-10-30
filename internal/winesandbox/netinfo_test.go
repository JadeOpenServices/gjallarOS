package winesandbox

import (
	"net/netip"
	"slices"
	"testing"
)

// Shaped like ip -j output on a laptop with Wi-Fi, Tailscale and IPv6 from
// a router announcement. Addresses are documentation ranges (example).
const addrSample = `[
 {"ifname":"lo","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8},{"family":"inet6","local":"::1","prefixlen":128}]},
 {"ifname":"wlan0","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24},{"family":"inet6","local":"2001:db8:1::10","prefixlen":64},{"family":"inet6","local":"fe80::10","prefixlen":64}]},
 {"ifname":"tailscale0","addr_info":[{"family":"inet","local":"100.70.1.2","prefixlen":32},{"family":"inet6","local":"fd7a:115c:a1e0::2","prefixlen":128}]},
 {"ifname":"tun0","addr_info":[{"family":"inet","local":"198.51.100.2","address":"198.51.100.1","prefixlen":32}]}
]`

const route4Sample = `[
 {"dst":"default","gateway":"192.0.2.1","dev":"wlan0","protocol":"dhcp"},
 {"dst":"192.0.2.0/24","dev":"wlan0","protocol":"kernel"},
 {"dst":"0.0.0.0/1","dev":"tun0","protocol":"static"},
 {"dst":"100.100.100.100","dev":"tailscale0","table":"52"}
]`

const route6Sample = `[
 {"dst":"2001:db8:1::/64","dev":"wlan0","protocol":"ra"},
 {"dst":"2001:db8:2::/64","dev":"wlan0","protocol":"ra"},
 {"dst":"default","via":{"family":"inet6","host":"fe80::1"},"dev":"wlan0","protocol":"ra"},
 {"dst":"fe80::/64","dev":"wlan0","protocol":"kernel"}
]`

const neighSample = `[
 {"dst":"2001:db8:1::1","dev":"wlan0","lladdr":"00:00:5e:00:53:01","router":null,"state":["STALE"]},
 {"dst":"2001:db8:1::20","dev":"wlan0","lladdr":"00:00:5e:00:53:20","state":["REACHABLE"]}
]`

func TestParseNet(t *testing.T) {
	n, err := ParseNet([]byte(addrSample), [][]byte{[]byte(route4Sample), []byte(route6Sample)}, []byte(neighSample))
	if err != nil {
		t.Fatal(err)
	}
	addrs := func(list ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range list {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	prefixes := func(list ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, s := range list {
			out = append(out, netip.MustParsePrefix(s))
		}
		return out
	}
	wantSelf := addrs("127.0.0.1", "::1", "192.0.2.10", "2001:db8:1::10", "fe80::10", "100.70.1.2", "fd7a:115c:a1e0::2", "198.51.100.2")
	if !slices.Equal(n.Self, wantSelf) {
		t.Errorf("Self = %v, want %v", n.Self, wantSelf)
	}
	wantRouters := addrs("192.0.2.1", "fe80::1", "2001:db8:1::1")
	if !slices.Equal(n.Routers, wantRouters) {
		t.Errorf("Routers = %v, want %v", n.Routers, wantRouters)
	}
	// The VPN's 0.0.0.0/1, the tun peer and host routes are no LAN.
	wantLAN := prefixes("192.0.2.0/24", "2001:db8:1::/64", "2001:db8:1::/64", "2001:db8:2::/64")
	if !slices.Equal(n.LAN, wantLAN) {
		t.Errorf("LAN = %v, want %v", n.LAN, wantLAN)
	}
}

func TestParseNetRefusesBrokenJSON(t *testing.T) {
	if _, err := ParseNet([]byte("{"), nil, []byte("[]")); err == nil {
		t.Error("broken address JSON accepted")
	}
	if _, err := ParseNet([]byte("[]"), [][]byte{[]byte("x")}, []byte("[]")); err == nil {
		t.Error("broken route JSON accepted")
	}
	if _, err := ParseNet([]byte("[]"), nil, []byte("nope")); err == nil {
		t.Error("broken neighbour JSON accepted")
	}
}

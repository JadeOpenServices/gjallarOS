// Package winesandbox runs one Wine program in its own sandbox: a network
// namespace that pasta connects to the internet, an nftables table that
// decides where the program may connect, a minimal root under bubblewrap,
// Landlock for symlinks, and a Wayland socket with a security context.
// Design and test results: wiki page 746, Plane 272.
package winesandbox

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Flags are the program's two network switches.
type Flags struct {
	// Internet lets the program reach the internet and look up names.
	Internet bool `json:"internet"`
	// Home lets the program reach the tailnet, and the home network's
	// devices while this device is at home.
	Home bool `json:"home"`
}

// DNSAddr is where pasta answers name lookups inside the namespace.
const DNSAddr = "169.254.53.53"

// Table is the nftables table the rules live in, inside the program's
// own network namespace.
const Table = "gjsb"

// Net is what the host's network looks like right now.
type Net struct {
	// Self are this device's own addresses on every interface. pasta
	// connects to them through the host, so they are closed.
	Self []netip.Addr
	// Routers are gateways and IPv6 routers. They stay closed even with
	// Home on: a program has no reason to reach the router itself.
	Routers []netip.Addr
	// LAN are the subnets reached without a gateway.
	LAN []netip.Prefix
}

// tailnet is opened by the Home switch on any network.
var tailnet = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// tailnetDNS is Tailscale's resolver. Names come through pasta only.
var tailnetDNS = []netip.Addr{
	netip.MustParseAddr("100.100.100.100"),
	netip.MustParseAddr("fd7a:115c:a1e0::53"),
}

var special4 = "0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16, 224.0.0.0/3"
var special6 = "::/128, ::1/128, fe80::/10, ff00::/8"
var private4 = "10.0.0.0/8, 100.64.0.0/10, 172.16.0.0/12, 192.168.0.0/16, 192.0.0.0/24, 198.18.0.0/15"
var private6 = "fc00::/7, fec0::/10"

// HomeLAN picks the subnets the Home switch opens while this device is at
// home: the trust verdict's bypass prefixes that lie inside a subnet this
// device is connected to. Anything else in the list is ignored.
func HomeLAN(bypass []string, lan []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, text := range bypass {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(text))
		if err != nil {
			continue
		}
		prefix = prefix.Masked()
		for _, connected := range lan {
			if connected.Addr().Is4() == prefix.Addr().Is4() && connected.Bits() <= prefix.Bits() && connected.Contains(prefix.Addr()) {
				out = append(out, prefix)
				break
			}
		}
	}
	return out
}

// Ruleset is the whole table for one program. Sent to nft -f in one go it
// replaces the old table atomically, so a change never leaves a gap.
// homeLAN is HomeLAN's answer, or nil while this device is not at home.
//
// The order matters: own addresses, routers and special ranges are closed
// first, then the Home switch opens the tailnet and home subnets, then
// every other local or private range is closed, then the Internet switch
// decides the rest.
func Ruleset(n Net, f Flags, homeLAN []netip.Prefix) string {
	var self4, self6, router4, router6, lan4, lan6, home4, home6 []string
	split := func(a netip.Addr, v4, v6 *[]string) {
		a = a.Unmap()
		if a.Is4() {
			*v4 = append(*v4, a.String())
		} else {
			*v6 = append(*v6, a.WithZone("").String())
		}
	}
	splitPrefix := func(p netip.Prefix, v4, v6 *[]string) {
		p = p.Masked()
		if p.Addr().Is4() {
			*v4 = append(*v4, p.String())
		} else {
			*v6 = append(*v6, p.String())
		}
	}
	for _, a := range n.Self {
		split(a, &self4, &self6)
	}
	for _, a := range append(slices.Clone(n.Routers), tailnetDNS...) {
		split(a, &router4, &router6)
	}
	for _, p := range n.LAN {
		splitPrefix(p, &lan4, &lan6)
	}
	if f.Home {
		for _, p := range append(slices.Clone(tailnet), homeLAN...) {
			splitPrefix(p, &home4, &home6)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s\ndelete table inet %s\ntable inet %s {\n", Table, Table, Table)
	set := func(name, typ string, elements []string) {
		fmt.Fprintf(&b, "\tset %s { type %s; flags interval; auto-merge;", name, typ)
		if len(elements) > 0 {
			slices.Sort(elements)
			elements = slices.Compact(elements)
			fmt.Fprintf(&b, " elements = { %s };", strings.Join(elements, ", "))
		}
		b.WriteString(" }\n")
	}
	set("self4", "ipv4_addr", self4)
	set("self6", "ipv6_addr", self6)
	set("router4", "ipv4_addr", router4)
	set("router6", "ipv6_addr", router6)
	set("lan4", "ipv4_addr", lan4)
	set("lan6", "ipv6_addr", lan6)
	set("home4", "ipv4_addr", home4)
	set("home6", "ipv6_addr", home6)

	const no = "reject with icmpx admin-prohibited"
	b.WriteString("\tchain out {\n\t\ttype filter hook output priority filter; policy accept;\n")
	line := func(format string, args ...any) {
		b.WriteString("\t\t")
		fmt.Fprintf(&b, format, args...)
		b.WriteString("\n")
	}
	line(`oif "lo" accept`)
	if f.Internet {
		line("ip daddr %s meta l4proto { tcp, udp } th dport 53 accept", DNSAddr)
	}
	line("ip daddr @self4 %s", no)
	line("ip6 daddr @self6 %s", no)
	line("ip daddr @router4 %s", no)
	line("ip6 daddr @router6 %s", no)
	line("ip daddr { %s } %s", special4, no)
	line("ip6 daddr { %s } %s", special6, no)
	line("ip daddr @home4 accept")
	line("ip6 daddr @home6 accept")
	line("ip daddr @lan4 %s", no)
	line("ip6 daddr @lan6 %s", no)
	line("ip daddr { %s } %s", private4, no)
	line("ip6 daddr { %s } %s", private6, no)
	if f.Internet {
		line("accept")
	} else {
		line(no)
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

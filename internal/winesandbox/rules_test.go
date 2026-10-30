package winesandbox

import (
	"net/netip"
	"strings"
	"testing"
)

// Example network: addresses are documentation ranges.
var exampleNet = Net{
	Self:    []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8:1::10"), netip.MustParseAddr("100.70.1.2")},
	Routers: []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("fe80::1")},
	LAN:     []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("2001:db8:1::/64")},
}

func TestHomeLAN(t *testing.T) {
	got := HomeLAN([]string{"192.0.2.0/24", "192.0.2.128/25", " 2001:db8:1::/64 ", "10.0.0.0/8", "0.0.0.0/0", "::/0", "junk", "192.0.0.0/16"}, exampleNet.LAN)
	want := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.0.2.128/25"), netip.MustParsePrefix("2001:db8:1::/64")}
	if len(got) != len(want) {
		t.Fatalf("HomeLAN = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HomeLAN = %v, want %v", got, want)
		}
	}
}

func chain(t *testing.T, rules string) []string {
	t.Helper()
	i := strings.Index(rules, "chain out {")
	if i < 0 {
		t.Fatalf("no chain in\n%s", rules)
	}
	var out []string
	for _, line := range strings.Split(rules[i:], "\n")[2:] {
		line = strings.TrimSpace(line)
		if line == "}" {
			break
		}
		out = append(out, line)
	}
	return out
}

func TestRulesetBothOff(t *testing.T) {
	rules := Ruleset(exampleNet, Flags{}, nil)
	if !strings.HasPrefix(rules, "table inet gjsb\ndelete table inet gjsb\ntable inet gjsb {\n") {
		t.Errorf("table is not replaced atomically:\n%s", rules)
	}
	lines := chain(t, rules)
	if last := lines[len(lines)-1]; last != "reject with icmpx admin-prohibited" {
		t.Errorf("last rule = %q, want reject", last)
	}
	if strings.Contains(rules, "dport 53") {
		t.Error("name lookups open with Internet off")
	}
	if strings.Contains(rules, "100.64.0.0/10 }") || strings.Contains(rules, "set home4 { type ipv4_addr; flags interval; auto-merge; elements") {
		t.Error("home set filled with Home off")
	}
}

func TestRulesetInternetOnly(t *testing.T) {
	rules := Ruleset(exampleNet, Flags{Internet: true}, nil)
	lines := chain(t, rules)
	if lines[0] != `oif "lo" accept` || !strings.Contains(lines[1], "169.254.53.53") {
		t.Errorf("first rules = %q", lines[:2])
	}
	if lines[len(lines)-1] != "accept" {
		t.Errorf("last rule = %q, want accept", lines[len(lines)-1])
	}
	for _, want := range []string{
		"set self4 { type ipv4_addr; flags interval; auto-merge; elements = { 100.70.1.2, 192.0.2.10 }; }",
		"set router4 { type ipv4_addr; flags interval; auto-merge; elements = { 100.100.100.100, 192.0.2.1 }; }",
		"set router6 { type ipv6_addr; flags interval; auto-merge; elements = { fd7a:115c:a1e0::53, fe80::1 }; }",
		"set lan4 { type ipv4_addr; flags interval; auto-merge; elements = { 192.0.2.0/24 }; }",
		"set home4 { type ipv4_addr; flags interval; auto-merge; }",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("missing %q in\n%s", want, rules)
		}
	}
}

func TestRulesetHome(t *testing.T) {
	home := HomeLAN([]string{"192.0.2.0/24"}, exampleNet.LAN)
	rules := Ruleset(exampleNet, Flags{Home: true}, home)
	if !strings.Contains(rules, "set home4 { type ipv4_addr; flags interval; auto-merge; elements = { 100.64.0.0/10, 192.0.2.0/24 }; }") {
		t.Errorf("home4 wrong:\n%s", rules)
	}
	if !strings.Contains(rules, "set home6 { type ipv6_addr; flags interval; auto-merge; elements = { fd7a:115c:a1e0::/48 }; }") {
		t.Errorf("home6 wrong:\n%s", rules)
	}
	lines := chain(t, rules)
	// Own addresses, routers and special ranges close before the home
	// sets open; LAN and private ranges close after.
	index := func(prefix string) int {
		for i, l := range lines {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		t.Fatalf("no rule %q", prefix)
		return -1
	}
	if !(index("ip daddr @self4") < index("ip daddr @home4") && index("ip daddr @router4") < index("ip daddr @home4") &&
		index("ip daddr { 0.0.0.0/8") < index("ip daddr @home4") && index("ip daddr @home4") < index("ip daddr @lan4") &&
		index("ip daddr @lan4") < index("ip daddr { 10.0.0.0/8")) {
		t.Errorf("rule order wrong:\n%s", strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != "reject with icmpx admin-prohibited" {
		t.Errorf("Internet off but last rule = %q", lines[len(lines)-1])
	}
}

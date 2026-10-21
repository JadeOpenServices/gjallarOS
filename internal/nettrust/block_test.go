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
)

func TestBlockRulesKeepTailscaleAndTheLAN(t *testing.T) {
	rules, err := blockRules([]string{"wlan0", "enp1s0"}, []netip.Prefix{
		netip.MustParsePrefix("172.16.5.0/24"), netip.MustParsePrefix("2001:db8:5::/64"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"table inet gjallar_vpn_block {}\ndelete table inet gjallar_vpn_block\n",
		`elements = { "wlan0", "enp1s0" }`,
		"169.254.0.0/16, 224.0.0.0/24, 255.255.255.255, 172.16.5.0/24 }",
		"fe80::/10, ff02::/16, 2001:db8:5::/64 }",
		"meta mark & 0xff0000 == 0x80000 accept",
		"udp sport 68 udp dport 67 accept",
		"type filter hook forward priority filter; policy accept;",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q:\n%s", want, rules)
		}
	}
	// DNS is dropped before the LAN is let through, in both chains.
	if strings.Count(rules, "th dport { 53, 853 } counter drop\n\t\tip daddr @lan4 accept") != 2 {
		t.Errorf("DNS drop not ahead of the LAN accept:\n%s", rules)
	}
	// The forward chain never trusts the mark: it is set by local sockets.
	if strings.Count(rules, "meta mark") != 1 {
		t.Errorf("mark accepted outside the output chain:\n%s", rules)
	}
}

func TestBlockRulesRefuseOddInterfaceNames(t *testing.T) {
	for _, dev := range []string{`wl"an0`, "a b", "", "averyveryverylongname"} {
		if _, err := blockRules([]string{dev}, nil); err == nil {
			t.Errorf("accepted %q", dev)
		}
	}
	if _, err := blockRules(nil, nil); err == nil {
		t.Error("accepted no uplink")
	}
}

func TestApplyBlockCoversEveryDefaultRoute(t *testing.T) {
	old := blockRulesPath
	blockRulesPath = filepath.Join(t.TempDir(), "vpn-block.nft")
	defer func() { blockRulesPath = old }()
	var calls []string
	s := System{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "ip -j -4 route show default":
			return []byte(`[{"dev":"wlan0"},{"dev":"tailscale0"}]`), nil
		case "ip -j -6 route show default":
			return []byte(`[{"dev":"wwan0"}]`), nil
		}
		return nil, nil
	}}
	if err := s.ApplyBlock(context.Background(), wifi("cafe", "", "172.16.5.0/24"), true); err != nil {
		t.Fatal(err)
	}
	rules, _ := os.ReadFile(blockRulesPath)
	if !strings.Contains(string(rules), `elements = { "wlan0", "wwan0" }`) {
		t.Fatalf("uplinks wrong:\n%s", rules)
	}
	if !slices.Contains(calls, "nft -f "+blockRulesPath) || !slices.Contains(calls, "nft list chain inet gjallar_vpn_block output") {
		t.Fatalf("calls = %q", calls)
	}
}

func TestApplyBlockRemovesOnlyAnInstalledTable(t *testing.T) {
	var calls []string
	installed := false
	s := System{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if args[0] == "list" && !installed {
			return nil, errors.New("no such table")
		}
		return nil, nil
	}}
	if err := s.ApplyBlock(context.Background(), nil, false); err != nil || len(calls) != 1 {
		t.Fatalf("absent: calls = %q, %v", calls, err)
	}
	installed, calls = true, nil
	if err := s.ApplyBlock(context.Background(), nil, false); err != nil ||
		!slices.Equal(calls, []string{"nft list table inet gjallar_vpn_block", "nft delete table inet gjallar_vpn_block"}) {
		t.Fatalf("installed: calls = %q, %v", calls, err)
	}
}

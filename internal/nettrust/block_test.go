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

// tempBlockPaths points the rules file and the boot marker into a test dir.
func tempBlockPaths(t *testing.T) {
	dir := t.TempDir()
	oldRules, oldMarker := blockRulesPath, BootBlockPath
	blockRulesPath = filepath.Join(dir, "vpn-block.nft")
	BootBlockPath = filepath.Join(dir, "vpn-boot-block")
	t.Cleanup(func() { blockRulesPath, BootBlockPath = oldRules, oldMarker })
}

func TestBootBlockRulesKeepOnlyLoopbackAndTheTunnel(t *testing.T) {
	rules := bootBlockRules()
	for _, want := range []string{
		"table inet gjallar_vpn_block {}\ndelete table inet gjallar_vpn_block\n",
		"\t\toifname \"lo\" accept\n\t\toifname \"tailscale*\" accept\n",
		"elements = { 169.254.0.0/16, 224.0.0.0/24, 255.255.255.255 }",
		"elements = { fe80::/10, ff02::/16 }",
		"meta mark & 0xff0000 == 0x80000 accept",
		"udp sport 68 udp dport 67 accept",
		"udp sport 546 udp dport 547 accept",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q:\n%s", want, rules)
		}
	}
	// No uplink set: every other interface is an uplink at boot.
	if strings.Contains(rules, "uplinks") {
		t.Errorf("boot rules name uplinks:\n%s", rules)
	}
	if strings.Count(rules, "th dport { 53, 853 } counter drop\n\t\tip daddr @lan4 accept") != 2 ||
		strings.Count(rules, "\t\tcounter drop\n\t}") != 2 || strings.Count(rules, "meta mark") != 1 {
		t.Errorf("chains differ from the verdict table:\n%s", rules)
	}
}

func TestApplyBootBlockLeavesAMarkerTheVerdictRemoves(t *testing.T) {
	tempBlockPaths(t)
	var calls []string
	s := System{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte(`[{"dev":"wlan0"}]`), nil
	}}
	if err := s.ApplyBootBlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"nft -f " + blockRulesPath, "nft list chain inet gjallar_vpn_block output"}) {
		t.Fatalf("calls = %q", calls)
	}
	if _, err := os.Stat(BootBlockPath); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	if err := s.ApplyBlock(context.Background(), wifi("cafe", "", "172.16.5.0/24"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(BootBlockPath); !os.IsNotExist(err) {
		t.Fatalf("marker left after a verdict: %v", err)
	}
}

func TestApplyBootBlockWritesNoMarkerOnFailure(t *testing.T) {
	tempBlockPaths(t)
	s := System{run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("nft failed")
	}}
	if err := s.ApplyBootBlock(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(BootBlockPath); !os.IsNotExist(err) {
		t.Fatalf("marker without a table: %v", err)
	}
}

func TestApplyBlockCoversEveryDefaultRoute(t *testing.T) {
	tempBlockPaths(t)
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
	tempBlockPaths(t)
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
	os.WriteFile(BootBlockPath, nil, 0644)
	if err := s.ApplyBlock(context.Background(), nil, false); err != nil ||
		!slices.Equal(calls, []string{"nft list table inet gjallar_vpn_block", "nft delete table inet gjallar_vpn_block"}) {
		t.Fatalf("installed: calls = %q, %v", calls, err)
	}
	if _, err := os.Stat(BootBlockPath); !os.IsNotExist(err) {
		t.Fatalf("boot marker left after the table went: %v", err)
	}
}

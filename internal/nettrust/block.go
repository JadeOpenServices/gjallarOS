package nettrust

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// BlockTable is the nftables table that cuts off the internet outside the
// tunnel while a verdict is Blocked.
const BlockTable = "gjallar_vpn_block"

// blockRulesPath holds the last ruleset handed to nft.
var blockRulesPath = "/run/gjallar/vpn-block.nft"

// BootBlockPath marks that the boot table is still in place: no verdict
// has replaced it since boot. Anyone may read it; it only reports.
var BootBlockPath = "/run/gjallar/vpn-boot-block"

// Kernel interface names; anything else is refused instead of quoted.
var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,15}$`)

// BlockText explains a Blocked reason in plain words.
func BlockText(reason string) string {
	switch reason {
	case BlockExitNodeOffline:
		return "the exit node is offline; internet is blocked until it is back"
	case BlockNoExitNode:
		return "no exit node can be set; internet is blocked outside the tunnel"
	case BlockTailscaleDown:
		return "Tailscale is not running; internet is blocked outside the tunnel"
	}
	return reason
}

// BootBlockText explains the boot table.
const BootBlockText = "internet outside the tunnel is blocked until the first network check since boot"

// BlockCause is the first half of BlockText as a sentence, for places with
// little room: "Tailscale is not running."
func BlockCause(reason string) string {
	cause, _, _ := strings.Cut(BlockText(reason), ";")
	if cause == "" {
		return ""
	}
	return strings.ToUpper(cause[:1]) + cause[1:] + "."
}

// blockRules builds the table that drops traffic leaving through an uplink,
// except tailscaled's own packets, DHCP, the local subnets and link-local
// addresses. DNS to the local network is dropped too: the queries would
// tell this network what the user looks up.
func blockRules(uplinks []string, subnets []netip.Prefix) (string, error) {
	if len(uplinks) == 0 {
		return "", fmt.Errorf("no uplink to block")
	}
	quoted := make([]string, 0, len(uplinks))
	for _, dev := range uplinks {
		if !ifaceName.MatchString(dev) {
			return "", fmt.Errorf("unexpected interface name %q", dev)
		}
		quoted = append(quoted, `"`+dev+`"`)
	}
	set := fmt.Sprintf("\tset uplinks {\n\t\ttype ifname\n\t\telements = { %s }\n\t}\n", strings.Join(quoted, ", "))
	return blockTable(set, []string{"oifname != @uplinks accept"}, subnets), nil
}

// bootBlockRules is the table installed at boot, before any network is up
// and before the first verdict: the uplinks and the LAN are not known yet,
// so everything but loopback and the tunnel is blocked, as in blockRules
// with no local subnet. It has the table name of blockRules, so the first
// verdict replaces or removes it in one step.
func bootBlockRules() string {
	return blockTable("", []string{`oifname "lo" accept`, `oifname "tailscale*" accept`}, nil)
}

// blockTable writes the block table: set holds extra set definitions and
// pass the first rules of each chain, the traffic that never counts as
// leaving through an uplink.
func blockTable(set string, pass []string, subnets []netip.Prefix) string {
	lan4 := []string{"169.254.0.0/16", "224.0.0.0/24", "255.255.255.255"}
	lan6 := []string{"fe80::/10", "ff02::/16"}
	for _, prefix := range subnets {
		if prefix.Addr().Is4() {
			lan4 = append(lan4, prefix.Masked().String())
		} else {
			lan6 = append(lan6, prefix.Masked().String())
		}
	}

	var b strings.Builder
	// Creating the table first lets the delete succeed when it is new, so
	// the swap is one transaction: there is never a moment without rules.
	fmt.Fprintf(&b, "table inet %s {}\ndelete table inet %s\n", BlockTable, BlockTable)
	fmt.Fprintf(&b, "table inet %s {\n", BlockTable)
	b.WriteString(set)
	fmt.Fprintf(&b, "\tset lan4 {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = { %s }\n\t}\n", strings.Join(lan4, ", "))
	fmt.Fprintf(&b, "\tset lan6 {\n\t\ttype ipv6_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = { %s }\n\t}\n", strings.Join(lan6, ", "))
	for _, chain := range []string{"output", "forward"} {
		fmt.Fprintf(&b, "\tchain %s {\n\t\ttype filter hook %s priority filter; policy accept;\n", chain, chain)
		for _, rule := range pass {
			b.WriteString("\t\t" + rule + "\n")
		}
		if chain == "output" {
			// Only root can set this mark or bind the DHCP client ports.
			fmt.Fprintf(&b, "\t\tmeta mark & 0x%x == 0x%x accept\n", tailscaleFwmarkMask, tailscaleBypassMark)
			b.WriteString("\t\tudp sport 68 udp dport 67 accept\n")
			b.WriteString("\t\tudp sport 546 udp dport 547 accept\n")
		}
		b.WriteString("\t\tmeta l4proto { tcp, udp } th dport { 53, 853 } counter drop\n")
		b.WriteString("\t\tip daddr @lan4 accept\n")
		b.WriteString("\t\tip6 daddr @lan6 accept\n")
		b.WriteString("\t\tcounter drop\n\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// parseDefaultDevices lists the devices of every default route, outside
// the tunnel.
func parseDefaultDevices(data []byte) ([]string, error) {
	var routes []struct {
		Dev string `json:"dev"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil, fmt.Errorf("parse default routes: %w", err)
	}
	var devs []string
	for _, r := range routes {
		if r.Dev != "" && !strings.HasPrefix(r.Dev, "tailscale") && !slices.Contains(devs, r.Dev) {
			devs = append(devs, r.Dev)
		}
	}
	return devs, nil
}

// ApplyBlock installs the block table for n, or removes it when block is
// false. Every device with an IPv4 or IPv6 default route counts as an
// uplink, so a second connection cannot carry the traffic around it.
func (s System) ApplyBlock(ctx context.Context, n *Network, block bool) error {
	if !block {
		if _, err := s.run(ctx, "nft", "list", "table", "inet", BlockTable); err != nil {
			return clearBootBlock() // not installed
		}
		if _, err := s.run(ctx, "nft", "delete", "table", "inet", BlockTable); err != nil {
			return err
		}
		return clearBootBlock()
	}
	uplinks := []string{n.Device}
	for _, family := range []string{"-4", "-6"} {
		out, err := s.run(ctx, "ip", "-j", family, "route", "show", "default")
		if err != nil {
			return err
		}
		devs, err := parseDefaultDevices(out)
		if err != nil {
			return err
		}
		for _, dev := range devs {
			if !slices.Contains(uplinks, dev) {
				uplinks = append(uplinks, dev)
			}
		}
	}
	rules, err := blockRules(uplinks, n.Subnets)
	if err != nil {
		return err
	}
	if err := s.loadBlock(ctx, rules); err != nil {
		return err
	}
	return clearBootBlock()
}

// ApplyBootBlock installs the boot table and the marker that reports it.
func (s System) ApplyBootBlock(ctx context.Context) error {
	if err := s.loadBlock(ctx, bootBlockRules()); err != nil {
		return err
	}
	return os.WriteFile(BootBlockPath, nil, 0644)
}

// loadBlock hands rules to nft and checks the table is there.
func (s System) loadBlock(ctx context.Context, rules string) error {
	if err := os.MkdirAll(filepath.Dir(blockRulesPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(blockRulesPath, []byte(rules), 0600); err != nil {
		return err
	}
	if _, err := s.run(ctx, "nft", "-f", blockRulesPath); err != nil {
		return err
	}
	_, err := s.run(ctx, "nft", "list", "chain", "inet", BlockTable, "output")
	return err
}

// clearBootBlock removes the marker once a verdict owns the table.
func clearBootBlock() error {
	if err := os.Remove(BootBlockPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

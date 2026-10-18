package config

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// TailscaleIntent configures Tailscale, selected as the tailscale app, and
// when its exit node acts as a VPN. A nil intent keeps 192.168.8.0/24
// always local, as before the installer asked.
type TailscaleIntent struct {
	// HomeSubnets are the LANs behind the home router, kept local at home.
	HomeSubnets []string `json:"homeSubnets"`
	// TrustedWifis are Wi-Fi names where no VPN is needed. A trusted name
	// only counts on a network that needed a key to join.
	TrustedWifis []string `json:"trustedWifis"`
	// ExitNode is the tailnet node used as VPN on untrusted networks; empty
	// leaves the exit node alone.
	ExitNode string `json:"exitNode"`
	// WifiExitNodes still send the internet through an exit node on these
	// trusted Wi-Fis, keeping their LAN reachable.
	WifiExitNodes map[string]string `json:"wifiExitNodes,omitempty"`
	// RouterHome trusts any LAN where a subnet router of the tailnet
	// proves it is, without home subnets or targets.
	RouterHome bool `json:"routerHome"`
	// SiteRouterTrust trusts a LAN whose tailnet router is nearby and
	// reaches every SiteRouterTargets entry.
	SiteRouterTrust   bool     `json:"siteRouterTrust"`
	SiteRouterTargets []string `json:"siteRouterTargets"`
}

func ValidateTailscale(t *TailscaleIntent) error {
	if t == nil {
		return nil
	}
	for _, raw := range t.HomeSubnets {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return fmt.Errorf("tailscale.homeSubnets: %q is not a CIDR subnet", raw)
		}
		if prefix.Bits() == 0 {
			return fmt.Errorf("tailscale.homeSubnets: %q would keep all traffic out of the VPN", raw)
		}
	}
	for _, ssid := range t.TrustedWifis {
		if ssid == "" || len(ssid) > 32 {
			return fmt.Errorf("tailscale.trustedWifis: %q is not a Wi-Fi name (1-32 bytes)", ssid)
		}
	}
	if strings.ContainsAny(t.ExitNode, " \t\n") {
		return fmt.Errorf("tailscale.exitNode: %q is not a node name or address", t.ExitNode)
	}
	for ssid, node := range t.WifiExitNodes {
		if !slices.Contains(t.TrustedWifis, ssid) {
			return fmt.Errorf("tailscale.wifiExitNodes: %q is not a trusted Wi-Fi", ssid)
		}
		if node == "" || strings.ContainsAny(node, " \t\n") {
			return fmt.Errorf("tailscale.wifiExitNodes: %q is not a node name or address", node)
		}
	}
	for _, target := range t.SiteRouterTargets {
		host, port, err := net.SplitHostPort(target)
		if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
			return fmt.Errorf("tailscale.siteRouterTargets: %q is not host:port", target)
		}
	}
	if t.SiteRouterTrust && len(t.SiteRouterTargets) == 0 {
		return fmt.Errorf("tailscale.siteRouterTrust needs siteRouterTargets, the home hosts a site router must reach")
	}
	return nil
}

// SplitList parses a comma-separated prompt answer. An item in single or
// double quotes is taken verbatim, so Wi-Fi names may hold commas or
// surrounding spaces.
func SplitList(raw string) ([]string, error) {
	out := []string{}
	for i := 0; i < len(raw); {
		switch raw[i] {
		case ' ', '\t', ',':
			i++
			continue
		case '"', '\'':
			end := strings.IndexByte(raw[i+1:], raw[i])
			if end < 0 {
				return nil, fmt.Errorf("missing closing %c in %q", raw[i], raw)
			}
			item := raw[i+1 : i+1+end]
			i += end + 2
			if rest := strings.TrimLeft(raw[i:], " \t"); rest != "" && rest[0] != ',' {
				return nil, fmt.Errorf("put a comma after the quoted %q", item)
			}
			out = append(out, item)
		default:
			end := strings.IndexByte(raw[i:], ',')
			if end < 0 {
				end = len(raw) - i
			}
			if item := strings.TrimSpace(raw[i : i+end]); item != "" {
				out = append(out, item)
			}
			i += end
		}
	}
	return out, nil
}

// DefaultSiteRouterTarget suggests the first host of the first home subnet,
// which is the home router on most LANs, on the DNS port.
func DefaultSiteRouterTarget(homeSubnets []string) string {
	for _, raw := range homeSubnets {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		return net.JoinHostPort(prefix.Masked().Addr().Next().String(), "53")
	}
	return ""
}

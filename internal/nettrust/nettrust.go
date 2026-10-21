// Package nettrust decides whether the current network is trusted and keeps
// the Tailscale exit node and the home-LAN routing bypass in line with it.
//
// A network is trusted when one of these holds:
//
//   - home: a local subnet is a home subnet, and either a tailnet router
//     proves it is on this LAN or the Wi-Fi is a trusted, secured network.
//     With RouterHome, a tailnet router that proves it is on this LAN is
//     enough, whatever the subnet.
//   - site: a tailnet router proves it is on this LAN, advertises this LAN,
//     and the required targets answer through it outside the tunnel.
//   - trusted-wifi: the Wi-Fi name is trusted and the connection is secured.
//
// Router proof is a direct Tailscale disco ping answered from an address in
// the local subnet. Disco pings are signed with the peer's node key, so a
// foreign network cannot fake it by copying a subnet or a Wi-Fi name.
package nettrust

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
)

// Exit node policy values besides a node name or address.
const (
	// ExitNodeAuto picks an online exit node, preferring one that routes a
	// home subnet: the home router.
	ExitNodeAuto = "auto"
	// ExitNodeOff keeps the exit node off on every network.
	ExitNodeOff = "off"
)

type Policy struct {
	HomeSubnets  []string `json:"homeSubnets"`
	TrustedWifis []string `json:"trustedWifis"`
	// ExitNode is used on untrusted networks: a node name or address,
	// ExitNodeAuto or ExitNodeOff. Empty leaves the exit node alone, or
	// keeps it off when WifiExitNodes needs it managed.
	ExitNode string `json:"exitNode"`
	// WifiExitNodes sends the internet through an exit node on these
	// trusted Wi-Fis too, keeping their LAN reachable. Keys are Wi-Fi
	// names, values are like ExitNode.
	WifiExitNodes map[string]string `json:"wifiExitNodes,omitempty"`
	// RouterHome makes any LAN home whose tailnet router proves it is
	// there. Any member of this tailnet counts; nodes shared in from
	// another tailnet never bring their subnet routes, so they cannot.
	RouterHome        bool     `json:"routerHome"`
	SiteRouterTrust   bool     `json:"siteRouterTrust"`
	SiteRouterTargets []string `json:"siteRouterTargets"`
}

// Managed reports whether the policy asks for trust decisions. Without
// one, the home subnets stay bypassed everywhere, as before nettrust.
func (p Policy) Managed() bool {
	return len(p.TrustedWifis) > 0 || p.ExitNode != "" || len(p.WifiExitNodes) > 0 || p.RouterHome || p.SiteRouterTrust
}

func LoadPolicy(path string) (Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("read network trust policy: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse network trust policy: %w", err)
	}
	if _, err := p.homePrefixes(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func (p Policy) homePrefixes() ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(p.HomeSubnets))
	for _, raw := range p.HomeSubnets {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid home subnet %q: %w", raw, err)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// Network is the connection that carries the default route.
type Network struct {
	Device  string
	Gateway netip.Addr
	Subnets []netip.Prefix
	SSID    string // empty when the device is not Wi-Fi
	KeyMgmt string // NetworkManager key-mgmt; empty for an open network
	// GatewayMAC is the gateway's hardware address; empty when it did not
	// answer.
	GatewayMAC string
}

// Secured reports whether joining the Wi-Fi needed a credential the access
// point had to know. Open and OWE networks can be cloned by anyone.
func (n Network) Secured() bool {
	switch n.KeyMgmt {
	case "wpa-psk", "sae", "wpa-eap", "wpa-eap-suite-b-192":
		return true
	}
	return false
}

type Peer struct {
	HostName string
	DNSName  string
	IPs      []netip.Addr
	Online   bool
	Routes   []netip.Prefix
	ExitNode bool // currently used as this machine's exit node
	// ExitNodeOption: the peer offers itself as an exit node.
	ExitNodeOption bool
}

// Matches reports whether name refers to this peer by host name, MagicDNS
// name or Tailscale address.
func (p Peer) Matches(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" {
		return false
	}
	dns := strings.TrimSuffix(strings.ToLower(p.DNSName), ".")
	if name == strings.ToLower(p.HostName) || name == dns {
		return true
	}
	if short, _, ok := strings.Cut(dns, "."); ok && name == short {
		return true
	}
	for _, ip := range p.IPs {
		if name == ip.String() {
			return true
		}
	}
	return false
}

type Tailnet struct {
	Running bool
	Peers   []Peer
}

func (t Tailnet) CurrentExitNode() *Peer {
	for i := range t.Peers {
		if t.Peers[i].ExitNode {
			return &t.Peers[i]
		}
	}
	return nil
}

// Prober runs the checks that touch the network.
type Prober interface {
	// DirectVia pings peer over Tailscale and returns the LAN endpoint that
	// answered, if the path is direct.
	DirectVia(ctx context.Context, peer Peer) (netip.Addr, bool)
	// Reach connects to a host:port target outside the Tailscale tunnel.
	Reach(ctx context.Context, target string) error
	// GatewayOwns reports whether addr belongs to the local gateway itself:
	// routed via it on this device and answering at one hop. A router's
	// direct path may use its WAN address instead of its LAN one.
	GatewayOwns(ctx context.Context, net *Network, addr netip.Addr) bool
}

const (
	TrustOffline     = "offline"
	TrustLegacy      = "legacy"
	TrustHome        = "home"
	TrustSite        = "site"
	TrustTrustedWifi = "trusted-wifi"
	TrustUntrusted   = "untrusted"
)

type Verdict struct {
	Trust  string `json:"trust"`
	Reason string `json:"reason"`
	Device string `json:"device,omitempty"`
	SSID   string `json:"ssid,omitempty"`
	Router string `json:"router,omitempty"`
	// RouterVia is the local address the router's proof came from, and
	// RouterTailnet its tailnet addresses. The time fallback reads them
	// (ROADMAP section 18, step 3).
	RouterVia     string   `json:"routerVia,omitempty"`
	RouterTailnet []string `json:"routerTailnet,omitempty"`
	Bypass        []string `json:"bypass"`
	ExitNode      string   `json:"exitNode"`
	// ManageExitNode is false when the policy names no exit node or
	// Tailscale is not running; the exit node is then left alone.
	ManageExitNode bool `json:"manageExitNode"`
	// ExitNodeError is why the wanted exit node could not be set; the
	// tunnel is then not protecting this network.
	ExitNodeError string `json:"exitNodeError,omitempty"`
	// Blocked says why the internet is cut off on this network: an exit
	// node is wanted here but cannot carry the traffic. Empty when not.
	Blocked string `json:"blocked,omitempty"`
	// Network is the NetworkKey of the network this verdict is for.
	Network string `json:"network,omitempty"`
	// Paused is set while a pause lets this network carry the traffic
	// without the tunnel; Blocked is then empty.
	Paused   *PauseState `json:"paused,omitempty"`
	Warnings []string    `json:"warnings,omitempty"`
}

// Blocked reasons. With BlockExitNodeOffline Tailscale drops the traffic
// itself; the firewall block in System.ApplyBlock backs up every reason.
const (
	BlockExitNodeOffline = "exit-node-offline"
	BlockNoExitNode      = "no-exit-node"
	BlockTailscaleDown   = "tailscale-down"
)

func (v Verdict) Trusted() bool {
	switch v.Trust {
	case TrustHome, TrustSite, TrustTrustedWifi:
		return true
	}
	return false
}

// Decide maps the observed network to a verdict. net is nil when no
// connection carries a default route.
func Decide(ctx context.Context, p Policy, net *Network, tn Tailnet, pr Prober) (Verdict, error) {
	home, err := p.homePrefixes()
	if err != nil {
		return Verdict{}, err
	}

	if !p.Managed() {
		return Verdict{
			Trust:  TrustLegacy,
			Reason: "no trust policy configured; home subnets stay local",
			Bypass: strs(home),
		}, nil
	}

	v := Verdict{ManageExitNode: (p.ExitNode != "" || len(p.WifiExitNodes) > 0) && tn.Running}

	if net == nil {
		// Leave the exit node as it is: clearing it between two untrusted
		// networks would only widen the window before the next decision.
		v.ManageExitNode = false
		v.Trust = TrustOffline
		v.Reason = "no default route"
		v.Bypass = []string{}
		return v, nil
	}
	v.Device, v.SSID = net.Device, net.SSID

	localHome := overlapping(home, net.Subnets)
	wifiTrusted := net.SSID != "" && slices.Contains(p.TrustedWifis, net.SSID) && net.Secured()
	if net.SSID != "" && slices.Contains(p.TrustedWifis, net.SSID) && !net.Secured() {
		v.Warnings = append(v.Warnings, fmt.Sprintf(
			"Wi-Fi %q is trusted by name but open (key-mgmt %q); anyone can clone it",
			net.SSID, net.KeyMgmt,
		))
	}

	router, via := provenRouter(ctx, net, tn, pr)
	if router != nil {
		v.Router = router.HostName + " via " + via.String()
		v.RouterVia = via.String()
		for _, ip := range router.IPs {
			v.RouterTailnet = append(v.RouterTailnet, ip.String())
		}
	}

	switch {
	case len(localHome) > 0 && (router != nil || wifiTrusted):
		v.Trust = TrustHome
		v.Reason = "on a home subnet, proven by " + proof(router, wifiTrusted)
		v.Bypass = strs(localHome)
		return v, nil

	case router != nil && p.RouterHome:
		v.Trust = TrustHome
		v.Reason = "tailnet router " + router.HostName + " is on this LAN"
		v.Bypass = strs(net.Subnets)
		return v, nil

	case router != nil && p.SiteRouterTrust:
		if missing := unreachable(ctx, p.SiteRouterTargets, pr); missing != "" {
			v.Warnings = append(v.Warnings, "site router "+router.HostName+" does not reach "+missing)
			break
		}
		v.Trust = TrustSite
		v.Reason = "tailnet router " + router.HostName + " is on this LAN and reaches every required target"
		v.Bypass = strs(net.Subnets)
		return v, nil
	}

	if wifiTrusted {
		v.Trust = TrustTrustedWifi
		v.Reason = fmt.Sprintf("trusted Wi-Fi %q (%s)", net.SSID, net.KeyMgmt)
		v.Bypass = strs(net.Subnets)
		if node := p.WifiExitNodes[net.SSID]; node != "" {
			v.Reason += ", internet through an exit node"
			v.ExitNode, v.ExitNodeError = pickExitNode(node, tn, home, v.ManageExitNode)
			v.warnOffline(tn)
			v.failClosed(node, tn)
		}
		return v, nil
	}

	v.Trust = TrustUntrusted
	v.Reason = "no trusted Wi-Fi and no tailnet router proof"
	v.Bypass = []string{}
	v.ExitNode, v.ExitNodeError = pickExitNode(p.ExitNode, tn, home, v.ManageExitNode)
	v.warnOffline(tn)
	v.failClosed(p.ExitNode, tn)
	if len(localHome) > 0 {
		v.Warnings = append(v.Warnings, fmt.Sprintf(
			"local subnet collides with home subnet %s; home traffic stays in the tunnel",
			strings.Join(strs(localHome), ", "),
		))
	}
	return v, nil
}

// warnOffline: a named exit node stays set while it is offline, so
// Tailscale blocks the internet instead of sending it around the tunnel.
func (v *Verdict) warnOffline(tn Tailnet) {
	if !v.ManageExitNode || v.ExitNode == "" {
		return
	}
	for _, peer := range tn.Peers {
		if peer.Matches(v.ExitNode) && !peer.Online {
			v.Warnings = append(v.Warnings, "exit node "+v.ExitNode+" is offline; internet is blocked until it is back")
			return
		}
	}
}

// failClosed records why the internet is blocked when choice wants an exit
// node on this network and none can carry the traffic.
func (v *Verdict) failClosed(choice string, tn Tailnet) {
	switch {
	case choice == "" || choice == ExitNodeOff:
	case !tn.Running:
		v.Blocked = BlockTailscaleDown
	case v.ExitNode == "" || v.ExitNodeError != "":
		v.Blocked = BlockNoExitNode
	default:
		for _, peer := range tn.Peers {
			if peer.Matches(v.ExitNode) && !peer.Online {
				v.Blocked = BlockExitNodeOffline
				return
			}
		}
	}
}

// pickExitNode resolves a policy exit node choice to the node to use.
func pickExitNode(choice string, tn Tailnet, home []netip.Prefix, managed bool) (node, failure string) {
	switch choice {
	case "", ExitNodeOff:
		return "", ""
	case ExitNodeAuto:
		if peer := autoExitNode(tn, home, true); peer != nil {
			return peer.HostName, ""
		}
		if !managed {
			return "", ""
		}
		// Fail closed, as a named node does: keep the exit node that is
		// set, or set an offline one, so Tailscale blocks the traffic
		// instead of the untrusted network carrying it.
		if current := tn.CurrentExitNode(); current != nil {
			return current.HostName, ""
		}
		if peer := autoExitNode(tn, home, false); peer != nil {
			return peer.HostName, ""
		}
		return "", "auto: no exit node in this tailnet"
	default:
		return choice, ""
	}
}

// autoExitNode picks an exit node, online only when online is set,
// preferring one that routes a home subnet. Peers are sorted by host name,
// so the pick is stable.
func autoExitNode(tn Tailnet, home []netip.Prefix, online bool) *Peer {
	var fallback *Peer
	for i := range tn.Peers {
		peer := &tn.Peers[i]
		if (online && !peer.Online) || !peer.ExitNodeOption || len(peer.IPs) == 0 {
			continue
		}
		if len(overlapping(home, peer.Routes)) > 0 {
			return peer
		}
		if fallback == nil {
			fallback = peer
		}
	}
	return fallback
}

func proof(router *Peer, wifiTrusted bool) string {
	switch {
	case router != nil && wifiTrusted:
		return "tailnet router " + router.HostName + " and trusted Wi-Fi"
	case router != nil:
		return "tailnet router " + router.HostName
	default:
		return "trusted Wi-Fi"
	}
}

// provenRouter returns an online peer that advertises a local subnet and
// answers a direct ping from inside it or from the local gateway.
func provenRouter(ctx context.Context, net *Network, tn Tailnet, pr Prober) (*Peer, netip.Addr) {
	if !tn.Running {
		return nil, netip.Addr{}
	}
	for i := range tn.Peers {
		peer := &tn.Peers[i]
		if !peer.Online || !advertisesLocal(peer.Routes, net.Subnets) {
			continue
		}
		via, ok := pr.DirectVia(ctx, *peer)
		if !ok {
			continue
		}
		for _, subnet := range net.Subnets {
			if subnet.Contains(via) {
				return peer, via
			}
		}
		if pr.GatewayOwns(ctx, net, via) {
			return peer, via
		}
	}
	return nil, netip.Addr{}
}

// advertisesLocal ignores exit-node default routes, which cover every
// subnet and say nothing about where the peer is.
func advertisesLocal(routes, local []netip.Prefix) bool {
	for _, route := range routes {
		if route.Bits() == 0 {
			continue
		}
		for _, subnet := range local {
			if route.Bits() <= subnet.Bits() && route.Contains(subnet.Addr()) {
				return true
			}
		}
	}
	return false
}

func unreachable(ctx context.Context, targets []string, pr Prober) string {
	if len(targets) == 0 {
		return "any target (siteRouterTargets is empty)"
	}
	for _, target := range targets {
		if err := pr.Reach(ctx, target); err != nil {
			return target + ": " + err.Error()
		}
	}
	return ""
}

func overlapping(home, local []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, h := range home {
		for _, l := range local {
			if h.Overlaps(l) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

func strs(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, p.Masked().String())
	}
	return out
}

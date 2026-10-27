package nettrust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// BypassPriority runs before Tailscale's policy rules (5210-5270).
	BypassPriority = "2500"
	// tailscaleBypassMark is the fwmark tailscaled gives its own traffic;
	// its rules send it to the main table, outside the tunnel. Tailscale
	// owns the bits in tailscaleFwmarkMask (util/linuxfw).
	tailscaleBypassMark = 0x80000
	tailscaleFwmarkMask = 0xff0000
)

type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// System observes and changes the real machine.
type System struct {
	run runner
}

func NewSystem() System { return System{run: execRun} }

// Network returns the connection with the lowest-metric default route, or
// nil when there is none.
func (s System) Network(ctx context.Context) (*Network, error) {
	out, err := s.run(ctx, "ip", "-j", "route", "show", "default")
	if err != nil {
		return nil, err
	}
	dev, gateway, ok, err := parseDefaultRoute(out)
	if err != nil || !ok {
		return nil, err
	}

	out, err = s.run(ctx, "ip", "-j", "addr", "show", "dev", dev)
	if err != nil {
		return nil, err
	}
	subnets, err := parseSubnets(out)
	if err != nil {
		return nil, err
	}

	n := &Network{Device: dev, Gateway: gateway, Subnets: subnets}
	n.GatewayMAC = s.gatewayMAC(ctx, n)

	typ, err := s.nmGet(ctx, "device", "show", dev, "GENERAL.TYPE")
	if err != nil || typ != "wifi" {
		return n, nil
	}
	uuid, err := s.nmGet(ctx, "device", "show", dev, "GENERAL.CON-UUID")
	if err != nil || uuid == "" {
		return n, nil
	}
	n.SSID, err = s.nmGet(ctx, "connection", "show", "uuid", uuid, "802-11-wireless.ssid")
	if err != nil {
		return nil, err
	}
	// Open profiles have no security setting; nmcli then fails the field.
	n.KeyMgmt, _ = s.nmGet(ctx, "connection", "show", "uuid", uuid, "802-11-wireless-security.key-mgmt")
	return n, nil
}

// gatewayMAC reads the gateway's hardware address from the neighbour
// table, after one marked echo when the entry is missing or failed.
func (s System) gatewayMAC(ctx context.Context, n *Network) string {
	if !n.Gateway.IsValid() {
		return ""
	}
	for try := 0; try < 2; try++ {
		out, err := s.run(ctx, "ip", "-j", "neigh", "show", n.Gateway.String(), "dev", n.Device)
		if err == nil {
			if mac := parseNeighMAC(out); mac != "" {
				return mac
			}
		}
		if try == 0 {
			mark := strconv.Itoa(tailscaleBypassMark)
			s.run(ctx, "ping", "-n", "-q", "-c", "1", "-W", "1", "-m", mark, "-I", n.Device, n.Gateway.String())
		}
	}
	return ""
}

func parseNeighMAC(data []byte) string {
	var entries []struct {
		LLAddr string   `json:"lladdr"`
		State  []string `json:"state"`
	}
	if json.Unmarshal(data, &entries) != nil {
		return ""
	}
	for _, e := range entries {
		if e.LLAddr != "" && !slices.Contains(e.State, "FAILED") && !slices.Contains(e.State, "INCOMPLETE") {
			return e.LLAddr
		}
	}
	return ""
}

func (s System) nmGet(ctx context.Context, args ...string) (string, error) {
	field := args[len(args)-1]
	cmd := append([]string{"--escape", "no", "--get-values", field}, args[:len(args)-1]...)
	out, err := s.run(ctx, "nmcli", cmd...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func parseDefaultRoute(data []byte) (string, netip.Addr, bool, error) {
	var routes []struct {
		Dev     string `json:"dev"`
		Gateway string `json:"gateway"`
		Metric  int    `json:"metric"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return "", netip.Addr{}, false, fmt.Errorf("parse default routes: %w", err)
	}
	slices.SortStableFunc(routes, func(a, b struct {
		Dev     string `json:"dev"`
		Gateway string `json:"gateway"`
		Metric  int    `json:"metric"`
	}) int {
		return a.Metric - b.Metric
	})
	for _, r := range routes {
		if r.Dev == "" || strings.HasPrefix(r.Dev, "tailscale") {
			continue
		}
		gw, _ := netip.ParseAddr(r.Gateway)
		return r.Dev, gw, true, nil
	}
	return "", netip.Addr{}, false, nil
}

func parseSubnets(data []byte) ([]netip.Prefix, error) {
	var links []struct {
		AddrInfo []struct {
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
			Scope     string `json:"scope"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("parse addresses: %w", err)
	}
	var out []netip.Prefix
	for _, link := range links {
		for _, a := range link.AddrInfo {
			if a.Scope != "global" {
				continue
			}
			addr, err := netip.ParseAddr(a.Local)
			if err != nil {
				continue
			}
			prefix, err := addr.Prefix(a.PrefixLen)
			if err != nil {
				continue
			}
			if !slices.Contains(out, prefix) {
				out = append(out, prefix)
			}
		}
	}
	return out, nil
}

// tailnetSettle bounds the wait for a starting tailscaled. Every start
// triggers a decision, and one made before Running would leave the exit
// node unmanaged until the next trigger.
var tailnetSettle = 30 * time.Second

// tailscaledStarting reports whether systemd may still bring tailscaled up.
// A stopped or failed unit has no start coming, so waiting for it only
// delays the decision, a pause among them. Without an answer from systemd
// it keeps waiting, as before.
func (s System) tailscaledStarting(ctx context.Context) bool {
	out, _ := s.run(ctx, "systemctl", "is-active", "tailscaled.service")
	switch strings.TrimSpace(string(out)) {
	case "inactive", "failed", "deactivating":
		return false
	}
	return true
}

func (s System) Tailnet(ctx context.Context) (Tailnet, error) {
	deadline := time.Now().Add(tailnetSettle)
	for {
		out, err := s.run(ctx, "tailscale", "status", "--json")
		state := ""
		if len(out) > 0 {
			var head struct{ BackendState string }
			_ = json.Unmarshal(out, &head)
			state = head.BackendState
		}
		// No answer yet, NoState and Starting settle by themselves;
		// Stopped, NeedsLogin and NeedsMachineAuth wait for a person.
		noAnswer := err != nil && len(out) == 0
		starting := state == "NoState" || state == "Starting" ||
			(state == "" && (!noAnswer || s.tailscaledStarting(ctx)))
		if !starting || !time.Now().Before(deadline) {
			if noAnswer {
				// A stopped or logged-out tailscaled is not an error for us.
				return Tailnet{}, nil
			}
			return parseTailnet(out)
		}
		select {
		case <-ctx.Done():
			return Tailnet{}, nil
		case <-time.After(time.Second):
		}
	}
}

func parseTailnet(data []byte) (Tailnet, error) {
	var status struct {
		BackendState string
		Peer         map[string]struct {
			HostName       string
			DNSName        string
			TailscaleIPs   []string
			Online         bool
			PrimaryRoutes  []string
			ExitNode       bool
			ExitNodeOption bool
		}
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return Tailnet{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	t := Tailnet{Running: status.BackendState == "Running"}
	for _, p := range status.Peer {
		peer := Peer{HostName: p.HostName, DNSName: p.DNSName, Online: p.Online, ExitNode: p.ExitNode, ExitNodeOption: p.ExitNodeOption}
		for _, raw := range p.TailscaleIPs {
			if ip, err := netip.ParseAddr(raw); err == nil {
				peer.IPs = append(peer.IPs, ip)
			}
		}
		for _, raw := range p.PrimaryRoutes {
			if prefix, err := netip.ParsePrefix(raw); err == nil {
				peer.Routes = append(peer.Routes, prefix)
			}
		}
		t.Peers = append(t.Peers, peer)
	}
	slices.SortFunc(t.Peers, func(a, b Peer) int { return strings.Compare(a.HostName, b.HostName) })
	return t, nil
}

var pongVia = regexp.MustCompile(`pong from .* via (\S+) in `)

func (s System) DirectVia(ctx context.Context, peer Peer) (netip.Addr, bool) {
	if len(peer.IPs) == 0 {
		return netip.Addr{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, _ := s.run(ctx, "tailscale", "ping", "--c", "5", "--timeout", "2s", "--until-direct", peer.IPs[0].String())
	return parsePongVia(out)
}

func parsePongVia(out []byte) (netip.Addr, bool) {
	var via netip.Addr
	found := false
	for _, m := range pongVia.FindAllSubmatch(out, -1) {
		ap, err := netip.ParseAddrPort(string(m[1]))
		if err != nil {
			// DERP relays print "via DERP(fra)"; not a direct path.
			continue
		}
		via, found = ap.Addr().Unmap(), true
	}
	return via, found
}

// GatewayOwns sends one-hop echoes: a router answers for its own
// addresses, and forwards anything else into an expired TTL.
func (s System) GatewayOwns(ctx context.Context, n *Network, addr netip.Addr) bool {
	if n == nil || !n.Gateway.IsValid() || !addr.IsValid() {
		return false
	}
	// With the bypass mark, as tailscaled's own packets: an exit node left
	// on from the previous network must not route the probe into the tunnel.
	mark := strconv.Itoa(tailscaleBypassMark)
	out, err := s.run(ctx, "ip", "-j", "route", "get", addr.String(), "mark", mark)
	if err != nil {
		return false
	}
	var routes []struct {
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
	}
	if json.Unmarshal(out, &routes) != nil || len(routes) == 0 ||
		routes[0].Gateway != n.Gateway.String() || routes[0].Dev != n.Device {
		return false
	}
	_, err = s.run(ctx, "ping", "-n", "-q", "-c", "2", "-W", "1", "-t", "1", "-m", mark, "-I", n.Device, addr.String())
	return err == nil
}

// Reach connects with tailscaled's bypass mark so the probe takes the main
// table: the local gateway, not this machine's own tunnel.
func (System) Reach(ctx context.Context, target string) error {
	dialer := net.Dialer{
		Timeout: 4 * time.Second,
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, tailscaleBypassMark)
			}); err != nil {
				return err
			}
			return serr
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	return conn.Close()
}

// ApplyBypass replaces the bypass rules with prefixes. Each rule looks up
// the main table but ignores its default route, so a prefix that is not
// connected here falls through to Tailscale instead of the local gateway.
func (s System) ApplyBypass(ctx context.Context, prefixes []string) error {
	for _, family := range []string{"-4", "-6"} {
		for range 64 {
			if _, err := s.run(ctx, "ip", family, "rule", "del", "priority", BypassPriority); err != nil {
				break
			}
		}
	}
	for _, raw := range prefixes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return err
		}
		family := "-4"
		if prefix.Addr().Is6() {
			family = "-6"
		}
		if _, err := s.run(ctx, "ip", family, "rule", "add", "priority", BypassPriority,
			"to", prefix.String(), "lookup", "main", "suppress_prefixlength", "0"); err != nil {
			return err
		}
	}
	_, err := s.run(ctx, "ip", "route", "flush", "cache")
	return err
}

// ApplyExitNode sets or clears the exit node when it differs from want.
// It returns whether it changed anything.
func (s System) ApplyExitNode(ctx context.Context, tn Tailnet, want string) (bool, error) {
	current := tn.CurrentExitNode()
	switch {
	case want == "" && current == nil:
		return false, nil
	case want != "" && current != nil && current.Matches(want):
		return false, nil
	case want == "":
		_, err := s.run(ctx, "tailscale", "set", "--exit-node=")
		return err == nil, err
	default:
		// tailscale set takes an address or the MagicDNS name, not the
		// host name people see in the admin console.
		var peer *Peer
		for i := range tn.Peers {
			if tn.Peers[i].Matches(want) && len(tn.Peers[i].IPs) > 0 {
				peer = &tn.Peers[i]
				break
			}
		}
		if peer == nil {
			return false, fmt.Errorf("exit node %q is not in this tailnet", want)
		}
		// LAN access keeps captive portals and local printers usable.
		_, err := s.run(ctx, "tailscale", "set", "--exit-node="+peer.IPs[0].String(), "--exit-node-allow-lan-access=true")
		return err == nil, err
	}
}

func WriteStatus(path string, v Verdict) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Unchanged verdicts are not rewritten: path units watch this file.
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

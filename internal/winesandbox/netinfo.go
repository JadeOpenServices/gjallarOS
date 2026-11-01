package winesandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// ReadNet asks ip (iproute2) for the host's addresses, routes and IPv6
// routers.
func ReadNet(ctx context.Context, ip string) (Net, error) {
	run := func(args ...string) ([]byte, error) {
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, ip, append([]string{"-j"}, args...)...)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	addrs, err := run("addr", "show")
	if err != nil {
		return Net{}, err
	}
	var routes [][]byte
	for _, family := range []string{"-4", "-6"} {
		out, err := run(family, "route", "show", "table", "all", "type", "unicast")
		if err != nil {
			return Net{}, err
		}
		routes = append(routes, out)
	}
	neigh, err := run("-6", "neigh", "show")
	if err != nil {
		return Net{}, err
	}
	return ParseNet(addrs, routes, neigh)
}

// ParseNet reads the JSON of ip -j addr show, ip -j route show (one per
// address family) and ip -j -6 neigh show.
func ParseNet(addrJSON []byte, routeJSON [][]byte, neighJSON []byte) (Net, error) {
	var n Net
	var links []struct {
		AddrInfo []struct {
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
			Address   string `json:"address"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(addrJSON, &links); err != nil {
		return Net{}, fmt.Errorf("read addresses: %w", err)
	}
	for _, link := range links {
		for _, info := range link.AddrInfo {
			a, err := netip.ParseAddr(info.Local)
			if err != nil {
				continue
			}
			a = a.WithZone("")
			n.Self = append(n.Self, a)
			// A point-to-point address names its peer; its prefix is
			// not a LAN.
			if info.Address != "" && info.Address != info.Local {
				continue
			}
			if prefix, ok := lanPrefix(netip.PrefixFrom(a, info.PrefixLen)); ok {
				n.LAN = append(n.LAN, prefix)
			}
		}
	}

	for _, data := range routeJSON {
		var routes []struct {
			Dst      string `json:"dst"`
			Gateway  string `json:"gateway"`
			Protocol string `json:"protocol"`
			Via      *struct {
				Host string `json:"host"`
			} `json:"via"`
		}
		if err := json.Unmarshal(data, &routes); err != nil {
			return Net{}, fmt.Errorf("read routes: %w", err)
		}
		for _, r := range routes {
			gateway := r.Gateway
			if r.Via != nil {
				gateway = r.Via.Host
			}
			if gateway != "" {
				if a, err := netip.ParseAddr(gateway); err == nil {
					n.Routers = append(n.Routers, a.WithZone(""))
				}
				continue
			}
			// IPv6 routers announce on-link prefixes that need no
			// address of ours. Other routes without a gateway are left
			// out: a VPN's 0.0.0.0/1 is not a LAN.
			if r.Protocol != "ra" {
				continue
			}
			if prefix, err := netip.ParsePrefix(r.Dst); err == nil {
				if prefix, ok := lanPrefix(prefix); ok {
					n.LAN = append(n.LAN, prefix)
				}
			}
		}
	}

	// A router flag shows as "router": null, so only the key counts.
	var neighbours []map[string]json.RawMessage
	if err := json.Unmarshal(neighJSON, &neighbours); err != nil {
		return Net{}, fmt.Errorf("read IPv6 neighbours: %w", err)
	}
	for _, entry := range neighbours {
		if _, router := entry["router"]; !router {
			continue
		}
		var dst string
		if json.Unmarshal(entry["dst"], &dst) != nil {
			continue
		}
		if a, err := netip.ParseAddr(dst); err == nil {
			n.Routers = append(n.Routers, a.WithZone(""))
		}
	}
	return n, nil
}

// lanPrefix keeps a subnet that holds other devices. Host addresses,
// whole address families and loopback, link-local and multicast ranges are
// left out; the rules close the special ranges on their own.
func lanPrefix(prefix netip.Prefix) (netip.Prefix, bool) {
	if !prefix.IsValid() {
		return netip.Prefix{}, false
	}
	prefix = prefix.Masked()
	a := prefix.Addr()
	if prefix.Bits() == 0 || prefix.Bits() == a.BitLen() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() {
		return netip.Prefix{}, false
	}
	return prefix, true
}

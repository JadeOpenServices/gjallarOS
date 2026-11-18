package identity

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ProfileDir holds the NetworkManager profiles, below the old disk's btrfs
// top level and, without the leading "@/", below the new root.
const ProfileDir = nmDir

// MaxProfiles and MaxProfile cap how many profiles are listed and how big
// one may be; a certificate file next to a profile has the same cap.
const (
	MaxProfiles = maxProfiles
	MaxProfile  = maxProfile
)

type kind int

const (
	kindOther kind = iota // a profile that is not an identity item (ethernet, bridge, ...)
	kindBad               // not a profile NetworkManager would load
	kindWiFi
	kindWiFiEnterprise
	kindVPN
)

// group is one [name] of a keyfile with its keys in file order.
type group struct {
	name string
	keys []keyValue
}

type keyValue struct{ key, value string }

// parseKeyfile reads a keyfile, nm-settings-keyfile(5): the GLib key file
// format, comments with #, [group] lines and key=value lines. Anything else
// makes the whole file bad, as it does for NetworkManager. A group or a key
// in one group named twice is refused too: NetworkManager would merge
// them, and what is checked must be what it reads.
func parseKeyfile(raw []byte) ([]group, error) {
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("not UTF-8 text")
	}
	var groups []group
	seen := map[string]bool{}
	var keys map[string]bool
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimRight(strings.TrimLeft(line, " \t"), "\r")
		switch {
		case line == "" || line[0] == '#':
		case line[0] == '[':
			if len(line) < 3 || line[len(line)-1] != ']' || strings.ContainsAny(line[1:len(line)-1], "[]") {
				return nil, errors.New("a broken group line")
			}
			name := line[1 : len(line)-1]
			if seen[name] {
				return nil, fmt.Errorf("group %q twice", name)
			}
			seen[name] = true
			keys = map[string]bool{}
			groups = append(groups, group{name: name})
		default:
			key, value, ok := strings.Cut(line, "=")
			key = strings.TrimRight(key, " \t")
			if !ok || key == "" || len(groups) == 0 {
				return nil, errors.New("a line that is no key")
			}
			if keys[key] {
				return nil, fmt.Errorf("key %q twice", key)
			}
			keys[key] = true
			g := &groups[len(groups)-1]
			g.keys = append(g.keys, keyValue{key, strings.Trim(value, " \t")})
		}
	}
	return groups, nil
}

// kindOf tells the connection type of parsed groups; only the type and the
// presence of [802-1x] are looked at.
func kindOf(groups []group) kind {
	typ, enterprise := "", false
	for _, g := range groups {
		if g.name == "802-1x" {
			enterprise = true
		}
		if g.name != "connection" {
			continue
		}
		for _, kv := range g.keys {
			if kv.key == "type" {
				typ = kv.value
			}
		}
	}
	switch typ {
	case "":
		return kindBad
	case "wifi", "802-11-wireless":
		if enterprise {
			return kindWiFiEnterprise
		}
		return kindWiFi
	case "vpn", "wireguard":
		return kindVPN
	}
	return kindOther
}

// profileKind reads the connection type of a keyfile.
func profileKind(raw []byte) kind {
	groups, err := parseKeyfile(raw)
	if err != nil {
		return kindBad
	}
	return kindOf(groups)
}

// Profile is a NetworkManager profile of the old disk, written again from
// an allowlist for the new disk (L64).
type Profile struct {
	// Item is WiFi, WiFiEnterprise or VPN; set also when the profile is
	// refused, so the caller can tell which item it stays behind for.
	Item Item
	// Data is the new keyfile. It holds secrets: the caller clears it.
	Data []byte
	// Certs names the certificate files in ProfileDir the profile points
	// to; they go along with it.
	Certs []string
}

// check tells whether a value of one allowlisted key is sound; a key that
// is not on the list is left out, an allowlisted key with a value that
// fails its check refuses the whole profile.
type check func(p *Profile, value string) bool

var (
	reText     = regexp.MustCompile(`^[^\x00-\x1f\x7f]*$`)
	reInt      = regexp.MustCompile(`^-?[0-9]{1,10}$`)
	reUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reWords    = regexp.MustCompile(`^[a-z0-9-]+(;[a-z0-9-]+)*;?$`)
	reDomain   = regexp.MustCompile(`^[A-Za-z0-9.-]+(;[A-Za-z0-9.-]+)*;?$`)
	reWGKey    = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)
	reHost     = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+):[0-9]{1,5}$`)
	reUsers    = regexp.MustCompile(`^(user:[a-z_][a-z0-9_-]{0,31}:?;)*$`)
	reBlob     = regexp.MustCompile(`^data:;base64,[A-Za-z0-9+/]+={0,2}$`)
	reAddrN    = regexp.MustCompile(`^address[0-9]{1,3}$`)
	reWEPKey   = regexp.MustCompile(`^wep-key[0-3]$`)
	reCertName = regexp.MustCompile(`^[A-Za-z0-9_+@-][A-Za-z0-9_+@.-]*\.(pem|der|crt|cer|p12|key)$`)
)

func text(_ *Profile, v string) bool   { return reText.MatchString(v) }
func number(_ *Profile, v string) bool { return reInt.MatchString(v) }
func boolean(_ *Profile, v string) bool {
	return v == "true" || v == "false" || v == "1" || v == "0"
}
func words(_ *Profile, v string) bool   { return reWords.MatchString(v) }
func domains(_ *Profile, v string) bool { return reDomain.MatchString(v) }
func wgKey(_ *Profile, v string) bool   { return reWGKey.MatchString(v) }

func oneOf(allowed ...string) check {
	return func(_ *Profile, v string) bool {
		for _, a := range allowed {
			if v == a {
				return true
			}
		}
		return false
	}
}

func addrs(_ *Profile, v string) bool {
	for a := range strings.SplitSeq(strings.TrimSuffix(v, ";"), ";") {
		if _, err := netip.ParseAddr(a); err != nil {
			return false
		}
	}
	return true
}

func prefixes(_ *Profile, v string) bool {
	for a := range strings.SplitSeq(strings.TrimSuffix(v, ";"), ";") {
		if _, err := netip.ParsePrefix(a); err != nil {
			return false
		}
	}
	return true
}

// address is addressN: a prefix with an optional gateway after a comma.
func address(_ *Profile, v string) bool {
	pfx, gw, hasGW := strings.Cut(v, ",")
	if _, err := netip.ParsePrefix(pfx); err != nil {
		return false
	}
	if hasGW {
		if _, err := netip.ParseAddr(gw); err != nil {
			return false
		}
	}
	return true
}

func endpoint(_ *Profile, v string) bool {
	if !reHost.MatchString(v) {
		return false
	}
	port, err := strconv.Atoi(v[strings.LastIndexByte(v, ':')+1:])
	return err == nil && port > 0 && port < 65536
}

// cert takes a certificate given inline (data:;base64,...) or as a file in
// ProfileDir itself; that file goes along. A path anywhere else (a home
// folder, /etc/shadow) is refused: root would read it for the profile.
func cert(p *Profile, v string) bool {
	if reBlob.MatchString(v) {
		return true
	}
	// NetworkManager reads a path without a folder next to the profile.
	path := strings.TrimPrefix(v, "file://")
	dir := "/" + strings.TrimPrefix(ProfileDir, "@/") + "/"
	name, ok := strings.CutPrefix(path, dir)
	if !ok && !strings.HasPrefix(v, "file://") {
		name, ok = path, true
	}
	if !ok || !CertName(name) {
		return false
	}
	p.Certs = append(p.Certs, name)
	return true
}

// CertName tells whether name is a file name a certificate next to the
// profiles may have: one plain, visible name, nothing to decode, with the
// ending NetworkManager takes a bare name as a file by (a list like 1;2;3;
// is a certificate given inline as bytes; that is not taken).
func CertName(name string) bool {
	return len(name) <= 255 && reCertName.MatchString(name)
}

// ProfileName tells whether name is a profile NetworkManager loads from
// ProfileDir: it ignores hidden files and writes *.nmconnection.
func ProfileName(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".nmconnection") &&
		len(name) <= 255 && !strings.ContainsAny(name, "/\\") && reText.MatchString(name)
}

// The allowlist: per group, the keys that go along. A group that is not
// here is left out whole, with whatever it holds (scripts, foreign paths,
// plugin data). Static addresses and DNS stay, so a profile behaves as it
// did; secrets that NetworkManager keeps in the user's keyring (the *-flags)
// stay there and come with the keyring.
var (
	connectionKeys = map[string]check{
		"id": text, "uuid": func(_ *Profile, v string) bool { return reUUID.MatchString(v) },
		"type": text, "autoconnect": boolean, "autoconnect-priority": number,
		"permissions": func(_ *Profile, v string) bool { return reUsers.MatchString(v) },
	}
	wifiKeys = map[string]check{
		"ssid": text, "mode": oneOf("infrastructure"), "hidden": boolean,
		"security": oneOf("802-11-wireless-security"),
	}
	wifiSecurityKeys = map[string]check{
		"key-mgmt": oneOf("none", "ieee8021x", "wpa-psk", "sae", "owe", "wpa-eap", "wpa-eap-suite-b-192"),
		"psk":      text, "psk-flags": number, "auth-alg": oneOf("open", "shared", "leap"),
		"wep-key-flags": number, "wep-key-type": number, "wep-tx-keyidx": number, "pmf": number,
	}
	dot1xKeys = map[string]check{
		"eap": words, "identity": text, "anonymous-identity": text, "password": text,
		"password-flags": number, "phase2-auth": words, "phase2-autheap": words,
		"phase1-peapver": number, "domain-suffix-match": domains, "domain-match": domains,
		"system-ca-certs": boolean, "private-key-password": text, "private-key-password-flags": number,
		"phase2-private-key-password": text, "phase2-private-key-password-flags": number,
		"ca-cert": cert, "client-cert": cert, "private-key": cert,
		"phase2-ca-cert": cert, "phase2-client-cert": cert, "phase2-private-key": cert,
	}
	ipKeys = map[string]check{
		"method":  oneOf("auto", "manual", "disabled", "link-local", "ignore", "dhcp"),
		"gateway": addrs, "dns": addrs, "dns-search": domains, "ignore-auto-dns": boolean,
		"never-default": boolean, "may-fail": boolean, "dns-priority": number, "route-metric": number,
		"addr-gen-mode": words, "ip6-privacy": number,
	}
	wireguardKeys = map[string]check{
		"private-key": wgKey, "private-key-flags": number, "listen-port": number,
		"mtu": number, "peer-routes": boolean,
	}
	peerKeys = map[string]check{
		"endpoint": endpoint, "allowed-ips": prefixes, "preshared-key": wgKey,
		"preshared-key-flags": number, "persistent-keepalive": number,
	}
)

// keysFor returns the allowlist of group name in a profile of kind k, or
// nil when the group is left out.
func keysFor(k kind, name string) func(key string) check {
	pick := func(m map[string]check) func(string) check {
		return func(key string) check { return m[key] }
	}
	switch name {
	case "connection":
		return pick(connectionKeys)
	case "ipv4", "ipv6":
		return func(key string) check {
			if reAddrN.MatchString(key) {
				return address
			}
			return ipKeys[key]
		}
	}
	switch k {
	case kindWiFi, kindWiFiEnterprise:
		switch name {
		case "wifi", "802-11-wireless":
			return pick(wifiKeys)
		case "wifi-security", "802-11-wireless-security":
			return func(key string) check {
				if reWEPKey.MatchString(key) {
					return text
				}
				return wifiSecurityKeys[key]
			}
		case "802-1x":
			return pick(dot1xKeys)
		}
	case kindVPN:
		if name == "wireguard" {
			return pick(wireguardKeys)
		}
		if pub, ok := strings.CutPrefix(name, "wireguard-peer."); ok && reWGKey.MatchString(pub) {
			return pick(peerKeys)
		}
	}
	return nil
}

// ErrNotCarried refuses a profile the stick does not carry over.
var ErrNotCarried = errors.New("not a profile the stick carries over")

// CleanProfile writes the keyfile raw again from the allowlist above: Wi-Fi
// keeps name, security type and key; an enterprise Wi-Fi its 802.1X login,
// with certificates inline or next to the profile only; a WireGuard VPN its
// keys and peers. A plugin VPN (OpenVPN and the like) stays behind whole:
// its data names programs and files the plugin runs and reads as root.
func CleanProfile(raw []byte) (Profile, error) {
	groups, err := parseKeyfile(raw)
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %v", ErrNotCarried, err)
	}
	var p Profile
	k := kindOf(groups)
	switch k {
	case kindWiFi:
		p.Item = WiFi
	case kindWiFiEnterprise:
		p.Item = WiFiEnterprise
	case kindVPN:
		p.Item = VPN
	default:
		return p, fmt.Errorf("%w: not Wi-Fi or VPN", ErrNotCarried)
	}
	for _, g := range groups {
		if g.name == "vpn" {
			return p, fmt.Errorf("%w: a plugin VPN", ErrNotCarried)
		}
	}
	data, err := rewrite(groups, len(raw), &p, func(name string) func(string) check { return keysFor(k, name) })
	if err != nil {
		return Profile{Item: p.Item}, err
	}
	p.Data = data
	return p, nil
}

// rewrite writes groups again with only the groups and keys allowed names;
// a kept key whose value fails its check refuses the whole file. size is
// the length of the raw file.
func rewrite(groups []group, size int, p *Profile, allowed func(group string) func(key string) check) ([]byte, error) {
	var out bytes.Buffer
	// Room for all of it at once: no copy of a secret is left behind in a
	// smaller buffer.
	out.Grow(2*size + 64)
	for _, g := range groups {
		keys := allowed(g.name)
		if keys == nil {
			continue
		}
		fmt.Fprintf(&out, "[%s]\n", g.name)
		for _, kv := range g.keys {
			ok := keys(kv.key)
			if ok == nil {
				continue
			}
			if !ok(p, kv.value) {
				clear(out.Bytes())
				return nil, fmt.Errorf("%w: [%s] %s", ErrNotCarried, g.name, kv.key)
			}
			fmt.Fprintf(&out, "%s=%s\n", kv.key, kv.value)
		}
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

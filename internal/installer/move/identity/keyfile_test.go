package identity

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// cleanLines checks that every line of data is a group line or a key the
// allowlist of k has for its group, and returns the lines.
func cleanLines(t *testing.T, k kind, data []byte) []string {
	t.Helper()
	var lines []string
	var allowed func(string) check
	for line := range strings.SplitSeq(string(data), "\n") {
		lines = append(lines, line)
		switch {
		case line == "":
		case line[0] == '[':
			if allowed = keysFor(k, line[1:len(line)-1]); allowed == nil {
				t.Errorf("group not on the list: %s", line)
			}
		default:
			key, _, _ := strings.Cut(line, "=")
			if allowed == nil || allowed(key) == nil {
				t.Errorf("key not on the list: %s", key)
			}
		}
	}
	return lines
}

func TestCleanProfileFixtures(t *testing.T) {
	for name, want := range map[string]struct {
		item Item
		ok   bool
	}{
		"personal-wifi": {WiFi, true},
		"wireguard":     {VPN, true},
		// Its certificate lies in a home folder.
		"companywifi": {WiFiEnterprise, false},
		// A plugin VPN stays behind whole.
		"redhat-openvpn": {VPN, false},
		"main-eth0":      {"", false},
	} {
		p, err := CleanProfile([]byte(fixture(t, name+".nmconnection")))
		if p.Item != want.item || (err == nil) != want.ok {
			t.Errorf("%s: item %q, %v", name, p.Item, err)
			continue
		}
		if err != nil {
			if !errors.Is(err, ErrNotCarried) || p.Data != nil {
				t.Errorf("%s: refused with %v, data %d bytes", name, err, len(p.Data))
			}
			continue
		}
		lines := cleanLines(t, profileKind([]byte(fixture(t, name+".nmconnection"))), p.Data)
		if !slices.Contains(lines, "[connection]") || slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "#") }) {
			t.Errorf("%s: %q", name, lines)
		}
	}
}

// L64: a profile with an extra section, a script-like key and a
// certificate path to /etc/shadow. The written profile holds only the
// allowlisted keys and no foreign path.
func TestCleanProfileL64(t *testing.T) {
	base := fixture(t, "personal-wifi.nmconnection")
	planted := strings.Replace(base, "[connection]\n", "[connection]\nsecondaries=x\nup-script=/bin/sh -c id\n", 1) +
		"\n[user]\norg.example.run=/bin/sh\n\n[proxy]\nmethod=auto\npac-url=file:///etc/shadow\n"
	p, err := CleanProfile([]byte(planted))
	if err != nil {
		t.Fatal(err)
	}
	lines := cleanLines(t, kindWiFi, p.Data)
	for _, bad := range []string{"secondaries", "up-script", "[user]", "[proxy]", "/etc/shadow", "/bin/sh"} {
		if strings.Contains(string(p.Data), bad) {
			t.Errorf("%s written: %q", bad, lines)
		}
	}
	if !slices.Contains(lines, "key-mgmt=wpa-psk") || !slices.Contains(lines, "uuid=cdac6154-a33b-4b15-9904-666772cfa5ee") {
		t.Errorf("allowlisted keys lost: %q", lines)
	}

	// The certificate of an enterprise profile: next to the profile or
	// inline goes, anything else refuses the profile.
	company := fixture(t, "companywifi.nmconnection")
	withCert := func(v string) string {
		return strings.Replace(company, "ca-cert=/home/joe/.cert/corp.crt", "ca-cert="+v, 1)
	}
	for _, v := range []string{
		"/etc/shadow",
		"file:///etc/shadow",
		"../../../etc/shadow",
		"/etc/NetworkManager/system-connections/../../shadow",
		"/etc/NetworkManager/system-connections/.hidden",
		"file:///etc/NetworkManager/system-connections/a%2F..%2Fshadow",
		"file://corp.crt",
		"1;2;3;",
	} {
		p, err := CleanProfile([]byte(withCert(v)))
		if !errors.Is(err, ErrNotCarried) || p.Item != WiFiEnterprise || p.Data != nil || p.Certs != nil {
			t.Errorf("ca-cert=%s: %v, %q", v, err, p.Certs)
		}
	}
	for v, certs := range map[string][]string{
		"corp.crt": {"corp.crt"},
		"/etc/NetworkManager/system-connections/corp.crt":        {"corp.crt"},
		"file:///etc/NetworkManager/system-connections/corp.crt": {"corp.crt"},
		"data:;base64,MIIB": nil,
	} {
		p, err := CleanProfile([]byte(withCert(v)))
		if err != nil || !slices.Equal(p.Certs, certs) {
			t.Errorf("ca-cert=%s: %v, %q", v, err, p.Certs)
			continue
		}
		cleanLines(t, kindWiFiEnterprise, p.Data)
	}
}

// An allowlisted key whose value fails its check refuses the profile; a
// file NetworkManager would read in a different way than we do is bad.
func TestCleanProfileRefused(t *testing.T) {
	base := fixture(t, "personal-wifi.nmconnection")
	for name, raw := range map[string]string{
		"uuid":          strings.Replace(base, "uuid=cdac6154-a33b-4b15-9904-666772cfa5ee", "uuid=$(id)", 1),
		"address":       strings.Replace(base, "[ipv4]\n", "[ipv4]\naddress1=not-an-address\n", 1),
		"dns":           strings.Replace(base, "[ipv4]\n", "[ipv4]\ndns=192.0.2.1;example\n", 1),
		"mode":          strings.Replace(base, "mode=infrastructure", "mode=ap", 1),
		"permissions":   strings.Replace(base, "[connection]\n", "[connection]\npermissions=user:a b;\n", 1),
		"control":       strings.Replace(base, "id=CompanyWIFI", "id=Company\x1bWIFI", 1),
		"key twice":     strings.Replace(base, "[wifi-security]\n", "[wifi-security]\nkey-mgmt=none\n", 1),
		"group twice":   base + "\n[wifi]\nhidden=true\n",
		"lone bracket":  "[\n",
		"no group name": "[]\n",
	} {
		if p, err := CleanProfile([]byte(raw)); !errors.Is(err, ErrNotCarried) || p.Data != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Static addresses, DNS and a user-only profile stay as they were.
	static := strings.Replace(base, "[ipv4]\nmethod=auto\n", "[ipv4]\nmethod=manual\naddress1=192.0.2.10/24,192.0.2.1\ndns=192.0.2.1;\ndns-search=example.org;\n", 1)
	static = strings.Replace(static, "[connection]\n", "[connection]\npermissions=user:example;\n", 1)
	p, err := CleanProfile([]byte(static))
	if err != nil {
		t.Fatal(err)
	}
	lines := cleanLines(t, kindWiFi, p.Data)
	for _, want := range []string{"address1=192.0.2.10/24,192.0.2.1", "dns=192.0.2.1;", "dns-search=example.org;", "permissions=user:example;", "method=manual"} {
		if !slices.Contains(lines, want) {
			t.Errorf("%s lost: %q", want, lines)
		}
	}
}

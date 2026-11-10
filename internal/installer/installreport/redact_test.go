package installreport

import (
	"strings"
	"testing"
)

// Every value in these tests is SYNTHETIC.
var syntheticKnown = Known{
	User:     "kestrel",
	Name:     "Ada Kestrelson",
	Host:     "lantern-box",
	Email:    "ada.k@example.invalid",
	GitHub:   "adak-example",
	LiveHost: "nixos",
	LiveUser: "nixos",
	Serials:  []string{"SYN0000SERIAL42", "eui.0000000000000000synthetic01"},
}

func TestRedactRemovesPersonalValues(t *testing.T) {
	for _, tc := range []struct{ name, in, gone, want string }{
		{"user", "chown kestrel:users failed", "kestrel", "chown <user>:users failed"},
		{"user in home", "open /home/kestrel/.config/x: denied", "kestrel", "/home/<user>/.config/x"},
		{"other home", "open /home/someone/x", "someone", "/home/<user>/x"},
		{"full name", "git config user.name 'Ada Kestrelson'", "Kestrelson", "'<name>'"},
		{"name part", "Hello Ada,", "Ada", "Hello <name>,"},
		{"host in store path", "/nix/store/0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p-nixos-system-lantern-box-26.05/bin", "lantern-box", "nixos-system-<host>-26.05"},
		{"host upper case", "LANTERN-BOX unreachable", "LANTERN", "<host> unreachable"},
		{"email", "commit by ada.k@example.invalid", "ada.k@", "commit by <email>"},
		{"other email", "mail other.person@example.org bounced", "other.person", "mail <email> bounced"},
		{"github", "github.com/adak-example/repo", "adak-example", "github.com/<github-user>/repo"},
		{"serial", "nvme SYN0000SERIAL42 timed out", "SYN0000SERIAL42", "nvme <serial> timed out"},
		{"disk by id", "mount /dev/disk/by-id/nvme-SYNTHETIC_DISK_1234-part2", "SYNTHETIC_DISK", "/dev/disk/by-id/<redacted>"},
		{"disk by label", "/dev/disk/by-label/AdaHome", "AdaHome", "/dev/disk/by-label/<redacted>"},
		{"uuid", "luks 01234567-89ab-cdef-0123-456789abcdef locked", "89ab", "luks <uuid> locked"},
		{"vfat serial", "esp ABCD-1234 missing", "ABCD-1234", "esp <uuid> missing"},
		{"mac", "link 02:00:5e:10:00:01 down", "5e:10", "link <mac> down"},
		{"ipv4", "connect 192.0.2.17:443 refused", "192.0.2.17", "connect <ip>:443 refused"},
		{"ipv6", "route fd00:1234::5 unreachable", "fd00:1234::5", "route <ip> unreachable"},
		{"url userinfo", "fetch https://ada:hunter2@example.invalid/x", "hunter2", "https://<redacted>@example.invalid/x"},
		{"password", "password=correct-horse", "correct-horse", "password=<redacted>"},
		{"quoted secret", `token: "abc def"`, "abc def", "token: <redacted>"},
		{"psk", "PSK=SyntheticPSK1", "SyntheticPSK1", "PSK=<redacted>"},
		{"pin", "pin=123456", "123456", "pin=<redacted>"},
		{"opaque", "auth AbCdEfGhIjKlMnOpQrStUvWxYz0123456789", "AbCdEfGh", "auth <redacted>"},
		{"base64 with slash", "key AbCd/EfGh+IjKlMnOpQrStUvWxYz0123456789==", "EfGh", "key <redacted>"},
		{"machine id", "machine 0123456789abcdef0123456789abcdef", "0123456789abcdef", "machine <redacted>"},
		{"control chars", "a\x1b[31mred\x07", "\x1b", "a[31mred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in, syntheticKnown)
			if strings.Contains(got, tc.gone) || !strings.Contains(got, tc.want) {
				t.Fatalf("Redact(%q) = %q; want %q and no %q", tc.in, got, tc.want, tc.gone)
			}
		})
	}
}

func TestRedactKeepsUsefulText(t *testing.T) {
	for _, in := range []string{
		"nixos-rebuild switch failed with exit code 1",
		"/nix/store/0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p-linux-6.12.57/lib/modules",
		"commit 2aa66ac7dd24a0900983ccfc6841d50c5e04663d",
		"hash mismatch: got sha256-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/AbCdE=",
		"linux-firmware-20261010-zstd-compressed-modules",
		"12:34:56 started",
		"PCI 8086:a7a1 and USB 32ac:0002",
		"connect 127.0.0.1:631 refused",
		"/run/current-system/sw/share/gjallaros/installer-reference-configuration",
		"version 1.2.3 of std::io",
		"usersettings and hostkeys stay",
	} {
		if got := Redact(in, syntheticKnown); got != in {
			t.Errorf("Redact(%q) = %q; want it unchanged", in, got)
		}
	}
}

func TestRedactSkipsShortAndGenericKnownValues(t *testing.T) {
	known := Known{User: "jo", Host: "nixos", LiveUser: "root"}
	in := "jo ran nixos-rebuild as root in /nix/store"
	if got := Redact(in, known); got != in {
		t.Fatalf("Redact = %q; want it unchanged", got)
	}
}

func TestErrorTextKeepsHeadAndTail(t *testing.T) {
	raw := "FIRST " + strings.Repeat("x ", maxError) + " LAST"
	got := ErrorText(raw, Known{})
	if !strings.HasPrefix(got, "FIRST") || !strings.HasSuffix(got, "LAST") || !strings.Contains(got, "bytes cut") {
		t.Fatalf("cut error lost its ends: %q ... %q", got[:20], got[len(got)-20:])
	}
	if len(got) > maxError+64 {
		t.Fatalf("cut error is %d bytes", len(got))
	}
}

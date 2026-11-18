package identity

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The fixture follows bluez doc/settings-storage.txt with made-up keys.
func TestCleanBluezInfo(t *testing.T) {
	base := fixture(t, "bluez-headset.info")
	out, err := CleanBluezInfo([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(out), "\n")
	for _, want := range []string{"[General]", "Trusted=true", "[LinkKey]", "Key=00112233445566778899AABBCCDDEEFF", "[LongTermKey]", "Rand=0", "[IdentityResolvingKey]", "Timeout=216"} {
		if !slices.Contains(lines, want) {
			t.Errorf("%s lost: %q", want, lines)
		}
	}
	for _, bad := range []string{"PreferredBearer", "[ServiceChanged]", "CCC_LE"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("%s written: %q", bad, lines)
		}
	}

	// L64: extra groups and keys are dropped.
	planted := strings.Replace(base, "[General]\n", "[General]\nExec=/bin/sh\n", 1) + "\n[Run]\nCommand=/bin/sh\n"
	if out, err := CleanBluezInfo([]byte(planted)); err != nil || strings.Contains(string(out), "/bin/sh") {
		t.Errorf("planted: %v, %q", err, out)
	}

	// A bad value of a kept key, a broken file or no pairing key refuses it.
	for name, raw := range map[string]string{
		"short key":   strings.Replace(base, "Key=00112233445566778899AABBCCDDEEFF", "Key=0011", 1),
		"path key":    strings.Replace(base, "Key=00112233445566778899AABBCCDDEEFF", "Key=../../../../etc/shadow", 1),
		"class":       strings.Replace(base, "Class=0x240404", "Class=240404", 1),
		"techs":       strings.Replace(base, "BR/EDR;LE;", "BR/EDR;X;", 1),
		"services":    strings.Replace(base, "fb;\n", "fb;x;\n", 1),
		"name":        strings.Replace(base, "Example Headset", "Example\x1bHeadset", 1),
		"rand":        strings.Replace(base, "Rand=0", "Rand=-1", 1),
		"key twice":   strings.Replace(base, "[LinkKey]\n", "[LinkKey]\nType=4\n", 1),
		"group twice": base + "\n[LinkKey]\nType=4\n",
		"no pairing":  "[General]\nName=Example\n",
		"bracket":     "[\n",
	} {
		if out, err := CleanBluezInfo([]byte(raw)); !errors.Is(err, ErrNotCarried) || out != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !BTAddress("00:1A:7D:DA:71:13") || BTAddress("00:1a:7d:da:71:13") || BTAddress("../x") {
		t.Error("BTAddress")
	}
}

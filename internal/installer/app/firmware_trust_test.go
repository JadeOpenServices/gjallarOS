package app

import (
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// EMULATED firmware states.
func TestAskFirmwareLocked(t *testing.T) {
	locked := secureboot.FirmwareTrust{SecureBoot: secureboot.FlagOn, SetupMode: secureboot.FlagOff, AdminPassword: secureboot.PasswordSet}
	for _, c := range []struct {
		name  string
		trust secureboot.FirmwareTrust
		input string
		ask   bool
		want  bool
	}{
		{"password set, default locked", locked, "\n", true, true},
		{"password set, user knows it", locked, "n\n", true, false},
		{"no interface, default not locked", secureboot.FirmwareTrust{SecureBoot: secureboot.FlagOn, SetupMode: secureboot.FlagOff, AdminPassword: secureboot.PasswordUnknown}, "\n", true, false},
		{"no password", secureboot.FirmwareTrust{SecureBoot: secureboot.FlagOn, SetupMode: secureboot.FlagOff, AdminPassword: secureboot.PasswordNotSet}, "", false, false},
		{"setup mode", secureboot.FirmwareTrust{SecureBoot: secureboot.FlagOn, SetupMode: secureboot.FlagOn, AdminPassword: secureboot.PasswordSet}, "", false, false},
		{"secure boot off", secureboot.FirmwareTrust{SecureBoot: secureboot.FlagOff, SetupMode: secureboot.FlagOff, AdminPassword: secureboot.PasswordSet}, "", false, false},
	} {
		ui, out := validationTestUI(c.input)
		got, err := askFirmwareLocked(context.Background(), ui, c.trust)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		asked := strings.Contains(out.String(), "locked")
		if got != c.want || asked != c.ask {
			t.Errorf("%s: locked=%v asked=%v\n%s", c.name, got, asked, out)
		}
	}
}

func TestFirmwareTrustReport(t *testing.T) {
	got := firmwareTrustReport(secureboot.FirmwareTrust{
		SecureBoot: secureboot.FlagOn, SetupMode: secureboot.FlagOn, AuditMode: secureboot.FlagMissing,
		AdminPassword: secureboot.PasswordUnknown, ShimValidationOff: true,
	})
	for _, want := range []string{
		"What this firmware trusts:\n  Secure Boot: on, Setup Mode: on\n",
		"  WARNING: Shim's signature check is switched off",
		"  Note: The firmware is in Setup Mode",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "WARNING") > strings.Index(got, "Note:") {
		t.Errorf("warning after note:\n%s", got)
	}
}

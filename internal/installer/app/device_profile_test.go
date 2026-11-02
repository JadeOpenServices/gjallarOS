package app

import (
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

func TestSecureBootSupportGateAcceptsDetectedSupportedPolicy(t *testing.T) {
	err := validateSecureBootFirmwareSupport(
		true,
		"laptop/framework",
		oddc.EffectiveSecureBootFirmwarePolicy{
			SourceEntity: "vendor/framework",
			Policy: oddc.SecureBootFirmwarePolicy{
				Supported: true,
			},
		},
	)

	if err != nil {
		t.Fatal(err)
	}
}

func TestSecureBootSupportGateRejectsDetectedUnsupportedPolicy(t *testing.T) {
	err := validateSecureBootFirmwareSupport(
		true,
		"laptop/hp/zbook-x2-g4",
		oddc.EffectiveSecureBootFirmwarePolicy{
			SourceEntity: "vendor/hp",
			Policy: oddc.SecureBootFirmwarePolicy{
				Supported:         false,
				SetupModeStrategy: "unsupported",
				UnsupportedReason: "HP transfer is not validated.",
			},
		},
	)

	if err == nil {
		t.Fatal("unsupported detected firmware accepted Secure Boot")
	}

	if !strings.Contains(err.Error(), "laptop/hp/zbook-x2-g4") {
		t.Fatalf("error does not identify detected profile: %v", err)
	}
}

func TestSecureBootSupportGateAllowsDisabledSecureBootOnUnsupportedDevice(t *testing.T) {
	err := validateSecureBootFirmwareSupport(
		false,
		"laptop/hp/zbook-x2-g4",
		oddc.EffectiveSecureBootFirmwarePolicy{
			Policy: oddc.SecureBootFirmwarePolicy{
				Supported:         false,
				SetupModeStrategy: "unsupported",
				UnsupportedReason: "HP transfer is not validated.",
			},
		},
	)

	if err != nil {
		t.Fatal(err)
	}
}

func TestManagedDeviceCannotBypassUnsupportedFirmwarePolicy(t *testing.T) {
	user := config.User{
		Management: config.ManagementIntent{
			Enable: true,
		},
	}

	normalizeManagementSafety(&user)

	if !user.SecureBootEnable {
		t.Fatal("managed policy did not require Secure Boot")
	}

	err := validateSecureBootFirmwareSupport(
		user.SecureBootEnable,
		"laptop/hp/zbook-x2-g4",
		oddc.EffectiveSecureBootFirmwarePolicy{
			SourceEntity: "vendor/hp",
			Policy: oddc.SecureBootFirmwarePolicy{
				Supported:         false,
				SetupModeStrategy: "unsupported",
				UnsupportedReason: "HP transfer is not validated.",
			},
		},
	)

	if err == nil {
		t.Fatal("managed device bypassed unsupported firmware policy")
	}
}

func TestSecureBootSupportGateRejectsMissingPolicy(t *testing.T) {
	err := validateSecureBootFirmwareSupport(
		true,
		"laptop/test",
		oddc.EffectiveSecureBootFirmwarePolicy{},
	)

	if err == nil {
		t.Fatal("missing Secure Boot firmware policy was accepted")
	}

	if !strings.Contains(err.Error(), "no trusted Secure Boot firmware policy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

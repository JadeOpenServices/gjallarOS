package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func TestMayOfferSecureBootFallback(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		managed    bool
		unattended bool
		want       bool
	}{
		{
			name:    "interactive unmanaged unsupported request may downgrade",
			enabled: true,
			want:    true,
		},
		{
			name:    "managed must fail closed",
			enabled: true,
			managed: true,
			want:    false,
		},
		{
			name:       "unattended must fail closed",
			enabled:    true,
			unattended: true,
			want:       false,
		},
		{
			name:    "interactive config file remains interactive",
			enabled: true,
			want:    true,
		},
		{
			name: "disabled secure boot needs no fallback",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mayOfferSecureBootFallback(
				tt.enabled,
				tt.managed,
				tt.unattended,
			)
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTPM2AllowedOnlyWithSecureBoot(t *testing.T) {
	if tpm2AllowedForSecureBoot(false) {
		t.Fatal("TPM2 measured-boot unlock allowed while Secure Boot disabled")
	}

	if !tpm2AllowedForSecureBoot(true) {
		t.Fatal("TPM2 measured-boot unlock blocked while Secure Boot enabled")
	}
}

func TestTPM2FollowsRequestOnFreshInstalls(t *testing.T) {
	tests := []struct {
		managed, persistent, want bool
	}{
		{managed: false, persistent: false, want: true},
		{managed: true, persistent: true, want: true},
		{managed: false, persistent: true, want: false},
	}
	for _, test := range tests {
		if got := tpm2FollowsRequest(test.managed, test.persistent); got != test.want {
			t.Errorf("tpm2FollowsRequest(managed=%t, persistent=%t) = %t, want %t",
				test.managed, test.persistent, got, test.want)
		}
	}
}

// A rerun on an installed host must hand the Startup unlock answer to the
// check, not ask again: the old second question defaulted to no and Enter
// rendered luksTpm2Enable = false (e2e-fw13, 2026-10-20).
func TestInstalledHostTPM2CheckGetsTheRequest(t *testing.T) {
	for _, requested := range []bool{true, false} {
		args := installerTPM2Args("/repo", "/repo/generated/hardware.nix", requested)
		want := []string{"installer", "tpm2", "--repo", "/repo", "--hardware", "/repo/generated/hardware.nix", "--requested=" + strconv.FormatBool(requested)}
		if !slices.Equal(args, want) {
			t.Errorf("installerTPM2Args(requested=%t) = %q, want %q", requested, args, want)
		}
	}
}

func TestNoSecureBootPolicyNoticeNamesTheCause(t *testing.T) {
	notice := noSecureBootPolicyNotice("model/hp/zbook-x2-g4")
	if !strings.Contains(notice, "ODDC has no Secure Boot setup for model/hp/zbook-x2-g4") {
		t.Fatalf("notice does not name the missing ODDC setup:\n%s", notice)
	}
	if !strings.Contains(noSecureBootPolicyNotice(""), "no model for this machine") {
		t.Fatal("notice without a model must say ODDC has no model")
	}
}

func TestFirmwareSecureBootNotice(t *testing.T) {
	if off := firmwareSecureBootNotice(false, nil); !strings.Contains(off, "Secure Boot is off") {
		t.Fatalf("firmware-off notice does not report the state:\n%s", off)
	}
	on := firmwareSecureBootNotice(true, nil)
	if !strings.Contains(on, "Secure Boot is ON") || !strings.Contains(on, "refuse to start") {
		t.Fatalf("firmware-on notice does not warn about the unsigned boot loader:\n%s", on)
	}
	if bad := firmwareSecureBootNotice(false, errors.New("boom")); !strings.Contains(bad, "could not be read (boom)") {
		t.Fatalf("unreadable state not reported:\n%s", bad)
	}
}

func TestCollectStartupUnlock(t *testing.T) {
	for _, tc := range []struct {
		input string
		pin   bool
	}{
		{"\n", false},
		{"1\n", false},
		{"2\n", true},
		{"PIN at startup\n", true},
	} {
		var out bytes.Buffer
		u := config.User{}
		if err := collectStartupUnlock(context.Background(), prompt.New(strings.NewReader(tc.input), &out), "10", false, &u); err != nil {
			t.Fatalf("%q: %v", tc.input, err)
		}
		if !u.LUKSTPM2Enable || u.LUKSTPM2PIN != tc.pin {
			t.Fatalf("%q: enable=%t pin=%t", tc.input, u.LUKSTPM2Enable, u.LUKSTPM2PIN)
		}
		if strings.Contains(out.String(), "keyboard attached") {
			t.Fatalf("%q: keyboard note on a notebook:\n%s", tc.input, out.String())
		}
	}
}

func TestStartupUnlockKeepsPINDefault(t *testing.T) {
	var out bytes.Buffer
	u := config.User{LUKSTPM2Enable: true, LUKSTPM2PIN: true}
	if err := collectStartupUnlock(context.Background(), prompt.New(strings.NewReader("\n"), &out), "", false, &u); err != nil {
		t.Fatal(err)
	}
	if !u.LUKSTPM2PIN {
		t.Fatal("empty answer dropped the PIN chosen before")
	}
}

func TestKeyboardOnlyAtStartup(t *testing.T) {
	for _, tc := range []struct {
		chassis string
		touch   bool
		want    bool
	}{
		{"30", false, true},
		{"32", false, true},
		{"32", true, false},
		{"31", false, false},
		{"10", false, false},
		{"", false, false},
	} {
		if got := keyboardOnlyAtStartup(tc.chassis, tc.touch); got != tc.want {
			t.Errorf("chassis %q touch %t: %t, want %t", tc.chassis, tc.touch, got, tc.want)
		}
	}
}

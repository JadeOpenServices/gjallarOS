package config

import (
	"strings"
	"testing"
)

func validManagedUser() User {
	return User{
		EndpointManagedDevice:  true,
		JODSEndpoint:           "https://jods.example.test:1666",
		JODSPolicySigningKey:   strings.Repeat("ab", 32),
		JODSRecoverySigningKey: strings.Repeat("cd", 32),
		JODSEnrollmentMode:     "manual",
		JODSDeviceClass:        "laptop",
		JODSDesktopProfile:     "headless",
	}
}

func TestValidateJODS(t *testing.T) {
	if err := ValidateJODS(validManagedUser()); err != nil {
		t.Fatal(err)
	}
	u := validManagedUser()
	u.JODSEndpoint = "http://jods.example.test"
	if err := ValidateJODS(u); err == nil {
		t.Fatal("accepted non-HTTPS JODS endpoint")
	}
	u = validManagedUser()
	u.JODSPolicySigningKey = "abcd"
	if err := ValidateJODS(u); err == nil {
		t.Fatal("accepted malformed JODS policy key")
	}
	u = validManagedUser()
	u.JODSRecoverySigningKey = "abcd"
	if err := ValidateJODS(u); err == nil {
		t.Fatal("accepted malformed JODS recovery-command key")
	}
	u = validManagedUser()
	u.JODSRecoverySigningKey = u.JODSPolicySigningKey
	if err := ValidateJODS(u); err == nil {
		t.Fatal("accepted one key for both JODS signing roles")
	}
	u = validManagedUser()
	u.JODSEndpoint = "https://127.0.0.1:1666"
	if err := ValidateJODS(u); err == nil {
		t.Fatal("accepted loopback JODS endpoint")
	}
	for _, channel := range []string{"dev", "staging"} {
		u = validManagedUser()
		u.ReleaseChannel = channel
		if err := ValidateJODS(u); err == nil {
			t.Fatalf("accepted the %s channel on a managed device", channel)
		}
	}
}

func TestUnmanagedJODSDoesNotRequireConfiguration(t *testing.T) {
	if err := ValidateJODS(User{}); err != nil {
		t.Fatal(err)
	}
}

func TestUnmanagedJODSRejectsPrebootLock(t *testing.T) {
	u := User{JODSPrebootLockEnable: true}
	if err := ValidateJODS(u); err == nil {
		t.Fatal("unmanaged device accepted JODS preboot lock")
	}
}

func TestValidateJODSAllowsHyprlandDesktopProfile(t *testing.T) {
	u := validManagedUser()
	u.JODSDesktopProfile = "hyprland"
	if err := ValidateJODS(u); err != nil {
		t.Fatalf("hyprland desktop profile rejected: %v", err)
	}
}

func TestValidateJODSRejectsUnsafeDesktopProfile(t *testing.T) {
	for _, profile := range []string{"", "../hyprland", "hypr land", "/etc/passwd", strings.Repeat("a", 65)} {
		u := validManagedUser()
		u.JODSDesktopProfile = profile
		if err := ValidateJODS(u); err == nil {
			t.Fatalf("accepted unsafe desktop profile %q", profile)
		}
	}
}

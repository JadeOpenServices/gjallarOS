package config

import (
	"strings"
	"testing"
)

func TestValidateHostname(t *testing.T) {
	base := User{
		Username:    "tester",
		Theme:       "noctalia",
		Shell:       "zsh",
		Editors:     []string{"vscodium"},
		Browsers:    []string{"firefox"},
		AIAgentMode: "workspace",
	}
	for _, name := range []string{"gjallarOS", "a", "lab-2", "old_name", strings.Repeat("a", 63)} {
		user := base
		user.Hostname = name
		if err := Validate(user); err != nil {
			t.Errorf("hostname %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"-lab", "lab-", "lab.example", "lab name", "läb", "_lab", strings.Repeat("a", 64)} {
		user := base
		user.Hostname = name
		if err := Validate(user); err == nil {
			t.Errorf("hostname %q accepted", name)
		}
	}
}

func TestValidateReleaseChannel(t *testing.T) {
	user, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"", "dev", "staging", "stable"} {
		user.ReleaseChannel = channel
		if err := Validate(user); err != nil {
			t.Fatalf("channel %q rejected: %v", channel, err)
		}
	}
	for _, channel := range []string{"main", "Stable", "stable ", "../dev"} {
		user.ReleaseChannel = channel
		if err := Validate(user); err == nil {
			t.Fatalf("channel %q accepted", channel)
		}
	}
	user.ReleaseChannel = ""
	if user.Channel() != "stable" {
		t.Fatalf("default channel = %q", user.Channel())
	}
}

func TestValidateSettingsSigningKey(t *testing.T) {
	user, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	// SYNTHETIC key: 32 bytes of 'k' in the OpenSSH wire format.
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGtra2tra2tra2tra2tra2tra2tra2tra2tra2tra2tr"
	for _, value := range []string{"", key, key + " SYNTHETIC comment"} {
		user.SettingsSigningKey = value
		if err := Validate(user); err != nil {
			t.Errorf("settingsSigningKey %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"ssh-rsa AAAA", "SYNTHETIC not a key", `from="x" ` + key} {
		user.SettingsSigningKey = value
		if err := Validate(user); err == nil || !strings.Contains(err.Error(), "settingsSigningKey") {
			t.Errorf("settingsSigningKey %q: %v", value, err)
		}
	}
}

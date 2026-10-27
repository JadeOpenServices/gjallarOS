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

func TestValidateTimezoneAndLocale(t *testing.T) {
	user, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"Europe/Berlin", "Etc/UTC", "UTC", "Etc/GMT+5", "Etc/GMT-14", "America/Argentina/Buenos_Aires", "America/Port-au-Prince", "EST5EDT"} {
		u := user
		u.Timezone = zone
		if err := Validate(u); err != nil {
			t.Errorf("timezone %q rejected: %v", zone, err)
		}
	}
	for _, zone := range []string{`"Etc/UTC"`, "Etc/../../etc/shadow", "/etc/shadow", "Europe/", "Europe Berlin", "Europe//Berlin", "../UTC", strings.Repeat("A", 65)} {
		u := user
		u.Timezone = zone
		if err := Validate(u); err == nil {
			t.Errorf("timezone %q accepted", zone)
		}
	}
	for _, locale := range []string{"en_US.UTF-8", "de_DE.UTF-8", "C.UTF-8", "C", "POSIX", "eo", "ast_ES.UTF-8", "sr_RS@latin", "ca_ES.UTF-8@valencia"} {
		u := user
		u.Locale = locale
		if err := Validate(u); err != nil {
			t.Errorf("locale %q rejected: %v", locale, err)
		}
	}
	for _, locale := range []string{`"en_US.UTF-8"`, "en_US.UTF-8/UTF-8", "en US", "../en_US", "EN_us"} {
		u := user
		u.Locale = locale
		if err := Validate(u); err == nil {
			t.Errorf("locale %q accepted", locale)
		}
	}
}

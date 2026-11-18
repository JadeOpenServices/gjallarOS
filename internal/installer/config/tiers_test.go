package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Every key of user.config.json needs a tier (ROADMAP §14). This test is
// the schema check that fails when a new setting ships without one.
func TestEveryKeyHasTier(t *testing.T) {
	tiers, err := Tiers()
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	defaults.Tailscale = &TailscaleIntent{}
	raw, err := json.Marshal(defaults)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for key := range keys {
		if _, ok := tiers[key]; !ok {
			t.Errorf("%s is written to user.config.json but has no tier", key)
		}
	}
}

func TestTierOf(t *testing.T) {
	for key, want := range map[string]Tier{
		"theme":            TierEveryday,
		"hostname":         TierSecurity,
		"username":         TierSecurity,
		"aiEndpoint":       TierSecurity,
		"nextcloudHost":    TierSecurity,
		"webApplications":  TierSecurity,
		"firefoxAddons":    TierSecurity,
		"tailscale":        TierSecurity,
		"secureBootEnable": TierCritical,
		"luksTpm2Pin":      TierCritical,
		"management":       TierCritical,
		"prebootLock":      TierCritical,
		"forceRedeploy":    TierInstaller,
		"runRebuild":       TierInstaller,
	} {
		got, err := TierOf(key)
		if err != nil {
			t.Errorf("TierOf(%q): %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("TierOf(%q) = %s, want %s", key, got, want)
		}
	}
	if _, err := TierOf("backgroundNormal"); err == nil {
		t.Error("TierOf accepted a key user.config.json does not have")
	}
}

func TestDeviceScoped(t *testing.T) {
	for key, want := range map[string]bool{
		"hostname":          true,
		"usbTrustTpmHandle": true,
		"tailscale":         true,
		"releaseChannel":    true,
		"theme":             false,
		"aiEndpoint":        false,
		"nextcloudHost":     false,
	} {
		got, err := DeviceScoped(key)
		if err != nil || got != want {
			t.Errorf("DeviceScoped(%q) = %t, %v; want %t", key, got, err, want)
		}
	}
	if _, err := DeviceScoped("backgroundNormal"); err == nil {
		t.Error("DeviceScoped accepted a key user.config.json does not have")
	}
}

// Critical keys guard the disk, boot and management of this device. A new
// one must say whether it may come from another device; until then it may
// not.
func TestCriticalKeysAreDeviceScoped(t *testing.T) {
	tiers, err := Tiers()
	if err != nil {
		t.Fatal(err)
	}
	for key, tier := range tiers {
		if tier != TierCritical {
			continue
		}
		if device, err := DeviceScoped(key); err != nil || !device {
			t.Errorf("%s is critical but not scope:\"device\"", key)
		}
	}
}

// Every installer-tier key is taken over, and nothing else (L65).
func TestWithInstallerKeysFrom(t *testing.T) {
	tiers, err := Tiers()
	if err != nil {
		t.Fatal(err)
	}
	var from, user User
	set := func(u *User) {
		v := reflect.ValueOf(u).Elem()
		for i := range v.NumField() {
			if v.Field(i).Kind() == reflect.Bool {
				v.Field(i).SetBool(true)
			}
		}
	}
	set(&user)
	got := WithInstallerKeysFrom(user, from)
	v, ut := reflect.ValueOf(got), reflect.TypeFor[User]()
	installer := 0
	for i := range ut.NumField() {
		f := ut.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tiers[key] != TierInstaller {
			if !reflect.DeepEqual(v.Field(i).Interface(), reflect.ValueOf(user).Field(i).Interface()) {
				t.Errorf("%s changed", key)
			}
			continue
		}
		installer++
		if f.Type.Kind() != reflect.Bool {
			t.Fatalf("installer key %s is not a bool; extend this test", key)
		}
		if v.Field(i).Bool() {
			t.Errorf("installer key %s kept", key)
		}
	}
	if installer == 0 {
		t.Error("no installer keys")
	}
}

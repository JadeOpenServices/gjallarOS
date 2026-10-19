package config

import (
	"encoding/json"
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
		"jodsEndpoint":     TierCritical,
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

package app

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func TestAppChoicesNewInstallTakesCatalogueDefaults(t *testing.T) {
	u, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := appChoices(nil, &u); err != nil {
		t.Fatal(err)
	}
	want, err := apps.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(u.Apps, want) {
		t.Errorf("apps = %v, want the catalogue defaults %v", u.Apps, want)
	}
	if u.AIEndpoint != "" || u.AIRemoteModel != "" || u.AIRemoteContextTokens != 0 || u.AIAgentMode != "" || u.OverrideModelWith != "" {
		t.Errorf("new install has AI settings: %q %q %d %q %q", u.AIEndpoint, u.AIRemoteModel, u.AIRemoteContextTokens, u.AIAgentMode, u.OverrideModelWith)
	}
	if u.ThunderbirdSmartCard || u.NextcloudEnable || u.NextcloudHost != "" || len(u.WebApplications) != 0 {
		t.Errorf("new install has app settings: smartcard=%v nextcloud=%v %q web=%v", u.ThunderbirdSmartCard, u.NextcloudEnable, u.NextcloudHost, u.WebApplications)
	}
	if got := apps.ActiveFirefoxAddons(u.FirefoxAddons); !maps.Equal(got, map[string]string{"ublock-origin": apps.AddonLocked}) {
		t.Errorf("active add-ons = %v, want only uBlock Origin locked", got)
	}
	if u.FirefoxStoreAddons == nil || len(u.FirefoxStoreAddons) != 0 {
		t.Errorf("store add-ons = %#v, want an empty list", u.FirefoxStoreAddons)
	}
	if u.HasApp("tailscale") {
		empty := &config.TailscaleIntent{HomeSubnets: []string{}, TrustedWifis: []string{}, SiteRouterTargets: []string{}}
		if !reflect.DeepEqual(u.Tailscale, empty) {
			t.Errorf("tailscale = %#v, want the empty intent", u.Tailscale)
		}
	} else if u.Tailscale != nil {
		t.Errorf("tailscale intent %#v without the app", u.Tailscale)
	}
}

func TestAppChoicesRerunKeepsRunningChoices(t *testing.T) {
	running := runningFixture(t)
	running.FirefoxAddons = apps.CompleteFirefoxAddons(map[string]apps.FirefoxCollectionChoice{
		"privacy": {Enable: true, Addons: map[string]string{"ublock-origin": apps.AddonOn, "clearurls": apps.AddonLocked}},
	})
	// SYNTHETIC store pin: made-up file number and hash.
	running.FirefoxStoreAddons = []apps.FirefoxStoreAddon{{
		ID: "addon@example.org", Name: "Example", Version: "1", Mode: apps.AddonOn,
		URL:    "https://addons.mozilla.org/firefox/downloads/file/4000001/example.xpi",
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Permissions: []string{"storage"},
	}}
	running.WebApplications = []config.WebApplicationIntent{{ID: config.WebApplicationPlane, Endpoint: "https://plane.example.test"}}
	u, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := appChoices(&running, &u); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"Apps", "Tailscale", "ThunderbirdSmartCard", "AIEndpoint", "AIRemoteModel", "AIRemoteContextTokens",
		"AIAgentMode", "OverrideModelWith", "WebApplications", "NextcloudEnable", "NextcloudHost",
		"FirefoxAddons", "FirefoxStoreAddons",
	} {
		got := reflect.ValueOf(u).FieldByName(field).Interface()
		want := reflect.ValueOf(running).FieldByName(field).Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want the running %#v", field, got, want)
		}
	}
	if u.Tailscale == running.Tailscale {
		t.Error("the rerun shares the running Tailscale intent instead of copying it")
	}
}

func TestAppChoicesDropSettingsOfRemovedApps(t *testing.T) {
	running := runningFixture(t)
	running.Apps = slices.DeleteFunc(slices.Clone(running.Apps), func(id string) bool {
		return id == "ai" || id == "tailscale" || id == "thunderbird"
	})
	u, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := appChoices(&running, &u); err != nil {
		t.Fatal(err)
	}
	if u.Tailscale != nil || u.ThunderbirdSmartCard || u.AIEndpoint != "" || u.AIAgentMode != "" {
		t.Errorf("settings of removed apps kept: tailscale=%#v smartcard=%v ai=%q %q", u.Tailscale, u.ThunderbirdSmartCard, u.AIEndpoint, u.AIAgentMode)
	}
}

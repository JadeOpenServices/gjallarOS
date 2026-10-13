package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

// SYNTHETIC pin: made-up file number and hash.
func fakePin(_ context.Context, id, mode string) (apps.FirefoxStoreAddon, error) {
	if id == "missing@example.org" {
		return apps.FirefoxStoreAddon{}, errors.New("addons.mozilla.org has no add-on with the ID missing@example.org")
	}
	return apps.FirefoxStoreAddon{
		ID:          id,
		Name:        "Example",
		Version:     "1.0",
		URL:         "https://addons.mozilla.org/firefox/downloads/file/4000001/example-1.0.xpi",
		SHA256:      strings.Repeat("0f", 32),
		Permissions: []string{"storage"},
		Mode:        mode,
	}, nil
}

func collectFirefoxAddonAnswers(t *testing.T, user config.User, answers ...string) (config.User, string) {
	t.Helper()
	old := pinStoreAddon
	pinStoreAddon = fakePin
	t.Cleanup(func() { pinStoreAddon = old })
	var output bytes.Buffer
	ui := prompt.New(strings.NewReader(strings.Join(answers, "\n")+"\n"), &output)
	if err := collectFirefoxAddons(context.Background(), ui, &user); err != nil {
		t.Fatal(err)
	}
	if err := apps.ValidateFirefoxAddons(user.FirefoxAddons, user.FirefoxStoreAddons); err != nil {
		t.Fatal(err)
	}
	return user, output.String()
}

func storeIDs(store []apps.FirefoxStoreAddon) string {
	ids := []string{}
	for _, a := range store {
		ids = append(ids, a.ID+"="+a.Mode)
	}
	return strings.Join(ids, " ")
}

func TestCollectFirefoxAddons(t *testing.T) {
	got, out := collectFirefoxAddonAnswers(t, config.User{Browsers: []string{"firefox"}},
		"1,2,3", // privacy, password managers, comfort
		"2",     // privacy: containers only, uBlock off
		"1",     // passbolt
		"none",
		"passbolt@passbolt.com", // offered above
		"missing@example.org",   // not on the store
		"addon@example.org",
		"y",
	)
	for _, msg := range []string{"is offered as firefoxAddons.passwords.addons.passbolt", "no add-on with the ID missing@example.org", "It may use: storage"} {
		if !strings.Contains(out, msg) {
			t.Fatalf("missing %q:\n%s", msg, out)
		}
	}
	want := map[string]string{"multi-account-containers": apps.AddonOn, "passbolt": apps.AddonOn}
	if active := apps.ActiveFirefoxAddons(got.FirefoxAddons); !maps.Equal(active, want) {
		t.Fatalf("active %v, want %v", active, want)
	}
	if got.FirefoxAddons["comfort"].Enable {
		t.Fatal("collection with nothing ticked turned on")
	}
	if ids := storeIDs(got.FirefoxStoreAddons); ids != "addon@example.org=on" {
		t.Fatalf("store add-ons %v", ids)
	}

	// Enter keeps everything; a locked add-on stays locked.
	got.FirefoxAddons["privacy"].Addons["multi-account-containers"] = apps.AddonLocked
	again, _ := collectFirefoxAddonAnswers(t, got, "", "", "", "", "")
	want["multi-account-containers"] = apps.AddonLocked
	if active := apps.ActiveFirefoxAddons(again.FirefoxAddons); !maps.Equal(active, want) {
		t.Fatalf("earlier answers not kept: %v", active)
	}
	if ids := storeIDs(again.FirefoxStoreAddons); ids != "addon@example.org=on" {
		t.Fatalf("store add-ons %v", ids)
	}

	// Unticking a collection keeps its choices for later.
	off, _ := collectFirefoxAddonAnswers(t, again, "none", "")
	if len(apps.ActiveFirefoxAddons(off.FirefoxAddons)) != 0 || off.FirefoxAddons["privacy"].Addons["multi-account-containers"] != apps.AddonLocked {
		t.Fatalf("collection off: %+v", off.FirefoxAddons)
	}
}

func TestCollectFirefoxAddonsDefaultsKeepUBlock(t *testing.T) {
	got, _ := collectFirefoxAddonAnswers(t, config.User{Browsers: []string{"firefox"}}, "", "", "")
	if active := apps.ActiveFirefoxAddons(got.FirefoxAddons); !maps.Equal(active, map[string]string{"ublock-origin": apps.AddonOn}) {
		t.Fatalf("defaults %v", active)
	}
}

func TestCollectFirefoxAddonsSkipsOtherBrowsers(t *testing.T) {
	user := config.User{Browsers: []string{"chromium"}, FirefoxStoreAddons: []apps.FirefoxStoreAddon{{ID: "addon@example.org"}}}
	got, out := collectFirefoxAddonAnswers(t, user)
	if out != "" || len(got.FirefoxStoreAddons) != 0 {
		t.Fatalf("asked or kept add-ons without Firefox: %q %v", out, got.FirefoxStoreAddons)
	}
}

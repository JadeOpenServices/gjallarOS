package apps

import (
	"maps"
	"strings"
	"testing"
)

func TestFirefoxCollections(t *testing.T) {
	ids := map[string]bool{}
	addonIDs := map[string]bool{}
	for _, c := range FirefoxCollections() {
		if c.ID == "" || c.Name == "" || c.Description == "" || len(c.Addons) == 0 {
			t.Errorf("collection %q: id, name, description and add-ons are required", c.ID)
		}
		for _, a := range c.Addons {
			if a.ID == "" || a.Name == "" || a.Description == "" {
				t.Errorf("%s.%q: id, name and description are required", c.ID, a.ID)
			}
			if ids[a.ID] || addonIDs[a.AddonID] {
				t.Errorf("%s.%s listed twice", c.ID, a.ID)
			}
			if !ValidStoreAddonID(a.AddonID) {
				t.Errorf("%s.%s: bad add-on ID %q", c.ID, a.ID, a.AddonID)
			}
			if a.DefaultMode != "" && a.DefaultMode != AddonOn && a.DefaultMode != AddonLocked {
				t.Errorf("%s.%s: bad default mode %q", c.ID, a.ID, a.DefaultMode)
			}
			ids[a.ID], addonIDs[a.AddonID] = true, true
		}
	}
	if !addonIDs[UBlockOriginID] {
		t.Error("uBlock Origin is not offered")
	}
}

func pin(id, mode string) FirefoxStoreAddon {
	return FirefoxStoreAddon{
		ID:          id,
		Name:        "Example",
		Version:     "1.2.3",
		URL:         "https://addons.mozilla.org/firefox/downloads/file/4000001/example-1.2.3.xpi",
		SHA256:      strings.Repeat("ab", 32),
		Permissions: []string{"storage", "<all_urls>"},
		Mode:        mode,
	}
}

func TestFirefoxAddonDefaults(t *testing.T) {
	defaults := FirefoxAddonDefaults()
	if err := ValidateFirefoxAddons(defaults, nil); err != nil {
		t.Fatal(err)
	}
	if got := ActiveFirefoxAddons(defaults); !maps.Equal(got, map[string]string{"ublock-origin": AddonOn}) {
		t.Fatalf("defaults install %v", got)
	}
	if !defaults["privacy"].Enable || defaults["passwords"].Enable {
		t.Fatalf("collection defaults %+v", defaults)
	}
}

func TestCompleteFirefoxAddons(t *testing.T) {
	// A collection the user set up keeps exactly their add-ons; one they
	// never saw gets its defaults.
	got := CompleteFirefoxAddons(map[string]FirefoxCollectionChoice{
		"privacy":   {Enable: true, Addons: map[string]string{"clearurls": AddonLocked}},
		"passwords": {Enable: true, Addons: map[string]string{"passbolt": AddonOn}},
	})
	if got["privacy"].Addons["ublock-origin"] != AddonOff || got["privacy"].Addons["clearurls"] != AddonLocked {
		t.Fatalf("privacy %+v", got["privacy"])
	}
	if got["passwords"].Addons["bitwarden"] != AddonOff || got["comfort"].Enable {
		t.Fatalf("completed %+v", got)
	}
	want := map[string]string{"clearurls": AddonLocked, "passbolt": AddonOn}
	if active := ActiveFirefoxAddons(got); !maps.Equal(active, want) {
		t.Fatalf("active %v, want %v", active, want)
	}
}

func TestActiveFirefoxAddonsNeedsCollection(t *testing.T) {
	off := FirefoxAddonDefaults()
	privacy := off["privacy"]
	privacy.Enable = false
	off["privacy"] = privacy
	if got := ActiveFirefoxAddons(off); len(got) != 0 {
		t.Fatalf("collection off still installs %v", got)
	}
}

func TestValidateFirefoxAddons(t *testing.T) {
	defaults := FirefoxAddonDefaults()
	good := []FirefoxStoreAddon{pin("{11111111-2222-3333-4444-555555555555}", AddonOn), pin("addon@example.org", AddonOff)}
	if err := ValidateFirefoxAddons(defaults, good); err != nil {
		t.Fatal(err)
	}
	if got := ActiveStoreAddons(good); len(got) != 1 || got[0].ID != good[0].ID {
		t.Fatalf("active store add-ons %v", got)
	}

	for name, bad := range map[string]map[string]FirefoxCollectionChoice{
		"unknown collection": {"games": {Enable: true}},
		"unknown add-on":     {"privacy": {Enable: true, Addons: map[string]string{"passbolt": AddonOn}}},
		"unknown mode":       {"privacy": {Enable: true, Addons: map[string]string{"ublock-origin": "yes"}}},
	} {
		if ValidateFirefoxAddons(bad, nil) == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	change := func(f func(*FirefoxStoreAddon)) []FirefoxStoreAddon {
		a := pin("addon@example.org", AddonOn)
		f(&a)
		return []FirefoxStoreAddon{a}
	}
	for name, store := range map[string][]FirefoxStoreAddon{
		"path id":         change(func(a *FirefoxStoreAddon) { a.ID = "../x@y" }),
		"slash id":        change(func(a *FirefoxStoreAddon) { a.ID = "x@y/z" }),
		"plain id":        change(func(a *FirefoxStoreAddon) { a.ID = "plain-name" }),
		"bad guid":        change(func(a *FirefoxStoreAddon) { a.ID = "{not-a-guid}" }),
		"empty id":        change(func(a *FirefoxStoreAddon) { a.ID = "" }),
		"catalogue id":    change(func(a *FirefoxStoreAddon) { a.ID = UBlockOriginID }),
		"catalogue id 2":  change(func(a *FirefoxStoreAddon) { a.ID = "passbolt@passbolt.com" }),
		"latest url":      change(func(a *FirefoxStoreAddon) { a.URL = "https://addons.mozilla.org/firefox/downloads/latest/x/latest.xpi" }),
		"other host":      change(func(a *FirefoxStoreAddon) { a.URL = "https://example.org/firefox/downloads/file/1/x.xpi" }),
		"http url":        change(func(a *FirefoxStoreAddon) { a.URL = "http://addons.mozilla.org/firefox/downloads/file/1/x.xpi" }),
		"quote in url":    change(func(a *FirefoxStoreAddon) { a.URL = `https://addons.mozilla.org/firefox/downloads/file/1/x".xpi` }),
		"short hash":      change(func(a *FirefoxStoreAddon) { a.SHA256 = "abc" }),
		"upper hash":      change(func(a *FirefoxStoreAddon) { a.SHA256 = strings.Repeat("AB", 32) }),
		"empty version":   change(func(a *FirefoxStoreAddon) { a.Version = "" }),
		"bad mode":        change(func(a *FirefoxStoreAddon) { a.Mode = "" }),
		"control in name": change(func(a *FirefoxStoreAddon) { a.Name = "a\nb" }),
		"empty permission": change(func(a *FirefoxStoreAddon) {
			a.Permissions = []string{""}
		}),
		"twice": {pin("a@b", AddonOn), pin("a@b", AddonOff)},
	} {
		if ValidateFirefoxAddons(defaults, store) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

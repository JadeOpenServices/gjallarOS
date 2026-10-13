package apps

import (
	"slices"
	"testing"
)

func TestFirefoxCollections(t *testing.T) {
	ids := map[string]bool{}
	addonIDs := map[string]bool{UBlockOriginID: true}
	for _, c := range FirefoxCollections() {
		if c.ID == "" || c.Name == "" || c.Description == "" || len(c.Addons) == 0 {
			t.Errorf("collection %q: id, name, description and add-ons are required", c.ID)
		}
		for _, a := range c.Addons {
			if a.ID == "" || a.ID == CollectionSwitch || a.Name == "" || a.Description == "" {
				t.Errorf("%s.%q: id, name and description are required", c.ID, a.ID)
			}
			if ids[a.ID] || addonIDs[a.AddonID] {
				t.Errorf("%s.%s listed twice", c.ID, a.ID)
			}
			if !storeAddonID.MatchString(a.AddonID) {
				t.Errorf("%s.%s: bad add-on ID %q", c.ID, a.ID, a.AddonID)
			}
			ids[a.ID], addonIDs[a.AddonID] = true, true
		}
	}
}

func TestValidateFirefoxAddons(t *testing.T) {
	defaults := FirefoxAddonDefaults()
	if err := ValidateFirefoxAddons(defaults, nil); err != nil {
		t.Fatal(err)
	}
	if got := SelectedFirefoxAddons(defaults); len(got) != 0 {
		t.Fatalf("defaults select %v", got)
	}

	on := CompleteFirefoxAddons(map[string]map[string]bool{
		"passwords": {CollectionSwitch: true, "passbolt": true},
		"comfort":   {"vimium": false},
	})
	if err := ValidateFirefoxAddons(on, []string{"{11111111-2222-3333-4444-555555555555}", "addon@example.org"}); err != nil {
		t.Fatal(err)
	}
	if got := SelectedFirefoxAddons(on); !slices.Equal(got, []string{"passbolt"}) {
		t.Fatalf("selected %v", got)
	}

	for name, bad := range map[string]map[string]map[string]bool{
		"add-on on, collection off": {"passwords": {CollectionSwitch: false, "passbolt": true}},
		"add-on without switch":     {"passwords": {"passbolt": true}},
		"unknown collection":        {"games": {CollectionSwitch: true}},
		"unknown add-on":            {"privacy": {CollectionSwitch: true, "passbolt": true}},
	} {
		if ValidateFirefoxAddons(bad, nil) == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	for _, store := range [][]string{
		{"https://addons.mozilla.org/firefox/downloads/latest/x/latest.xpi"},
		{"../x@y"},
		{"x@y/z"},
		{"plain-name"},
		{"{not-a-guid}"},
		{""},
		{"a@b", "a@b"},
		{UBlockOriginID},
		{"passbolt@passbolt.com"},
	} {
		if ValidateFirefoxAddons(defaults, store) == nil {
			t.Errorf("store list %q accepted", store)
		}
	}
}

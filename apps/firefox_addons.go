package apps

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

// The optional Firefox add-ons, grouped in collections. Each id is the
// add-on's attribute in the NUR firefox-addons set, addonId its ID on
// addons.mozilla.org. uBlock Origin is not listed: every Firefox has it.
//
//go:embed firefox-addons.json
var firefoxAddonsJSON []byte

// UBlockOriginID is the add-on every Firefox gets.
const UBlockOriginID = "uBlock0@raymondhill.net"

// FirefoxAddon is one optional add-on.
type FirefoxAddon struct {
	ID          string `json:"id"`
	AddonID     string `json:"addonId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// FirefoxCollection groups add-ons; its add-ons can only be on while the
// collection is.
type FirefoxCollection struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Addons      []FirefoxAddon `json:"addons"`
}

// CollectionSwitch is the key that turns a collection on in user.config.json.
const CollectionSwitch = "enable"

// firefoxCollections is parsed once; the file is part of the build, so a
// broken one stops every program that uses this package (and its tests).
var firefoxCollections = func() []FirefoxCollection {
	var out []FirefoxCollection
	if err := json.Unmarshal(firefoxAddonsJSON, &out); err != nil {
		panic(fmt.Sprintf("apps/firefox-addons.json: %v", err))
	}
	return out
}()

// FirefoxCollections returns the collections in display order.
func FirefoxCollections() []FirefoxCollection {
	out := slices.Clone(firefoxCollections)
	for i := range out {
		out[i].Addons = slices.Clone(out[i].Addons)
	}
	return out
}

// FirefoxAddonDefaults has every collection and add-on, all off, so
// user.config.json shows each switch.
func FirefoxAddonDefaults() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, c := range firefoxCollections {
		out[c.ID] = map[string]bool{CollectionSwitch: false}
		for _, a := range c.Addons {
			out[c.ID][a.ID] = false
		}
	}
	return out
}

// CompleteFirefoxAddons adds missing collections and add-ons as off.
func CompleteFirefoxAddons(selection map[string]map[string]bool) map[string]map[string]bool {
	out := FirefoxAddonDefaults()
	for collection, values := range selection {
		if out[collection] == nil {
			out[collection] = map[string]bool{}
		}
		for key, on := range values {
			out[collection][key] = on
		}
	}
	return out
}

// storeAddonID is the add-on ID format addons.mozilla.org accepts: a GUID
// in braces or an email-like name.
var storeAddonID = regexp.MustCompile(`^(\{[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}|[a-zA-Z0-9._-]*@[a-zA-Z0-9._-]+)$`)

// ValidateFirefoxAddons rejects unknown collections or add-ons, an add-on
// that is on while its collection is off, and store add-on IDs that are
// malformed, repeated, or already offered in a collection.
func ValidateFirefoxAddons(selection map[string]map[string]bool, store []string) error {
	collections := firefoxCollections
	known := map[string]string{UBlockOriginID: "is always installed"}
	for name, values := range selection {
		i := slices.IndexFunc(collections, func(c FirefoxCollection) bool { return c.ID == name })
		if i < 0 {
			return fmt.Errorf("firefoxAddons: unknown collection %q", name)
		}
		for key, on := range values {
			if key == CollectionSwitch {
				continue
			}
			if !slices.ContainsFunc(collections[i].Addons, func(a FirefoxAddon) bool { return a.ID == key }) {
				return fmt.Errorf("firefoxAddons.%s: unknown add-on %q", name, key)
			}
			if on && !values[CollectionSwitch] {
				return fmt.Errorf("firefoxAddons.%s.%s is on, but the %s collection is off; turn on %q there or turn the add-on off", name, key, name, CollectionSwitch)
			}
		}
	}
	for _, c := range collections {
		for _, a := range c.Addons {
			known[a.AddonID] = fmt.Sprintf("is offered as firefoxAddons.%s.%s", c.ID, a.ID)
		}
	}
	seen := map[string]bool{}
	for _, id := range store {
		if len(id) > 80 || !storeAddonID.MatchString(id) {
			return fmt.Errorf("firefoxStoreAddons: %q is not an add-on ID from addons.mozilla.org", id)
		}
		if seen[id] {
			return fmt.Errorf("firefoxStoreAddons: %q listed twice", id)
		}
		seen[id] = true
		if why, ok := known[id]; ok {
			return fmt.Errorf("firefoxStoreAddons: %q %s", id, why)
		}
	}
	return nil
}

// SelectedFirefoxAddons returns the ids of the add-ons that are on, in
// display order.
func SelectedFirefoxAddons(selection map[string]map[string]bool) []string {
	ids := []string{}
	for _, c := range firefoxCollections {
		if !selection[c.ID][CollectionSwitch] {
			continue
		}
		for _, a := range c.Addons {
			if selection[c.ID][a.ID] {
				ids = append(ids, a.ID)
			}
		}
	}
	return ids
}

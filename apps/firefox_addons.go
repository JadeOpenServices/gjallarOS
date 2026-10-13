package apps

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"unicode"
)

// The Firefox add-ons GjallarOS offers, grouped in collections. Each id is
// the add-on's attribute in the NUR firefox-addons set, addonId its ID on
// addons.mozilla.org.
//
//go:embed firefox-addons.json
var firefoxAddonsJSON []byte

// UBlockOriginID is the add-on ID of uBlock Origin, which is on by default.
const UBlockOriginID = "uBlock0@raymondhill.net"

// Add-on modes. "on" installs the add-on and lets the user turn it off in
// Firefox; "locked" keeps it on; "off" leaves it out, and an add-on that
// GjallarOS installed before is removed again.
const (
	AddonOff    = "off"
	AddonOn     = "on"
	AddonLocked = "locked"
)

var addonModes = []string{AddonOff, AddonOn, AddonLocked}

// AddonModes returns the modes in the order the installer offers them.
func AddonModes() []string { return slices.Clone(addonModes) }

// FirefoxAddon is one add-on in a collection.
type FirefoxAddon struct {
	ID          string `json:"id"`
	AddonID     string `json:"addonId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// DefaultMode is the mode on a new install; empty means off.
	DefaultMode string `json:"defaultMode,omitempty"`
}

// FirefoxCollection groups add-ons; its add-ons are only installed while
// the collection is on.
type FirefoxCollection struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Addons      []FirefoxAddon `json:"addons"`
}

// FirefoxCollectionChoice is one collection in user.config.json.
type FirefoxCollectionChoice struct {
	Enable bool              `json:"enable"`
	Addons map[string]string `json:"addons"`
}

// FirefoxStoreAddon is an add-on from addons.mozilla.org, pinned to one
// reviewed file. Name and Permissions are shown when the pin changes; the
// build only uses ID, URL, SHA256 and Mode.
type FirefoxStoreAddon struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	URL         string   `json:"url"`
	SHA256      string   `json:"sha256"`
	Permissions []string `json:"permissions"`
	Mode        string   `json:"mode"`
}

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

func defaultMode(a FirefoxAddon) string {
	if a.DefaultMode == "" {
		return AddonOff
	}
	return a.DefaultMode
}

// FirefoxAddonDefaults lists every collection and add-on with its default,
// so user.config.json shows each switch. A collection starts on when one of
// its add-ons does.
func FirefoxAddonDefaults() map[string]FirefoxCollectionChoice {
	out := map[string]FirefoxCollectionChoice{}
	for _, c := range firefoxCollections {
		choice := FirefoxCollectionChoice{Addons: map[string]string{}}
		for _, a := range c.Addons {
			choice.Addons[a.ID] = defaultMode(a)
			choice.Enable = choice.Enable || defaultMode(a) != AddonOff
		}
		out[c.ID] = choice
	}
	return out
}

// CompleteFirefoxAddons adds missing collections with their defaults and
// missing add-ons of a listed collection as off: a collection the user has
// set up keeps exactly the add-ons they chose.
func CompleteFirefoxAddons(selection map[string]FirefoxCollectionChoice) map[string]FirefoxCollectionChoice {
	out := FirefoxAddonDefaults()
	for name, choice := range selection {
		complete := FirefoxCollectionChoice{Enable: choice.Enable, Addons: map[string]string{}}
		if defaults, ok := out[name]; ok {
			for id := range defaults.Addons {
				complete.Addons[id] = AddonOff
			}
		}
		for id, mode := range choice.Addons {
			complete.Addons[id] = mode
		}
		out[name] = complete
	}
	return out
}

// storeAddonID is the add-on ID format addons.mozilla.org accepts: a GUID
// in braces or an email-like name.
var storeAddonID = regexp.MustCompile(`^(\{[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}|[a-zA-Z0-9._-]*@[a-zA-Z0-9._-]+)$`)

// ValidStoreAddonID reports whether id has the add-on ID format.
func ValidStoreAddonID(id string) bool {
	return len(id) <= 80 && storeAddonID.MatchString(id)
}

var (
	storeAddonVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]{0,63}$`)
	storeAddonURL     = regexp.MustCompile(`^https://addons\.mozilla\.org/firefox/downloads/file/[0-9]{1,12}/[A-Za-z0-9._%+-]{1,200}\.xpi$`)
	storeAddonSHA256  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidateStoreAddon checks one pinned store add-on on its own.
func ValidateStoreAddon(a FirefoxStoreAddon) error {
	if !ValidStoreAddonID(a.ID) {
		return fmt.Errorf("%q is not an add-on ID from addons.mozilla.org", a.ID)
	}
	if !slices.Contains(addonModes, a.Mode) {
		return fmt.Errorf("%s: mode %q is not one of %v", a.ID, a.Mode, addonModes)
	}
	if !storeAddonVersion.MatchString(a.Version) {
		return fmt.Errorf("%s: version %q is not an add-on version", a.ID, a.Version)
	}
	if !storeAddonURL.MatchString(a.URL) {
		return fmt.Errorf("%s: url must be a file on https://addons.mozilla.org/firefox/downloads/file/", a.ID)
	}
	if !storeAddonSHA256.MatchString(a.SHA256) {
		return fmt.Errorf("%s: sha256 must be 64 lowercase hex digits", a.ID)
	}
	if !plainText(a.Name, 100) {
		return fmt.Errorf("%s: name is too long or holds control characters", a.ID)
	}
	if len(a.Permissions) > 200 {
		return fmt.Errorf("%s: too many permissions", a.ID)
	}
	for _, p := range a.Permissions {
		if p == "" || !plainText(p, 200) {
			return fmt.Errorf("%s: permission %q is empty, too long or holds control characters", a.ID, p)
		}
	}
	return nil
}

func plainText(s string, max int) bool {
	return len(s) <= max && !slices.ContainsFunc([]rune(s), unicode.IsControl)
}

// ValidateFirefoxAddons rejects unknown collections, add-ons or modes, and
// store add-ons that are malformed, repeated, or already in a collection.
func ValidateFirefoxAddons(selection map[string]FirefoxCollectionChoice, store []FirefoxStoreAddon) error {
	collections := firefoxCollections
	for name, choice := range selection {
		i := slices.IndexFunc(collections, func(c FirefoxCollection) bool { return c.ID == name })
		if i < 0 {
			return fmt.Errorf("firefoxAddons: unknown collection %q", name)
		}
		for id, mode := range choice.Addons {
			if !slices.ContainsFunc(collections[i].Addons, func(a FirefoxAddon) bool { return a.ID == id }) {
				return fmt.Errorf("firefoxAddons.%s: unknown add-on %q", name, id)
			}
			if !slices.Contains(addonModes, mode) {
				return fmt.Errorf("firefoxAddons.%s.addons.%s: mode %q is not one of %v", name, id, mode, addonModes)
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range store {
		if err := ValidateStoreAddon(a); err != nil {
			return fmt.Errorf("firefoxStoreAddons: %w", err)
		}
		if err := CheckStoreAddonID(a.ID); err != nil {
			return fmt.Errorf("firefoxStoreAddons: %w", err)
		}
		if seen[a.ID] {
			return fmt.Errorf("firefoxStoreAddons: %q listed twice", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

// CheckStoreAddonID rejects an ID that is malformed or that a collection
// already offers.
func CheckStoreAddonID(id string) error {
	if !ValidStoreAddonID(id) {
		return fmt.Errorf("%q is not an add-on ID from addons.mozilla.org", id)
	}
	for _, c := range firefoxCollections {
		for _, a := range c.Addons {
			if a.AddonID == id {
				return fmt.Errorf("%q is offered as firefoxAddons.%s.addons.%s", id, c.ID, a.ID)
			}
		}
	}
	return nil
}

// ActiveFirefoxAddons returns the mode of every add-on that is installed:
// its collection is on and the add-on is not off.
func ActiveFirefoxAddons(selection map[string]FirefoxCollectionChoice) map[string]string {
	out := map[string]string{}
	for _, c := range firefoxCollections {
		choice := selection[c.ID]
		if !choice.Enable {
			continue
		}
		for _, a := range c.Addons {
			if mode := choice.Addons[a.ID]; mode == AddonOn || mode == AddonLocked {
				out[a.ID] = mode
			}
		}
	}
	return out
}

// ActiveStoreAddons returns the store add-ons that are not off.
func ActiveStoreAddons(store []FirefoxStoreAddon) []FirefoxStoreAddon {
	out := []FirefoxStoreAddon{}
	for _, a := range store {
		if a.Mode == AddonOn || a.Mode == AddonLocked {
			out = append(out, a)
		}
	}
	return out
}

// Package firefoxstore pins add-ons from addons.mozilla.org to one file.
//
// The build installs exactly the pinned file and Firefox never updates it on
// its own, so a new version (and any new permission it asks for) only arrives
// when someone pins it again and sees the change.
package firefoxstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/JadeOpenServices/gjallarOS/apps"
)

// API is the addons.mozilla.org API; tests point it at a local server.
var API = "https://addons.mozilla.org/api/v5"

type addonResponse struct {
	GUID       string          `json:"guid"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	IsDisabled bool            `json:"is_disabled"`
	Name       json.RawMessage `json:"name"`
	Current    struct {
		Version string `json:"version"`
		File    struct {
			URL                 string   `json:"url"`
			Hash                string   `json:"hash"`
			Status              string   `json:"status"`
			Permissions         []string `json:"permissions"`
			HostPermissions     []string `json:"host_permissions"`
			OptionalPermissions []string `json:"optional_permissions"`
		} `json:"file"`
	} `json:"current_version"`
}

// Pin looks up the current public version of the add-on with this ID and
// returns it as a store add-on in the given mode.
func Pin(ctx context.Context, id, mode string) (apps.FirefoxStoreAddon, error) {
	if !apps.ValidStoreAddonID(id) {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("%q is not an add-on ID from addons.mozilla.org", id)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/addons/addon/"+url.PathEscape(id)+"/?lang=en-US", nil)
	if err != nil {
		return apps.FirefoxStoreAddon{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("ask addons.mozilla.org about %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("addons.mozilla.org has no add-on with the ID %s", id)
	}
	if resp.StatusCode != http.StatusOK {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("ask addons.mozilla.org about %s: %s", id, resp.Status)
	}
	var r addonResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("read the addons.mozilla.org answer for %s: %w", id, err)
	}
	return fromResponse(id, mode, r)
}

func fromResponse(id, mode string, r addonResponse) (apps.FirefoxStoreAddon, error) {
	// The API also finds add-ons by slug or number; only the exact ID counts,
	// because Firefox installs the file under the ID it declares.
	if r.GUID != id {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("addons.mozilla.org answered for %q, not %q; use the add-on ID from about:support", r.GUID, id)
	}
	if r.Type != "extension" {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("%s is a %s, not an extension", id, r.Type)
	}
	file := r.Current.File
	if r.Status != "public" || r.IsDisabled || file.Status != "public" {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("%s is not publicly listed on addons.mozilla.org", id)
	}
	sha, ok := strings.CutPrefix(file.Hash, "sha256:")
	if !ok {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("%s: addons.mozilla.org gave no sha256 for the file", id)
	}
	permissions := slices.Concat(file.Permissions, file.HostPermissions)
	for _, p := range file.OptionalPermissions {
		permissions = append(permissions, "optional "+p)
	}
	slices.Sort(permissions)
	a := apps.FirefoxStoreAddon{
		ID:          id,
		Name:        name(r.Name),
		Version:     r.Current.Version,
		URL:         file.URL,
		SHA256:      sha,
		Permissions: slices.Compact(permissions),
		Mode:        mode,
	}
	if err := apps.ValidateStoreAddon(a); err != nil {
		return apps.FirefoxStoreAddon{}, fmt.Errorf("addons.mozilla.org answer: %w", err)
	}
	return a, nil
}

// name reads a translated name: a plain string, or one entry per language.
func name(raw json.RawMessage) string {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return clip(plain)
	}
	var byLang map[string]string
	if json.Unmarshal(raw, &byLang) != nil {
		return ""
	}
	if n, ok := byLang["en-US"]; ok {
		return clip(n)
	}
	if langs := slices.Sorted(maps.Keys(byLang)); len(langs) > 0 {
		return clip(byLang[langs[0]])
	}
	return ""
}

func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	for len(s) > 100 {
		r := []rune(s)
		s = string(r[:len(r)-1])
	}
	return s
}

// Change describes what a new pin changes, for a person to confirm.
type Change struct {
	From, To         string
	Added, Removed   []string
	ModeFrom, ModeTo string
	SameFile         bool
}

// Compare lists the version and permission changes from old to new.
func Compare(old, new apps.FirefoxStoreAddon) Change {
	c := Change{From: old.Version, To: new.Version, ModeFrom: old.Mode, ModeTo: new.Mode, SameFile: old.URL == new.URL && old.SHA256 == new.SHA256}
	for _, p := range new.Permissions {
		if !slices.Contains(old.Permissions, p) {
			c.Added = append(c.Added, p)
		}
	}
	for _, p := range old.Permissions {
		if !slices.Contains(new.Permissions, p) {
			c.Removed = append(c.Removed, p)
		}
	}
	return c
}

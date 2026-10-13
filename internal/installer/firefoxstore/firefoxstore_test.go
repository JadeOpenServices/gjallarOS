package firefoxstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/apps"
)

// SYNTHETIC: shaped like an addons.mozilla.org v5 answer; the add-on, file
// number and hash are made up.
const answer = `{
  "guid": "addon@example.org",
  "type": "extension",
  "status": "public",
  "is_disabled": false,
  "name": {"en-US": "Example Add-on"},
  "current_version": {
    "version": "2.0.1",
    "file": {
      "url": "https://addons.mozilla.org/firefox/downloads/file/4000002/example-2.0.1.xpi",
      "hash": "sha256:` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `",
      "status": "public",
      "permissions": ["storage", "tabs"],
      "host_permissions": ["<all_urls>"],
      "optional_permissions": ["downloads"]
    }
  }
}`

func serve(t *testing.T, body string, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/addons/addon/addon@example.org/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := API
	API = srv.URL
	t.Cleanup(func() { API = old })
}

func TestPin(t *testing.T) {
	serve(t, answer, http.StatusOK)
	got, err := Pin(context.Background(), "addon@example.org", apps.AddonOn)
	if err != nil {
		t.Fatal(err)
	}
	want := apps.FirefoxStoreAddon{
		ID:          "addon@example.org",
		Name:        "Example Add-on",
		Version:     "2.0.1",
		URL:         "https://addons.mozilla.org/firefox/downloads/file/4000002/example-2.0.1.xpi",
		SHA256:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Permissions: []string{"<all_urls>", "optional downloads", "storage", "tabs"},
		Mode:        apps.AddonOn,
	}
	if got.ID != want.ID || got.Name != want.Name || got.Version != want.Version || got.URL != want.URL ||
		got.SHA256 != want.SHA256 || got.Mode != want.Mode || !slices.Equal(got.Permissions, want.Permissions) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestPinRejects(t *testing.T) {
	for name, body := range map[string]string{
		"other guid": strings.Replace(answer, `"guid": "addon@example.org"`, `"guid": "other@example.org"`, 1),
		"theme":      strings.Replace(answer, `"type": "extension"`, `"type": "statictheme"`, 1),
		"disabled":   strings.Replace(answer, `"is_disabled": false`, `"is_disabled": true`, 1),
		"not public": strings.Replace(answer, `"status": "public",
  "is_disabled"`, `"status": "unreviewed",
  "is_disabled"`, 1),
		"other host":  strings.Replace(answer, "https://addons.mozilla.org/", "https://example.org/", 1),
		"no sha256":   strings.Replace(answer, `"hash": "sha256:`, `"hash": "sha512:`, 1),
		"broken json": answer[:40],
	} {
		t.Run(name, func(t *testing.T) {
			serve(t, body, http.StatusOK)
			if _, err := Pin(context.Background(), "addon@example.org", apps.AddonOn); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	serve(t, answer, http.StatusNotFound)
	if _, err := Pin(context.Background(), "addon@example.org", apps.AddonOn); err == nil || !strings.Contains(err.Error(), "no add-on") {
		t.Fatalf("404: %v", err)
	}
	if _, err := Pin(context.Background(), "../addon@example.org", apps.AddonOn); err == nil {
		t.Fatal("bad ID accepted")
	}
}

func TestCompare(t *testing.T) {
	old := apps.FirefoxStoreAddon{Version: "1", URL: "a", SHA256: "x", Permissions: []string{"storage", "tabs"}, Mode: apps.AddonOn}
	new := apps.FirefoxStoreAddon{Version: "2", URL: "b", SHA256: "y", Permissions: []string{"<all_urls>", "storage"}, Mode: apps.AddonOn}
	c := Compare(old, new)
	if c.SameFile || !slices.Equal(c.Added, []string{"<all_urls>"}) || !slices.Equal(c.Removed, []string{"tabs"}) {
		t.Fatalf("%+v", c)
	}
	if !Compare(old, old).SameFile {
		t.Fatal("same pin differs")
	}
}

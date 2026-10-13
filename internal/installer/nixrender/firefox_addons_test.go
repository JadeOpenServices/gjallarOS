package nixrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func TestSyncUserIntentAddsFirefoxAddonsToOldState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.nix")
	before := `{pkgs, inputs, ...}:
rec {
    hostname = "old";
    theme = "old-theme";
    themeDetails = import (./. + "/../themes/${theme}.nix") {inherit pkgs;};
}
`
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	addons := apps.CompleteFirefoxAddons(map[string]apps.FirefoxCollectionChoice{
		"privacy":   {Enable: false, Addons: map[string]string{"ublock-origin": apps.AddonOn}},
		"passwords": {Enable: true, Addons: map[string]string{"passbolt": apps.AddonLocked}},
		"comfort":   {Enable: false, Addons: map[string]string{"vimium": apps.AddonOn}},
	})
	store := apps.FirefoxStoreAddon{
		ID:          "addon@example.org",
		Name:        "Example",
		Version:     "1.0",
		URL:         "https://addons.mozilla.org/firefox/downloads/file/4000001/example-1.0.xpi",
		SHA256:      strings.Repeat("0f", 32),
		Permissions: []string{"storage"},
		Mode:        apps.AddonOn,
	}
	off := store
	off.ID, off.Mode = "off@example.org", apps.AddonOff
	user := config.User{
		Hostname:           "gjallarOS",
		Theme:              "noctalia",
		AIAgentMode:        "workspace",
		JODSEnrollmentMode: "manual",
		FirefoxAddons:      addons,
		FirefoxStoreAddons: []apps.FirefoxStoreAddon{store, off},
	}
	if err := SyncUserIntent(path, user); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`firefoxAddons = { "passbolt" = "locked"; };`,
		`firefoxStoreAddons = [ { id = "addon@example.org"; url = "https://addons.mozilla.org/firefox/downloads/file/4000001/example-1.0.xpi"; sha256 = "` + store.SHA256 + `"; mode = "on"; } ];`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "off@example.org") {
		t.Fatalf("store add-on that is off rendered:\n%s", data)
	}
}

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
	addons := apps.FirefoxAddonDefaults()
	addons["passwords"][apps.CollectionSwitch] = true
	addons["passwords"]["passbolt"] = true
	addons["comfort"]["vimium"] = true
	user := config.User{
		Hostname:           "gjallarOS",
		Theme:              "noctalia",
		AIAgentMode:        "workspace",
		JODSEnrollmentMode: "manual",
		FirefoxAddons:      addons,
		FirefoxStoreAddons: []string{"addon@example.org"},
	}
	if err := SyncUserIntent(path, user); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`firefoxAddons = [ "passbolt" ];`, `firefoxStoreAddons = [ "addon@example.org" ];`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s:\n%s", want, data)
		}
	}
}

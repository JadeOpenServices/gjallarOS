package nixrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func TestTailscaleRender(t *testing.T) {
	if got := Tailscale(nil); got != "null" {
		t.Fatalf("nil intent = %s", got)
	}
	got := Tailscale(&config.TailscaleIntent{
		HomeSubnets:       []string{"192.168.8.0/24"},
		TrustedWifis:      []string{"bakasifu-5Ghz", "${evil}"},
		ExitNode:          "home-router",
		WifiExitNodes:     map[string]string{"${evil}": "auto"},
		SiteRouterTrust:   true,
		SiteRouterTargets: []string{"192.168.8.1:53"},
	})
	want := `{ homeSubnets = [ "192.168.8.0/24" ]; trustedWifis = [ "bakasifu-5Ghz" "\${evil}" ]; exitNode = "home-router"; wifiExitNodes = { "\${evil}" = "auto"; }; routerHome = false; siteRouterTrust = true; siteRouterTargets = [ "192.168.8.1:53" ]; }`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSyncUserIntentAddsTailscaleToOldState(t *testing.T) {
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
	user := config.User{
		Hostname:           "gjallarOS",
		Theme:              "noctalia",
		AIAgentMode:        "workspace",
		JODSEnrollmentMode: "manual",
		Tailscale:          &config.TailscaleIntent{ExitNode: "home-router"},
		Apps:               []string{"tailscale"},
	}
	if err := SyncUserIntent(path, user); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `tailscale = { homeSubnets = [  ]; trustedWifis = [  ]; exitNode = "home-router";`) {
		t.Fatalf("tailscale not synced:\n%s", data)
	}
	if !strings.Contains(string(data), `apps = [ "tailscale" ];`) {
		t.Fatalf("apps not synced:\n%s", data)
	}
}

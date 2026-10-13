package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func collectFirefoxAddonAnswers(t *testing.T, user config.User, answers ...string) (config.User, string) {
	t.Helper()
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

func TestCollectFirefoxAddons(t *testing.T) {
	got, out := collectFirefoxAddonAnswers(t, config.User{Browsers: []string{"firefox"}},
		"2,3", // password managers, comfort
		"1",   // passbolt
		"none",
		"passbolt@passbolt.com", // offered above
		"addon@example.org, {11111111-2222-3333-4444-555555555555}",
	)
	if !strings.Contains(out, "is offered as firefoxAddons.passwords.passbolt") {
		t.Fatalf("missing validation message:\n%s", out)
	}
	if selected := apps.SelectedFirefoxAddons(got.FirefoxAddons); !slices.Equal(selected, []string{"passbolt"}) {
		t.Fatalf("selected %v", selected)
	}
	if got.FirefoxAddons["comfort"][apps.CollectionSwitch] {
		t.Fatal("collection with nothing ticked turned on")
	}
	if !slices.Equal(got.FirefoxStoreAddons, []string{"addon@example.org", "{11111111-2222-3333-4444-555555555555}"}) {
		t.Fatalf("store add-ons %v", got.FirefoxStoreAddons)
	}

	again, _ := collectFirefoxAddonAnswers(t, got, "", "", "")
	if !slices.Equal(apps.SelectedFirefoxAddons(again.FirefoxAddons), []string{"passbolt"}) ||
		!slices.Equal(again.FirefoxStoreAddons, got.FirefoxStoreAddons) {
		t.Fatalf("earlier answers not kept: %v %v", again.FirefoxAddons, again.FirefoxStoreAddons)
	}
}

func TestCollectFirefoxAddonsSkipsOtherBrowsers(t *testing.T) {
	user := config.User{Browsers: []string{"chromium"}, FirefoxAddons: map[string]map[string]bool{"passwords": {apps.CollectionSwitch: true, "passbolt": true}}, FirefoxStoreAddons: []string{"addon@example.org"}}
	got, out := collectFirefoxAddonAnswers(t, user)
	if out != "" || len(apps.SelectedFirefoxAddons(got.FirefoxAddons)) != 0 || len(got.FirefoxStoreAddons) != 0 {
		t.Fatalf("asked or kept add-ons without Firefox: %q %v %v", out, got.FirefoxAddons, got.FirefoxStoreAddons)
	}
}

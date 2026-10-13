package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func addonsConfig(t *testing.T) string {
	t.Helper()
	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "user.config.json")
	if err := config.WriteAtomic(path, user); err != nil {
		t.Fatal(err)
	}
	return path
}

// SYNTHETIC pins: made-up file numbers and hashes; version 2 asks for one
// more permission.
func fakeAddonsPin(t *testing.T, version string) {
	t.Helper()
	old := addonsPin
	addonsPin = func(_ context.Context, id, mode string) (apps.FirefoxStoreAddon, error) {
		a := apps.FirefoxStoreAddon{
			ID: id, Name: "Example", Version: version, Mode: mode,
			URL:         "https://addons.mozilla.org/firefox/downloads/file/400000" + version + "/example.xpi",
			SHA256:      strings.Repeat(version, 64),
			Permissions: []string{"storage"},
		}
		if version == "2" {
			a.Permissions = append(a.Permissions, "<all_urls>")
		}
		return a, nil
	}
	t.Cleanup(func() { addonsPin = old })
}

func answers(lines ...string) prompt.UI {
	return prompt.UI{Reader: bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n")), Out: &bytes.Buffer{}}
}

func TestAddonsSet(t *testing.T) {
	path := addonsConfig(t)
	var out bytes.Buffer
	if err := addonsSet(path, []string{"passwords.passbolt", "locked"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := addonsSet(path, []string{"privacy.ublock-origin", "off"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Turned the passwords collection on") {
		t.Fatalf("output %s", out.String())
	}
	user, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := apps.ActiveFirefoxAddons(user.FirefoxAddons); len(got) != 1 || got["passbolt"] != apps.AddonLocked {
		t.Fatalf("active %v", got)
	}
	if err := addonsSet(path, []string{"passwords", "off"}, &out); err != nil {
		t.Fatal(err)
	}
	user, _ = config.Load(path)
	if len(apps.ActiveFirefoxAddons(user.FirefoxAddons)) != 0 || user.FirefoxAddons["passwords"].Addons["passbolt"] != apps.AddonLocked {
		t.Fatalf("collection off: %+v", user.FirefoxAddons["passwords"])
	}

	for _, bad := range [][]string{
		{"games", "on"},
		{"privacy.passbolt", "on"},
		{"privacy.ublock-origin", "yes"},
		{"privacy", "locked"},
		{"addon@example.org", "on"},
		{apps.UBlockOriginID, "on"},
		{"privacy"},
	} {
		if err := addonsSet(path, bad, &out); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestAddonsPinAndUpdate(t *testing.T) {
	path := addonsConfig(t)
	var out bytes.Buffer
	fakeAddonsPin(t, "1")
	if err := addonsPinNew(context.Background(), answers("n"), path, addonsArgs{rest: []string{"addon@example.org"}}, &out); err != nil {
		t.Fatal(err)
	}
	if user, _ := config.Load(path); len(user.FirefoxStoreAddons) != 0 {
		t.Fatal("pinned after no")
	}
	if err := addonsPinNew(context.Background(), answers("y"), path, addonsArgs{rest: []string{"addon@example.org"}}, &out); err != nil {
		t.Fatal(err)
	}
	if err := addonsPinNew(context.Background(), answers("y"), path, addonsArgs{rest: []string{"addon@example.org"}}, &out); err == nil {
		t.Fatal("pinned twice")
	}
	if err := addonsPinNew(context.Background(), answers("y"), path, addonsArgs{rest: []string{apps.UBlockOriginID}}, &out); err == nil {
		t.Fatal("pinned a collection add-on")
	}

	fakeAddonsPin(t, "2")
	ui := answers("y")
	if err := addonsUpdate(context.Background(), ui, path, addonsArgs{}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ui.Out.(*bytes.Buffer).String(), "Update addon@example.org from 1 to 2? New permissions: <all_urls>.") {
		t.Fatalf("question %s", ui.Out.(*bytes.Buffer).String())
	}
	user, _ := config.Load(path)
	if len(user.FirefoxStoreAddons) != 1 || user.FirefoxStoreAddons[0].Version != "2" {
		t.Fatalf("store %+v", user.FirefoxStoreAddons)
	}
	if err := addonsSet(path, []string{"addon@example.org", "locked"}, &out); err != nil {
		t.Fatal(err)
	}
	if user, _ := config.Load(path); user.FirefoxStoreAddons[0].Mode != apps.AddonLocked {
		t.Fatalf("mode %s", user.FirefoxStoreAddons[0].Mode)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte(`"<all_urls>"`)) {
		t.Fatal("permissions escaped in user.config.json")
	}

	// No answer on standard input and no --yes changes nothing.
	fakeAddonsPin(t, "3")
	eof := prompt.UI{Reader: bufio.NewReader(strings.NewReader("")), Out: &bytes.Buffer{}}
	if err := addonsUpdate(context.Background(), eof, path, addonsArgs{}, &out); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no answer: %v", err)
	}
	if err := addonsUpdate(context.Background(), answers(""), path, addonsArgs{}, &out); err != nil {
		t.Fatal(err)
	}
	if user, _ := config.Load(path); user.FirefoxStoreAddons[0].Version != "2" {
		t.Fatal("updated without a yes")
	}
}

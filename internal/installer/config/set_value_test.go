package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSetValueChangesOnlyThatKey(t *testing.T) {
	body := "{\n  \"username\": \"jo\",\n  \"list\": [\n    \"a\"\n  ],\n  \"nested\": {\"list\": 1},\n  \"hostname\": \"box\"\n}\n"
	path := writeConfigFixture(t, body, 0o640)

	if err := SetValue(path, "list", []map[string]any{{"id": "x", "perms": []string{"<all_urls>"}}}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"username\": \"jo\",\n  \"list\": [\n    {\n      \"id\": \"x\",\n      \"perms\": [\n        \"<all_urls>\"\n      ]\n    }\n  ],\n  \"nested\": {\"list\": 1},\n  \"hostname\": \"box\"\n}\n"
	if got := readConfigFixture(t, path); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestSetValueAddsMissingKey(t *testing.T) {
	path := writeConfigFixture(t, "{\n    \"username\": \"jo\"\n}\n", 0o600)
	if err := SetValue(path, "list", []string{}); err != nil {
		t.Fatal(err)
	}
	want := "{\n    \"list\": [],\n    \"username\": \"jo\"\n}\n"
	if got := readConfigFixture(t, path); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetValueRefusesDuplicateKey(t *testing.T) {
	body := "{\"list\": 1, \"list\": 2}"
	path := writeConfigFixture(t, body, 0o600)
	if err := SetValue(path, "list", 3); err == nil {
		t.Fatal("changed a file that lists the key twice")
	}
	if got := readConfigFixture(t, path); got != body {
		t.Fatalf("file changed: %s", got)
	}
}

func TestChangeTextKeepsLayoutAndChecks(t *testing.T) {
	defaults, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(defaults, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	updated, user, err := ChangeText(data, map[string]json.RawMessage{
		"weatherCity": json.RawMessage(`"Example City"`),
		"hostname":    json.RawMessage(`"desk-07"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.Hostname != "desk-07" || user.WeatherCity != "Example City" {
		t.Fatalf("ChangeText gave hostname %q weatherCity %q", user.Hostname, user.WeatherCity)
	}
	if !strings.Contains(string(updated), "\n  \"hostname\": \"desk-07\",\n") {
		t.Fatalf("hostname not changed in place:\n%s", updated)
	}
	if _, _, err := ChangeText(data, map[string]json.RawMessage{"noSuchKey": json.RawMessage(`true`)}); err == nil {
		t.Error("ChangeText accepted an unknown key")
	}
	if _, _, err := ChangeText(data, map[string]json.RawMessage{"theme": json.RawMessage(`"io`)}); err == nil {
		t.Error("ChangeText accepted broken JSON")
	}
	if _, _, err := ChangeText(data, map[string]json.RawMessage{"theme": json.RawMessage(`12`)}); err == nil {
		t.Error("ChangeText accepted a number for theme")
	}
}

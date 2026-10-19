package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseExplicitEmptyWebApplicationsDropsLegacyKeys(t *testing.T) {
	user, err := parsePresetWith(t, `{
		"webApplications": [],
		"planeEnable": true,
		"planeHost": "https://stale-plane.example.test",
		"drawioEnable": true,
		"drawioSelfHosted": true,
		"drawioHost": "https://stale-drawio.example.test"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	if user.WebApplications == nil || len(user.WebApplications) != 0 {
		t.Fatalf("webApplications = %#v, want explicit empty list", user.WebApplications)
	}
}

func TestParseMissingWebApplicationsMigratesLegacyKeys(t *testing.T) {
	user, err := parsePresetWith(t, `{"planeEnable": true, "planeHost": "plane.example.test"}`)
	if err != nil {
		t.Fatal(err)
	}

	assertWebApplications(t, user.WebApplications, []WebApplicationIntent{
		{ID: WebApplicationTeams},
		{ID: WebApplicationPlane, Endpoint: "http://plane.example.test"},
	})
}

func TestParseRejectsBadLegacyPlaneHost(t *testing.T) {
	_, err := parsePresetWith(t, `{"planeEnable": true, "planeHost": ""}`)
	if err == nil || !strings.Contains(err.Error(), "planeHost") {
		t.Fatalf("expected planeHost error, got %v", err)
	}
}

func TestWriteAtomicPersistsCanonicalEmptyWebApplications(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"user.config.json",
	)

	user := User{
		WebApplications: []WebApplicationIntent{},
	}

	if err := WriteAtomic(path, user); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)

	if !strings.Contains(
		text,
		`"webApplications": []`,
	) {
		t.Fatalf(
			"canonical empty webApplications was omitted:\n%s",
			text,
		)
	}

	if strings.Contains(
		text,
		`"webApplications": null`,
	) {
		t.Fatalf(
			"canonical empty webApplications persisted as null:\n%s",
			text,
		)
	}
}

func assertWebApplications(t *testing.T, got, want []WebApplicationIntent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("webApplications = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("webApplications[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

// parsePresetWith parses the shipped preset without its webApplications list,
// with the given JSON fields laid over it.
func parsePresetWith(t *testing.T, overlay string) (User, error) {
	t.Helper()
	preset, err := os.ReadFile("../../../scripts/installation/user_PresetJSON/default.user.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(preset, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "webApplications")
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(overlay), &extra); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		fields[key] = value
	}
	contents, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return Parse(contents)
}

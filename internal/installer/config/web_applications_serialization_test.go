package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var legacyWebApplicationJSONKeys = []string{
	"planeEnable",
	"planeHost",
	"drawioEnable",
	"drawioSelfHosted",
	"drawioHost",
}

func TestParseMigratesAllLegacyWebApplicationFields(t *testing.T) {
	user, err := parsePresetWith(t, `{
		"planeEnable": true,
		"planeHost": "plane.example.test",
		"drawioEnable": true,
		"drawioSelfHosted": true,
		"drawioHost": "drawio.example.test"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	assertWebApplications(t, user.WebApplications, []WebApplicationIntent{
		{ID: WebApplicationTeams},
		{ID: WebApplicationPlane, Endpoint: "http://plane.example.test"},
		{ID: WebApplicationDrawio, Endpoint: "http://drawio.example.test"},
	})
}

func TestHasLegacyWebApplications(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     bool
	}{
		{"canonical list", `{"webApplications": [{"id": "teams"}]}`, false},
		{"canonical empty list", `{"webApplications": []}`, false},
		{"missing list", `{}`, true},
		{"null list", `{"webApplications": null}`, true},
		{"leftover legacy key", `{"webApplications": [], "planeEnable": false}`, true},
		{"not JSON", `nope`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasLegacyWebApplications([]byte(tt.contents)); got != tt.want {
				t.Fatalf("HasLegacyWebApplications(%s) = %t, want %t", tt.contents, got, tt.want)
			}
		})
	}
}

func TestWriteAtomicEmitsCanonicalWebApplicationsOnly(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"user.config.json",
	)

	user := User{
		WebApplications: []WebApplicationIntent{
			{
				ID:       WebApplicationPlane,
				Endpoint: "https://plane.example.test",
			},
		},
	}

	if err := WriteAtomic(path, user); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}

	if _, ok := object["webApplications"]; !ok {
		t.Fatal("WriteAtomic omitted canonical webApplications")
	}

	for _, key := range legacyWebApplicationJSONKeys {
		if _, ok := object[key]; ok {
			t.Fatalf(
				"WriteAtomic emitted migration-only field %q",
				key,
			)
		}
	}
}

func TestLegacyMigrationWritesCanonicalWebApplicationsOnly(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"user.config.json",
	)

	user, err := parsePresetWith(t, `{"planeEnable": true, "planeHost": "plane.example.test"}`)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteAtomic(path, user); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}

	rawApplications, ok := object["webApplications"]
	if !ok {
		t.Fatal("migrated config omitted canonical webApplications")
	}

	var applications []WebApplicationIntent
	if err := json.Unmarshal(
		rawApplications,
		&applications,
	); err != nil {
		t.Fatal(err)
	}

	want := []WebApplicationIntent{
		{
			ID: WebApplicationTeams,
		},
		{
			ID:       WebApplicationPlane,
			Endpoint: "http://plane.example.test",
		},
	}

	if len(applications) != len(want) {
		t.Fatalf(
			"written webApplications = %#v, want %#v",
			applications,
			want,
		)
	}

	for index := range want {
		if applications[index] != want[index] {
			t.Fatalf(
				"written webApplications[%d] = %#v, want %#v",
				index,
				applications[index],
				want[index],
			)
		}
	}

	for _, key := range legacyWebApplicationJSONKeys {
		if _, ok := object[key]; ok {
			t.Fatalf(
				"migrated write retained legacy JSON key %q",
				key,
			)
		}
	}
}

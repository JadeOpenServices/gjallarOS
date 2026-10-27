package config

import (
	"strings"
	"testing"
)

func TestSupportedWebApplicationIDs(t *testing.T) {
	got := SupportedWebApplicationIDs()
	want := []string{"plane", "teams"}

	if len(got) != len(want) {
		t.Fatalf("SupportedWebApplicationIDs() = %#v, want %#v", got, want)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("SupportedWebApplicationIDs() = %#v, want %#v", got, want)
		}
	}
}

func TestNormalizeWebApplicationsCanonicalizesGenericIntent(t *testing.T) {
	user := User{
		WebApplications: []WebApplicationIntent{
			{
				ID:       " Plane ",
				Endpoint: "plane.example.test///",
			},
			{
				ID: "DRAWIO",
			},
			{
				ID: " teams ",
			},
		},
	}

	if err := NormalizeWebApplications(&user); err != nil {
		t.Fatal(err)
	}

	want := []WebApplicationIntent{
		{
			ID:       WebApplicationPlane,
			Endpoint: "http://plane.example.test",
		},
		{
			ID: WebApplicationTeams,
		},
	}

	if len(user.WebApplications) != len(want) {
		t.Fatalf("web applications = %#v, want %#v", user.WebApplications, want)
	}

	for index := range want {
		if user.WebApplications[index] != want[index] {
			t.Fatalf(
				"webApplications[%d] = %#v, want %#v",
				index,
				user.WebApplications[index],
				want[index],
			)
		}
	}

}

func TestParseDropsRetiredDrawio(t *testing.T) {
	contents := `{
		"apps": ["git", "drawio"],
		"webApplications": [{"id": "drawio", "endpoint": "https://drawio.example.test"}, {"id": "teams"}]
	}`
	if got := RetiredEntries([]byte(contents)); len(got) != 1 || got[0] != "drawio" {
		t.Fatalf("RetiredEntries = %v, want [drawio]", got)
	}
	user, err := parsePresetWith(t, contents)
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Apps) != 1 || user.Apps[0] != "git" {
		t.Fatalf("apps = %v, want [git]", user.Apps)
	}
	assertWebApplications(t, user.WebApplications, []WebApplicationIntent{{ID: WebApplicationTeams}})
	if got := RetiredEntries([]byte(`{"apps": ["git"], "webApplications": [{"id": "teams"}]}`)); len(got) != 0 {
		t.Fatalf("RetiredEntries = %v, want none", got)
	}
}

func TestParseMigratesPlaneAndDropsDrawio(t *testing.T) {
	user, err := parsePresetWith(t, `{
		"planeEnable": true,
		"planeHost": "plane.example.test/",
		"drawioEnable": true,
		"drawioSelfHosted": false,
		"drawioHost": "ignored.example.test"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	assertWebApplications(t, user.WebApplications, []WebApplicationIntent{
		{ID: WebApplicationTeams},
		{ID: WebApplicationPlane, Endpoint: "http://plane.example.test"},
	})
}

func TestParseCanonicalListWinsOverLegacyKeys(t *testing.T) {
	user, err := parsePresetWith(t, `{
		"webApplications": [{"id": "teams"}],
		"planeEnable": true,
		"planeHost": "https://stale-plane.example.test",
		"drawioEnable": true,
		"drawioSelfHosted": true,
		"drawioHost": "https://stale-drawio.example.test"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	assertWebApplications(t, user.WebApplications, []WebApplicationIntent{{ID: WebApplicationTeams}})
}

func TestNormalizeWebApplicationsRejectsDuplicateIDs(t *testing.T) {
	user := User{
		WebApplications: []WebApplicationIntent{
			{
				ID:       WebApplicationPlane,
				Endpoint: "https://plane.example.test",
			},
			{
				ID:       "PLANE",
				Endpoint: "https://other-plane.example.test",
			},
		},
	}

	err := NormalizeWebApplications(&user)
	if err == nil || !strings.Contains(err.Error(), "duplicate web application") {
		t.Fatalf("expected duplicate ID rejection, got %v", err)
	}
}

func TestNormalizeWebApplicationsRejectsUnknownID(t *testing.T) {
	user := User{
		WebApplications: []WebApplicationIntent{
			{ID: "unknown"},
		},
	}

	err := NormalizeWebApplications(&user)
	if err == nil || !strings.Contains(err.Error(), "unsupported web application") {
		t.Fatalf("expected unsupported ID rejection, got %v", err)
	}
}

func TestNormalizeWebApplicationsRequiresPlaneEndpoint(t *testing.T) {
	user := User{
		WebApplications: []WebApplicationIntent{
			{ID: WebApplicationPlane},
		},
	}

	err := NormalizeWebApplications(&user)
	if err == nil || !strings.Contains(err.Error(), "Plane requires an endpoint") {
		t.Fatalf("expected Plane endpoint rejection, got %v", err)
	}
}

func TestNormalizeWebApplicationsAllowsTeamsBuiltInEndpoint(t *testing.T) {
	user := User{
		WebApplications: []WebApplicationIntent{
			{ID: WebApplicationTeams},
		},
	}

	if err := NormalizeWebApplications(&user); err != nil {
		t.Fatal(err)
	}

	if len(user.WebApplications) != 1 ||
		user.WebApplications[0].ID != WebApplicationTeams ||
		user.WebApplications[0].Endpoint != "" {
		t.Fatalf("unexpected Teams intent: %#v", user.WebApplications)
	}
}

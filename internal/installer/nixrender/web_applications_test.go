package nixrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func TestWebApplicationsRendersSingleLineNixLiteral(t *testing.T) {
	got := WebApplications([]config.WebApplicationIntent{
		{
			ID:       config.WebApplicationPlane,
			Endpoint: "https://plane.example.test",
		},
		{
			ID: config.WebApplicationDrawio,
		},
		{
			ID: config.WebApplicationTeams,
		},
	})

	want := `[ { id = "plane"; endpoint = "https://plane.example.test"; } { id = "drawio"; endpoint = ""; } { id = "teams"; endpoint = ""; } ]`

	if got != want {
		t.Fatalf("WebApplications() = %q, want %q", got, want)
	}

	if strings.Contains(got, "\n") {
		t.Fatalf("webApplications literal must remain single-line: %q", got)
	}
}

func TestWebApplicationsEscapesNixInterpolation(t *testing.T) {
	got := WebApplications([]config.WebApplicationIntent{
		{
			ID:       `${builtins.abort "id"}`,
			Endpoint: `https://example.test/${builtins.abort "endpoint"}`,
		},
	})

	for _, want := range []string{
		`\${builtins.abort \"id\"}`,
		`\${builtins.abort \"endpoint\"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing escaped %q from %q", want, got)
		}
	}
}

func TestFromUserMapsCanonicalWebApplications(t *testing.T) {
	user := config.User{
		WebApplications: []config.WebApplicationIntent{
			{
				ID:       config.WebApplicationPlane,
				Endpoint: "https://plane.example.test",
			},
			{
				ID: config.WebApplicationTeams,
			},
		},
	}

	got := FromUser(user)

	if len(got.WebApplications) != 2 {
		t.Fatalf(
			"canonical web applications not mapped: %#v",
			got.WebApplications,
		)
	}

	for index := range user.WebApplications {
		if got.WebApplications[index] != user.WebApplications[index] {
			t.Fatalf(
				"webApplications[%d] = %#v, want %#v",
				index,
				got.WebApplications[index],
				user.WebApplications[index],
			)
		}
	}
}

func TestRenderEmitsOnlyCanonicalWebApplicationSettings(t *testing.T) {
	user := config.User{
		WebApplications: []config.WebApplicationIntent{
			{
				ID:       config.WebApplicationPlane,
				Endpoint: "https://plane.example.test",
			},
			{
				ID: config.WebApplicationDrawio,
			},
			{
				ID: config.WebApplicationTeams,
			},
		},
	}

	rendered := string(Render(FromUser(user)))

	if !strings.Contains(
		rendered,
		`webApplications = [ { id = "plane"; endpoint = "https://plane.example.test"; } { id = "drawio"; endpoint = ""; } { id = "teams"; endpoint = ""; } ];`,
	) {
		t.Fatalf(
			"canonical webApplications missing:\n%s",
			rendered,
		)
	}

	for _, legacy := range []string{
		"planeEnable = ",
		"planeHost = ",
		"drawioEnable = ",
		"drawioSelfHosted = ",
		"drawioHost = ",
	} {
		if strings.Contains(rendered, legacy) {
			t.Fatalf(
				"generated state retained legacy assignment %q:\n%s",
				legacy,
				rendered,
			)
		}
	}
}

func TestSyncUserIntentUpdatesWebApplicationsAndPreservesDerivedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.nix")

	before := `{pkgs, inputs, ...}:
rec {
    hostname = "old";
    webApplications = [ { id = "drawio"; endpoint = ""; } ];
    planeEnable = true;
    planeHost = "https://legacy-plane.example.test";
    drawioEnable = true;
    drawioSelfHosted = false;
    drawioHost = "";
    aiModel = "derived-model";
    theme = "old-theme";
    themeDetails = import (./. + "/../themes/${theme}.nix") {inherit pkgs;};
}
`

	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	user := config.User{
		Hostname: "gjallarOS",
		WebApplications: []config.WebApplicationIntent{
			{
				ID:       config.WebApplicationPlane,
				Endpoint: "https://plane.example.test",
			},
			{
				ID: config.WebApplicationTeams,
			},
		},
		Theme:              "noctalia",
		AIAgentMode:        "workspace",
		JODSEnrollmentMode: "manual",
	}

	if err := SyncUserIntent(path, user); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	got := string(data)

	for _, want := range []string{
		`hostname = "gjallarOS";`,
		`webApplications = [ { id = "plane"; endpoint = "https://plane.example.test"; } { id = "teams"; endpoint = ""; } ];`,
		`aiModel = "derived-model";`,
		`theme = "noctalia";`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q after sync:\n%s", want, got)
		}
	}

	for _, retired := range []string{
		"planeEnable",
		"planeHost",
		"drawioEnable",
		"drawioSelfHosted",
		"drawioHost",
	} {
		if strings.Contains(got, retired+" = ") {
			t.Fatalf(
				"retired web-application field %q survived sync:\n%s",
				retired,
				got,
			)
		}
	}
}

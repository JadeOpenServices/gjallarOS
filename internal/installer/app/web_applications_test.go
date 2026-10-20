package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func TestCollectProjectToolsSupportsTeamsWebApplication(t *testing.T) {
	var output bytes.Buffer
	user := config.User{}

	err := collectProjectTools(
		context.Background(),
		prompt.New(
			strings.NewReader(
				"no\n"+
					"no\n"+
					"yes\n"+
					"no\n",
			),
			&output,
		),
		nil,
		&user,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(user.WebApplications) != 1 {
		t.Fatalf(
			"webApplications = %#v, want one entry",
			user.WebApplications,
		)
	}

	application := user.WebApplications[0]

	if application.ID != config.WebApplicationTeams {
		t.Fatalf(
			"webApplications[0].ID = %q, want %q",
			application.ID,
			config.WebApplicationTeams,
		)
	}

	if application.Endpoint != "" {
		t.Fatalf(
			"Teams endpoint = %q, want feature-owned default",
			application.Endpoint,
		)
	}

	if len(user.WebApplications) != 1 ||
		user.NextcloudEnable {
		t.Fatalf(
			"Teams selection changed unrelated integrations: %+v",
			user,
		)
	}
}

func TestCollectProjectToolsProducesCanonicalWebApplicationIntent(t *testing.T) {
	var output bytes.Buffer
	user := config.User{}

	err := collectProjectTools(
		context.Background(),
		prompt.New(
			strings.NewReader(
				"yes\n"+
					"plane.example.test\n"+
					"yes\n"+
					"no\n"+
					"yes\n"+
					"no\n",
			),
			&output,
		),
		nil,
		&user,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []config.WebApplicationIntent{
		{
			ID:       config.WebApplicationPlane,
			Endpoint: "http://plane.example.test",
		},
		{
			ID: config.WebApplicationDrawio,
		},
		{
			ID: config.WebApplicationTeams,
		},
	}

	if len(user.WebApplications) != len(want) {
		t.Fatalf(
			"webApplications = %#v, want %#v",
			user.WebApplications,
			want,
		)
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

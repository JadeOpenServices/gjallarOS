package config

import (
	"strings"
	"testing"
)

func TestProjectToolsWithoutWebApplicationsValidate(t *testing.T) {
	if err := ValidateProjectTools(User{}); err != nil {
		t.Fatalf("no web applications should validate: %v", err)
	}
}

func TestPlaneRequiresEndpoint(t *testing.T) {
	u := User{WebApplications: []WebApplicationIntent{{ID: WebApplicationPlane}}}

	err := ValidateProjectTools(u)
	if err == nil || !strings.Contains(err.Error(), "Plane requires an endpoint") {
		t.Fatalf("expected Plane endpoint validation error, got %v", err)
	}
}

func TestTeamsDoesNotRequireEndpoint(t *testing.T) {
	u := User{WebApplications: []WebApplicationIntent{{ID: WebApplicationTeams}}}

	if err := ValidateProjectTools(u); err != nil {
		t.Fatalf("Teams should not require an endpoint: %v", err)
	}
}

func TestProjectToolsAcceptConfiguredEndpoints(t *testing.T) {
	u := User{WebApplications: []WebApplicationIntent{
		{ID: WebApplicationPlane, Endpoint: "192.0.2.203"},
		{ID: WebApplicationTeams},
	}}

	if err := ValidateProjectTools(u); err != nil {
		t.Fatalf("configured project tools should validate: %v", err)
	}
}

func TestNormalizeExternalServiceEndpoint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "IPv4",
			in:   "192.0.2.203",
			want: "http://192.0.2.203",
		},
		{
			name: "IPv4 with port",
			in:   "192.0.2.204:8080",
			want: "http://192.0.2.204:8080",
		},
		{
			name: "explicit HTTPS",
			in:   "https://plane.example.internal",
			want: "https://plane.example.internal",
		},
		{
			name: "explicit HTTP and port",
			in:   "http://drawio.example.internal:8080",
			want: "http://drawio.example.internal:8080",
		},
		{
			name: "trailing slash",
			in:   "https://plane.example.internal/",
			want: "https://plane.example.internal",
		},
		{
			name: "duplicate trailing slashes",
			in:   "https://plane.example.internal///",
			want: "https://plane.example.internal",
		},
		{
			name: "surrounding whitespace",
			in:   "  192.0.2.203:3000  ",
			want: "http://192.0.2.203:3000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeExternalServiceEndpoint(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeExternalServiceEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeExternalServiceEndpointRejectsMalformedValues(t *testing.T) {
	tests := []string{
		"",
		"   ",
		"http://",
		"https://",
		"://broken",
		"ftp://plane.example.internal",
		"http://user:password@plane.example.internal",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := NormalizeExternalServiceEndpoint(input); err == nil {
				t.Fatalf("expected %q to be rejected", input)
			}
		})
	}
}

func TestNormalizeProjectToolsCanonicalizesEnabledEndpoints(t *testing.T) {
	u := User{WebApplications: []WebApplicationIntent{
		{ID: WebApplicationPlane, Endpoint: "192.0.2.203:8080///"},
	}}

	if err := NormalizeProjectTools(&u); err != nil {
		t.Fatal(err)
	}

	if u.WebApplications[0].Endpoint != "http://192.0.2.203:8080" {
		t.Fatalf("unexpected normalized Plane endpoint: %q", u.WebApplications[0].Endpoint)
	}
}

func TestNormalizeExternalServiceEndpointPreservesQuery(t *testing.T) {
	const input = "http://192.0.2.204:8080/?offline=1&https=0"

	got, err := NormalizeExternalServiceEndpoint(input)
	if err != nil {
		t.Fatalf("NormalizeExternalServiceEndpoint() error = %v", err)
	}

	if got != input {
		t.Fatalf("NormalizeExternalServiceEndpoint() = %q, want %q", got, input)
	}
}

func validJODSProjectToolTestUser() User {
	return User{
		EndpointManagedDevice:  true,
		JODSEndpoint:           "https://jods.example.test",
		JODSPolicySigningKey:   strings.Repeat("a", 64),
		JODSRecoverySigningKey: strings.Repeat("b", 64),
		JODSEnrollmentMode:     "auto",
		JODSDeviceClass:        "workstation",
		JODSDesktopProfile:     "default",
	}
}

func TestProjectToolsRemainIndependentFromJODS(t *testing.T) {
	tests := []struct {
		name               string
		planeEnable        bool
		planeHost          string
		teamsEnable        bool
		jodsEnable         bool
		wantProjectToolErr bool
	}{
		{
			name: "both disabled",
		},
		{
			name:        "Plane only",
			planeEnable: true,
			planeHost:   "https://plane.example.test",
		},
		{
			name:        "Teams only",
			teamsEnable: true,
		},
		{
			name:        "both enabled",
			planeEnable: true,
			planeHost:   "https://plane.example.test",
			teamsEnable: true,
		},
		{
			name:               "Plane enabled with empty endpoint",
			planeEnable:        true,
			wantProjectToolErr: true,
		},
		{
			name:        "JODS disabled with project tools enabled",
			planeEnable: true,
			planeHost:   "https://plane.example.test",
			teamsEnable: true,
		},
		{
			name:       "JODS enabled with project tools disabled",
			jodsEnable: true,
		},
		{
			name:        "JODS enabled with project tools enabled",
			planeEnable: true,
			planeHost:   "https://plane.example.test",
			teamsEnable: true,
			jodsEnable:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := User{}
			if tt.planeEnable {
				u.WebApplications = append(u.WebApplications, WebApplicationIntent{ID: WebApplicationPlane, Endpoint: tt.planeHost})
			}
			if tt.teamsEnable {
				u.WebApplications = append(u.WebApplications, WebApplicationIntent{ID: WebApplicationTeams})
			}

			if tt.jodsEnable {
				jods := validJODSProjectToolTestUser()
				u.EndpointManagedDevice = jods.EndpointManagedDevice
				u.JODSEndpoint = jods.JODSEndpoint
				u.JODSPolicySigningKey = jods.JODSPolicySigningKey
				u.JODSRecoverySigningKey = jods.JODSRecoverySigningKey
				u.JODSEnrollmentMode = jods.JODSEnrollmentMode
				u.JODSDeviceClass = jods.JODSDeviceClass
				u.JODSDesktopProfile = jods.JODSDesktopProfile
			}

			projectErr := ValidateProjectTools(u)
			if tt.wantProjectToolErr {
				if projectErr == nil {
					t.Fatal("expected project-tool validation failure")
				}
			} else if projectErr != nil {
				t.Fatalf("project-tool validation unexpectedly failed: %v", projectErr)
			}

			if err := ValidateJODS(u); err != nil {
				t.Fatalf("JODS validation changed because of project-tool configuration: %v", err)
			}

			beforeJODS := struct {
				enabled     bool
				endpoint    string
				policyKey   string
				recoveryKey string
				enrollment  string
				deviceClass string
				desktop     string
			}{
				u.EndpointManagedDevice,
				u.JODSEndpoint,
				u.JODSPolicySigningKey,
				u.JODSRecoverySigningKey,
				u.JODSEnrollmentMode,
				u.JODSDeviceClass,
				u.JODSDesktopProfile,
			}

			normalized := u
			normalizeErr := NormalizeProjectTools(&normalized)

			if tt.wantProjectToolErr {
				if normalizeErr == nil {
					t.Fatal("expected project-tool normalization failure")
				}
			} else if normalizeErr != nil {
				t.Fatalf("project-tool normalization unexpectedly failed: %v", normalizeErr)
			}

			afterJODS := struct {
				enabled     bool
				endpoint    string
				policyKey   string
				recoveryKey string
				enrollment  string
				deviceClass string
				desktop     string
			}{
				normalized.EndpointManagedDevice,
				normalized.JODSEndpoint,
				normalized.JODSPolicySigningKey,
				normalized.JODSRecoverySigningKey,
				normalized.JODSEnrollmentMode,
				normalized.JODSDeviceClass,
				normalized.JODSDesktopProfile,
			}

			if beforeJODS != afterJODS {
				t.Fatalf(
					"project-tool normalization mutated JODS state: before=%+v after=%+v",
					beforeJODS,
					afterJODS,
				)
			}
		})
	}
}

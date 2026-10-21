package installercheck

import (
	"encoding/json"
	"testing"
)

func TestMatchesPresetWebApplicationList(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{
			name: "empty",
			json: `[]`,
			want: true,
		},
		{
			name: "teams built in endpoint",
			json: `[{"id":"teams"}]`,
			want: true,
		},
		{
			name: "plane endpoint",
			json: `[{"id":"plane","endpoint":"https://plane.example.test"}]`,
			want: true,
		},
		{
			name: "not a list",
			json: `{"id":"teams"}`,
			want: false,
		},
		{
			name: "null",
			json: `null`,
			want: false,
		},
		{
			name: "missing id",
			json: `[{"endpoint":"https://example.test"}]`,
			want: false,
		},
		{
			name: "id wrong type",
			json: `[{"id":true}]`,
			want: false,
		},
		{
			name: "endpoint wrong type",
			json: `[{"id":"plane","endpoint":false}]`,
			want: false,
		},
		{
			name: "unknown object field",
			json: `[{"id":"teams","browser":"edge"}]`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesPresetWebApplicationList(
				json.RawMessage(tt.json),
			)

			if got != tt.want {
				t.Fatalf(
					"matchesPresetWebApplicationList(%s) = %t, want %t",
					tt.json,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestPresetSchemaAcceptsCanonicalAndLegacyWebApplications(t *testing.T) {
	if presetSchema["webApplications"] != presetWebApplicationList {
		t.Fatal("webApplications schema type is not registered")
	}

	legacy := map[string]presetValueType{
		"planeEnable":      presetBool,
		"planeHost":        presetString,
		"drawioEnable":     presetBool,
		"drawioSelfHosted": presetBool,
		"drawioHost":       presetString,
	}

	for key, want := range legacy {
		if got, ok := presetSchema[key]; !ok || got != want {
			t.Fatalf(
				"legacy schema %s = %v, %t; want %v",
				key,
				got,
				ok,
				want,
			)
		}
	}
}

func TestPresetTailscaleSchema(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"homeSubnets":["192.168.8.0/24"],"trustedWifis":["synthetic-5Ghz"],"exitNode":"r","siteRouterTrust":true,"siteRouterTargets":["192.168.8.1:53"]}`: true,
		`{"trustedWifis":["mum"],"exitNode":"r","wifiExitNodes":{"mum":"auto"}}`:                                                                           true,
		`{"wifiExitNodes":["mum"]}`: false,
		`{}`:                        true,
		`{"enable":true}`:           false, // selected by apps
		`{"trustedWifis":"home"}`:   false,
		`{"vpn":true}`:              false,
		`null`:                      false,
		`[]`:                        false,
	} {
		if got := matchesPresetType(json.RawMessage(raw), presetTailscale); got != want {
			t.Errorf("%s: got %v, want %v", raw, got, want)
		}
	}
}

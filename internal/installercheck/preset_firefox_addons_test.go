package installercheck

import (
	"encoding/json"
	"testing"
)

func TestMatchesPresetFirefoxAddons(t *testing.T) {
	tests := []struct {
		name string
		json string
		kind presetValueType
		want bool
	}{
		{"empty choices", `{}`, presetFirefoxAddons, true},
		{"collection choice", `{"privacy":{"enable":true,"addons":{"ublock-origin":"on"}}}`, presetFirefoxAddons, true},
		{"choices as list", `[]`, presetFirefoxAddons, false},
		{"empty store list", `[]`, presetFirefoxStoreAddons, true},
		{"store addon", `[{"id":"addon@example.test","version":"1.0","mode":"on"}]`, presetFirefoxStoreAddons, true},
		{"store as object", `{}`, presetFirefoxStoreAddons, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesPresetType(json.RawMessage(tt.json), tt.kind); got != tt.want {
				t.Fatalf("matchesPresetType(%s) = %t, want %t", tt.json, got, tt.want)
			}
		})
	}
}

func TestPresetSchemaKnowsFirefoxAddons(t *testing.T) {
	r := &Report{}
	validatePresetSchema(r, map[string]json.RawMessage{
		"firefoxAddons":      json.RawMessage(`{}`),
		"firefoxStoreAddons": json.RawMessage(`[]`),
	})
	for _, f := range r.Findings {
		t.Errorf("unexpected finding: %+v", f)
	}
}

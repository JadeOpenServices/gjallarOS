package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	tests := []struct {
		name     string
		hardware Hardware
		override Override
		profile  string
		model    string
		context  int
	}{
		{"low memory", Hardware{RAMGB: 7}, Override{}, "low-memory", "qwen2.5-coder:7b", 8192},
		{"too little memory", Hardware{RAMGB: 3, CPUCores: 4}, Override{}, None, "", 0},
		{"override on little memory", Hardware{RAMGB: 3}, Override{Enabled: true, Model: "qwen2.5-coder:1.5b"}, "user-override", "qwen2.5-coder:1.5b", 8192},
		{"16 GiB stays on 7b", Hardware{RAMGB: 15, CPUCores: 4}, Override{}, "low-memory", "qwen2.5-coder:7b", 8192},
		{"integrated", Hardware{RAMGB: 23, CPUCores: 4}, Override{}, "integrated", "qwen2.5-coder:14b", 16384},
		{"dedicated", Hardware{RAMGB: 31, CPUCores: 8, GPUType: "dedicated", VRAMMB: 12288}, Override{}, "dedicated", "qwen3-coder:30b", 32768},
		{"override", Hardware{RAMGB: 15, CPUCores: 4}, Override{Enabled: true, Model: "mistral"}, "user-override", "mistral", 16384},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Select(test.hardware, test.override)
			if got.Profile != test.profile || got.Model != test.model || got.ContextTokens != test.context {
				t.Fatalf("Select() = %#v", got)
			}
		})
	}
}

func TestAccelerationProfileSelection(t *testing.T) {
	tests := []struct {
		name     string
		hardware Hardware
		override Override
		want     string
	}{
		{
			name:     "small integrated automatic",
			hardware: Hardware{RAMGB: 23, CPUCores: 4, GPUType: "integrated"},
			want:     "auto",
		},
		{
			name:     "large integrated full",
			hardware: Hardware{RAMGB: 64, CPUCores: 8, GPUType: "integrated"},
			want:     "full",
		},
		{
			name: "qualified dedicated full",
			hardware: Hardware{
				RAMGB:    31,
				CPUCores: 8,
				GPUType:  "dedicated",
				VRAMMB:   12288,
			},
			want: "full",
		},
		{
			name:     "unknown override automatic",
			hardware: Hardware{RAMGB: 64, CPUCores: 8, GPUType: "integrated"},
			override: Override{
				Enabled: true,
				Model:   "mistral",
			},
			want: "auto",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Select(test.hardware, test.override)

			if got.AccelerationProfile != test.want {
				t.Fatalf(
					"AccelerationProfile = %q, want %q",
					got.AccelerationProfile,
					test.want,
				)
			}
		})
	}
}

func TestLoadOverrideRequiresModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.config.json")
	if err := os.WriteFile(path, []byte(`{"overrideAiSelection":true,"overrideModelWith":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverride(path); err == nil {
		t.Fatal("empty manual override accepted")
	}
}

func TestLoadOverrideRejectsControlCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.config.json")
	if err := os.WriteFile(path, []byte(`{"overrideAiSelection":true,"overrideModelWith":"bad\nmodel"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverride(path); err == nil {
		t.Fatal("control characters must be rejected")
	}
}

func TestOverrideErrorsNameFieldOnlyForFiles(t *testing.T) {
	_, err := NewOverride(true, "bad model!")
	if err == nil || strings.Contains(err.Error(), "overrideModelWith") || !strings.Contains(err.Error(), `"bad model!"`) {
		t.Fatalf("typed model error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "user.config.json")
	if err := os.WriteFile(path, []byte(`{"overrideAiSelection":true,"overrideModelWith":"bad model!"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverride(path); err == nil || !strings.HasPrefix(err.Error(), "overrideModelWith: ") {
		t.Fatalf("file model error = %v", err)
	}
}

// apps/ai/nixos.nix verifies the pulled base model against model-pins.json,
// so every model the automatic selection picks needs a complete pin there.
func TestModelPins(t *testing.T) {
	data, err := os.ReadFile("model-pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]struct {
		Config string   `json:"config"`
		Model  string   `json:"model"`
		Layers []string `json:"layers"`
	}
	if err := json.Unmarshal(data, &pins); err != nil {
		t.Fatal(err)
	}
	digest := func(s string) bool {
		hex := strings.TrimPrefix(s, "sha256:")
		return hex != s && len(hex) == 64 && strings.Trim(hex, "0123456789abcdef") == ""
	}
	for _, p := range modelProfiles {
		pin, ok := pins[p.Model]
		if !ok {
			t.Errorf("%s has no pin", p.Model)
			continue
		}
		if !digest(pin.Config) || !digest(pin.Model) || !slices.Contains(pin.Layers, pin.Model) || !slices.IsSorted(pin.Layers) {
			t.Errorf("%s pin is incomplete", p.Model)
		}
		for _, layer := range pin.Layers {
			if !digest(layer) {
				t.Errorf("%s layer %q is not a sha256 digest", p.Model, layer)
			}
		}
	}
	for model := range pins {
		// The manifest path in apps/ai/nixos.nix assumes library/<name>/<tag>.
		if strings.Count(model, ":") != 1 || strings.Contains(model, "/") {
			t.Errorf("pin %q must be a library model with a tag", model)
		}
	}
}

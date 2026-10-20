package nixrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncAIProfileReplacesOnlyAIFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.nix")
	before := `{pkgs, inputs, ...}:
rec {
    hostname = "gjallarOS";
    aiModel = "";
    aiAccelerationProfile = "";
    aiAgentMode = "workspace";
    aiContextTokens = 0;
    aiVramMB = 0;
    themeDetails = {};
}
`
	if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	if model, err := AIModel(path); err != nil || model != "" {
		t.Fatalf("AIModel = %q, %v", model, err)
	}
	if err := SyncAIProfile(path, Settings{
		AIModel: "qwen2.5-coder:7b", AIAccelerationProfile: "auto", AIContextTokens: 8192, AIVRAMMB: 512,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer(
		`aiModel = "";`, `aiModel = "qwen2.5-coder:7b";`,
		`aiAccelerationProfile = "";`, `aiAccelerationProfile = "auto";`,
		`aiContextTokens = 0;`, `aiContextTokens = 8192;`,
		`aiVramMB = 0;`, `aiVramMB = 512;`,
	).Replace(before)
	if string(data) != want {
		t.Fatalf("state:\n%s\nwant:\n%s", data, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v, %v", info.Mode(), err)
	}
	if model, err := AIModel(path); err != nil || model != "qwen2.5-coder:7b" {
		t.Fatalf("AIModel = %q, %v", model, err)
	}
}

func TestSyncAIProfileNeedsEveryField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.nix")
	if err := os.WriteFile(path, []byte("rec {\n    aiModel = \"\";\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SyncAIProfile(path, Settings{AIModel: "qwen2.5-coder:7b"}); err == nil {
		t.Fatal("state without aiVramMB synced")
	}
}

func TestAIModelReadsWhatStringWrote(t *testing.T) {
	for _, model := range []string{"", "qwen2.5-coder:7b", `odd"name\with${x}`} {
		path := filepath.Join(t.TempDir(), "state.nix")
		if err := os.WriteFile(path, []byte("    aiModel = "+String(model)+";\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := AIModel(path); err != nil || got != model {
			t.Errorf("AIModel(%q) = %q, %v", model, got, err)
		}
	}
	path := filepath.Join(t.TempDir(), "state.nix")
	if err := os.WriteFile(path, []byte("    aiModel = null;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AIModel(path); err == nil {
		t.Fatal("null read as a model")
	}
}

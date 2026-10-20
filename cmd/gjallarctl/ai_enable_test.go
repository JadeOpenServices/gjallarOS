package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/ai/profile"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/nixrender"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// fakeAIDetect stands in for hardware detection; its results are SYNTHETIC.
func fakeAIDetect(t *testing.T, ramGB int) *int {
	t.Helper()
	calls := 0
	old := aiDetect
	t.Cleanup(func() { aiDetect = old })
	aiDetect = func(_ context.Context, override profile.Override) (profile.Result, error) {
		calls++
		result := profile.Select(profile.Hardware{RAMGB: ramGB, CPUCores: 8}, override)
		result.RAMGB = ramGB
		return result, nil
	}
	return &calls
}

func aiState(t *testing.T, model string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.nix")
	state := "rec {\n    aiModel = " + nixrender.String(model) + ";\n    aiAccelerationProfile = \"\";\n    aiContextTokens = 0;\n    aiVramMB = 0;\n}\n"
	if err := os.WriteFile(path, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSyncLocalAIModelPicksWhenEmpty(t *testing.T) {
	calls := fakeAIDetect(t, 15)
	path := aiState(t, "")
	var stdout, stderr bytes.Buffer
	if !syncLocalAIModel(path, config.User{Apps: []string{"ai"}}, &stdout, &stderr) {
		t.Fatalf("failed: %s", stderr.String())
	}
	if model, _ := nixrender.AIModel(path); model != "qwen2.5-coder:7b" || *calls != 1 {
		t.Fatalf("model %q after %d detections", model, *calls)
	}
	if !strings.Contains(stdout.String(), "qwen2.5-coder:7b picked") {
		t.Fatalf("stdout %q", stdout.String())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "aiContextTokens = 8192;") {
		t.Fatalf("state:\n%s", data)
	}
}

func TestSyncLocalAIModelKeepsWhatWorks(t *testing.T) {
	calls := fakeAIDetect(t, 31)
	cases := []config.User{
		{Apps: []string{"ai"}},
		{Apps: []string{"ai"}, AIEndpoint: "https://ai.example.test"},
		{Apps: []string{"firefox"}},
	}
	for _, user := range cases {
		path := aiState(t, "qwen2.5-coder:7b")
		var stdout, stderr bytes.Buffer
		if !syncLocalAIModel(path, user, &stdout, &stderr) {
			t.Fatalf("failed: %s", stderr.String())
		}
		if model, _ := nixrender.AIModel(path); model != "qwen2.5-coder:7b" {
			t.Errorf("%+v: model became %q", user, model)
		}
	}
	if *calls != 0 {
		t.Fatalf("detected %d times", *calls)
	}
}

func TestSyncLocalAIModelFollowsOverride(t *testing.T) {
	fakeAIDetect(t, 15)
	path := aiState(t, "qwen2.5-coder:7b")
	user := config.User{Apps: []string{"ai"}, OverrideAISelection: true, OverrideModelWith: "llama3.2:3b"}
	var stdout, stderr bytes.Buffer
	if !syncLocalAIModel(path, user, &stdout, &stderr) {
		t.Fatalf("failed: %s", stderr.String())
	}
	if model, _ := nixrender.AIModel(path); model != "llama3.2:3b" {
		t.Fatalf("model %q", model)
	}
	// The override off again: the automatic pick comes back.
	user.OverrideAISelection, user.OverrideModelWith = false, ""
	if !syncLocalAIModel(path, user, &stdout, &stderr) {
		t.Fatalf("failed: %s", stderr.String())
	}
	if model, _ := nixrender.AIModel(path); model != "qwen2.5-coder:7b" {
		t.Fatalf("model %q", model)
	}
}

func TestSyncLocalAIModelStopsWhenNothingFits(t *testing.T) {
	fakeAIDetect(t, 3)
	path := aiState(t, "")
	var stdout, stderr bytes.Buffer
	if syncLocalAIModel(path, config.User{Apps: []string{"ai"}}, &stdout, &stderr) {
		t.Fatal("rebuild went on without a model")
	}
	if !strings.Contains(stderr.String(), "no local model fits 3 GiB RAM") || !strings.Contains(stderr.String(), "nothing rebuilt") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func aiSettingsBus(apps, endpoint string, override bool) *fakeSettingsBus {
	return &fakeSettingsBus{values: map[string]settings.KeyValue{
		"apps":                {Key: "apps", Tier: config.TierEveryday, Value: json.RawMessage(apps)},
		"aiEndpoint":          {Key: "aiEndpoint", Tier: config.TierSecurity, Value: json.RawMessage(endpoint)},
		"overrideAiSelection": {Key: "overrideAiSelection", Tier: config.TierEveryday, Value: json.RawMessage(strconv.FormatBool(override))},
	}}
}

func fakeMemory(t *testing.T, ramGB int) {
	t.Helper()
	old := aiMemoryGB
	t.Cleanup(func() { aiMemoryGB = old })
	aiMemoryGB = func() (int, error) { return ramGB, nil }
}

func TestAIEnableAddsTheApp(t *testing.T) {
	bus := aiSettingsBus(`["firefox"]`, `""`, false)
	useFakeSettings(t, bus)
	fakeMemory(t, 15)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ai", "enable"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if len(bus.requests) != 1 || bus.requests[0] != `{"apps":["firefox","ai"]}` {
		t.Fatalf("requests %q", bus.requests)
	}
	if len(bus.applied) != 1 {
		t.Fatalf("applied %q", bus.applied)
	}
}

func TestAIEnableRefusesTooLittleMemory(t *testing.T) {
	bus := aiSettingsBus(`["firefox"]`, `""`, false)
	useFakeSettings(t, bus)
	fakeMemory(t, 3)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ai", "enable"}, &stdout, &stderr); code == 0 {
		t.Fatal("asked for ai on 3 GiB")
	}
	if len(bus.requests) != 0 || !strings.Contains(stderr.String(), "no local model fits 3 GiB RAM") {
		t.Fatalf("requests %q, stderr %q", bus.requests, stderr.String())
	}

	// With an endpoint the local memory does not matter.
	bus = aiSettingsBus(`["firefox"]`, `"https://ai.example.test"`, false)
	useFakeSettings(t, bus)
	fakeMemory(t, 3)
	if code := run([]string{"ai", "enable"}, &stdout, &stderr); code != 0 || len(bus.requests) != 1 {
		t.Fatalf("code %d, requests %q: %s", code, bus.requests, stderr.String())
	}
}

func TestAIEnableWhenOn(t *testing.T) {
	bus := aiSettingsBus(`["ai"]`, `""`, false)
	useFakeSettings(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ai", "enable"}, &stdout, &stderr); code != 0 || len(bus.requests) != 0 {
		t.Fatalf("code %d, requests %q", code, bus.requests)
	}
	if !strings.Contains(stdout.String(), "already on") {
		t.Fatalf("stdout %q", stdout.String())
	}
}

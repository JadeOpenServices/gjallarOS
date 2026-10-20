package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/JadeOpenServices/gjallarOS/internal/ai/profile"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/nixrender"
)

var (
	aiDetect   = profile.DetectWith
	aiMemoryGB = profile.MemoryGB
)

// noLocalModel is the installer's answer when no local model fits.
const noLocalModel = "no local model fits %d GiB RAM (8 GiB needed): set aiEndpoint, set overrideAiSelection with overrideModelWith, or remove \"ai\" from apps"

// syncLocalAIModel picks the local AI model on a rebuild the way the
// installer does, when the ai app is on without an endpoint and the state
// has no model yet, or holds another model than the override asks for. A
// model the automatic table picked stays, so a rebuild never swaps a model
// that works for one the hardware detection now prefers. The model is
// derived here, as root on the system checkout; user.config.json only says
// whether local AI is on and which override it wants.
func syncLocalAIModel(statePath string, user config.User, stdout, stderr io.Writer) bool {
	if !user.HasApp("ai") || user.AIEndpoint != "" {
		return true
	}
	current, err := nixrender.AIModel(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: read the local AI model: %v\n", err)
		return false
	}
	override, err := profile.NewOverride(user.OverrideAISelection, user.OverrideModelWith)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: overrideModelWith: %v; nothing rebuilt\n", err)
		return false
	}
	switch {
	case current == "":
	case override.Enabled && override.Model != current:
	case !override.Enabled && !profile.Automatic(current):
	default:
		return true
	}
	result, err := aiDetect(context.Background(), override)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: pick the local AI model: %v; nothing rebuilt\n", err)
		return false
	}
	if result.Model == "" {
		fmt.Fprintf(stderr, "[GjallarOS] Error: user.config.json enables local AI, but "+noLocalModel+"; nothing rebuilt\n", result.RAMGB)
		return false
	}
	if err := nixrender.SyncAIProfile(statePath, nixrender.Settings{
		AIModel:               result.Model,
		AIAccelerationProfile: result.AccelerationProfile,
		AIContextTokens:       result.ContextTokens,
		AIVRAMMB:              result.VRAMMB,
	}); err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: write the local AI model: %v\n", err)
		return false
	}
	fmt.Fprintf(stdout, "[GjallarOS] Local AI: %s picked for this machine (%s).\n", result.Model, result.Profile)
	return true
}

// runAIEnable turns the ai app on through the settings service. The
// rebuild that follows picks the model for this machine.
func runAIEnable(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl ai enable")
		return 2
	}
	bus, ok := openSettings(stderr)
	if !ok {
		return 1
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()

	read := func(name string, value any) bool {
		key, err := settingsKey(ctx, bus, name)
		if err == nil {
			err = json.Unmarshal(key.Value, value)
		}
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: read %s: %v\n", name, err)
			return false
		}
		return true
	}
	var apps []string
	var endpoint string
	var override bool
	if !read("apps", &apps) || !read("aiEndpoint", &endpoint) || !read("overrideAiSelection", &override) {
		return 1
	}
	if slices.Contains(apps, "ai") {
		fmt.Fprintln(stdout, "Local AI is already on. A rebuild picks its model if it has none yet.")
		return 0
	}
	if endpoint == "" && !override {
		ramGB, err := aiMemoryGB()
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		if !profile.LocalFits(ramGB) {
			fmt.Fprintf(stderr, "ERROR: "+noLocalModel+"\n", ramGB)
			return 1
		}
	}
	request, err := json.Marshal(append(apps, "ai"))
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return settingsRequest(ctx, bus, map[string]json.RawMessage{"apps": request}, stdout, stderr)
}

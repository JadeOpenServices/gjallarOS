package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/ai/profile"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func stubLocalAI(t *testing.T, result profile.Result) {
	t.Helper()
	old := detectLocalAI
	detectLocalAI = func(context.Context, profile.Override) (profile.Result, error) { return result, nil }
	t.Cleanup(func() { detectLocalAI = old })
}

// A rerun keeps the running apps; when no local model fits this machine,
// an interactive run turns local AI off and says so instead of stopping.
func TestPickLocalAIRerunWithoutFittingModelTurnsAIOff(t *testing.T) {
	stubLocalAI(t, profile.Result{RAMGB: 4})
	u := config.User{Apps: []string{"firefox", "ai", "vscodium"}}
	var out bytes.Buffer
	ai, err := pickLocalAI(context.Background(), &u, false, &out)
	if err != nil || ai.Model != "" {
		t.Fatalf("ai=%+v err=%v", ai, err)
	}
	if !slices.Equal(u.Apps, []string{"firefox", "vscodium"}) {
		t.Fatalf("apps = %v", u.Apps)
	}
	if !strings.Contains(out.String(), "Local AI is turned off: no local model fits 4 GiB RAM") || !strings.Contains(out.String(), "gjallarctl ai enable") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestPickLocalAIPresetWithoutFittingModelStops(t *testing.T) {
	stubLocalAI(t, profile.Result{RAMGB: 4})
	u := config.User{Apps: []string{"ai"}}
	if _, err := pickLocalAI(context.Background(), &u, true, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "the preset enables local AI") {
		t.Fatalf("err = %v", err)
	}
	if !u.HasApp("ai") {
		t.Fatal("a preset must keep its own apps")
	}
}

func TestPickLocalAIKeepsFittingModelAndEndpoint(t *testing.T) {
	stubLocalAI(t, profile.Result{Model: "small", RAMGB: 16})
	u := config.User{Apps: []string{"ai"}}
	if ai, err := pickLocalAI(context.Background(), &u, false, &bytes.Buffer{}); err != nil || ai.Model != "small" || !u.HasApp("ai") {
		t.Fatalf("ai=%+v err=%v apps=%v", ai, err, u.Apps)
	}
	stubLocalAI(t, profile.Result{RAMGB: 4})
	u = config.User{Apps: []string{"ai"}, AIEndpoint: "https://ai.example.org"}
	if ai, err := pickLocalAI(context.Background(), &u, false, &bytes.Buffer{}); err != nil || ai.Model != "" || !u.HasApp("ai") {
		t.Fatalf("endpoint: ai=%+v err=%v apps=%v", ai, err, u.Apps)
	}
}

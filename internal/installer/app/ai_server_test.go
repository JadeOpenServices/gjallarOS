package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func TestCollectAIServerRetriesUntilValid(t *testing.T) {
	var output bytes.Buffer
	user := config.User{OverrideAISelection: true, OverrideModelWith: "qwen2.5-coder:7b"}
	answers := []string{
		"http://192.168.8.205:11434", "qwen3-coder:30b", "32768",
		"https://192.168.8.205/v1", "qwen3-coder:30b", "32768",
		"https://192.168.8.205/", "", "",
	}
	ui := prompt.New(strings.NewReader(strings.Join(answers, "\n")+"\n"), &output)
	if err := collectAIServer(context.Background(), ui, nil, &user); err != nil {
		t.Fatal(err)
	}
	if user.AIEndpoint != "https://192.168.8.205" || user.AIRemoteModel != "qwen3-coder:30b" || user.AIRemoteContextTokens != 32768 {
		t.Fatalf("central server = %q %q %d", user.AIEndpoint, user.AIRemoteModel, user.AIRemoteContextTokens)
	}
	if user.OverrideAISelection || user.OverrideModelWith != "" {
		t.Fatal("local model override kept for a central server")
	}
	if !strings.Contains(output.String(), "must be an https URL") {
		t.Fatalf("http rejection not shown:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "scheme://host[:port] only") {
		t.Fatalf("path rejection not shown:\n%s", output.String())
	}
}

func TestCollectAILowMemory(t *testing.T) {
	defer func(f func() (int, error)) { aiMemoryGB = f }(aiMemoryGB)
	aiMemoryGB = func() (int, error) { return 3, nil }
	tests := []struct {
		name    string
		answers []string
		apps    []string
		model   string
	}{
		{"default turns AI off", []string{""}, []string{"git"}, ""},
		{"local needs a named model", []string{"local", "", ""}, []string{"git", "ai"}, "qwen2.5-coder:1.5b"},
		{"bad model asked again", []string{"local", "-x y", "", ""}, []string{"git", "ai"}, "qwen2.5-coder:1.5b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			user := config.User{Apps: []string{"git", "ai"}}
			ui := prompt.New(strings.NewReader(strings.Join(test.answers, "\n")+"\n"), &output)
			if err := collectAI(context.Background(), ui, nil, &user); err != nil {
				t.Fatal(err)
			}
			if strings.Join(user.Apps, ",") != strings.Join(test.apps, ",") || user.OverrideModelWith != test.model {
				t.Fatalf("apps=%v model=%q\n%s", user.Apps, user.OverrideModelWith, output.String())
			}
			if test.model != "" && !user.OverrideAISelection {
				t.Fatal("local model on low memory not marked as user choice")
			}
		})
	}
}

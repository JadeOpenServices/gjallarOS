package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

// runningFixture is a made-up installed system whose answers all differ
// from the new-install defaults.
func runningFixture(t *testing.T) config.User {
	t.Helper()
	u, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	u.Hostname, u.Username = "example-laptop", "alex"
	u.Name, u.Email, u.GitHubUsername = "Alex Example", "alex@example.com", "alex-example"
	u.Timezone, u.Locale = "Europe/Vienna", "de_AT.UTF-8"
	u.KeyboardLayout, u.KeyboardVariant = "us", "intl"
	u.TouchpadWorkspaceSwipe, u.ClamshellEnable = false, false
	u.DotfilesDir = "/etc/gjallar/system"
	u.PrintingEnable, u.NetworkPrintingEnable = true, true
	u.USBGuardEnable, u.USBTrustEnforce, u.USBTrustLapseDays = true, true, 14
	u.USBTrustTPMHandle = "0x81000043"
	u.Theme = "example-theme"
	u.NextcloudEnable, u.NextcloudHost = true, "https://cloud.example.test"
	u.Apps = append(slices.DeleteFunc(slices.Clone(u.Apps), func(id string) bool { return id == "ai" || id == "tailscale" }), "ai", "tailscale")
	slices.Sort(u.Apps)
	u.AIEndpoint, u.AIRemoteModel, u.AIRemoteContextTokens = "https://ai.example.test", "example-model", 8192
	u.AIAgentMode = "owner-conservative"
	u.Tailscale = &config.TailscaleIntent{
		HomeSubnets:   []string{"10.1.0.0/24"},
		ExitNode:      "exit-example",
		TrustedWifis:  []string{"cafe, upstairs", "home"},
		WifiExitNodes: map[string]string{"cafe, upstairs": "other-exit"},
		RouterHome:    true,
		// SplitList of an empty answer gives an empty list, not nil.
		SiteRouterTargets: []string{},
	}
	return u
}

func TestRerunInterviewOffersRunningAnswers(t *testing.T) {
	original := aiMemoryGB
	t.Cleanup(func() { aiMemoryGB = original })
	aiMemoryGB = func() (int, error) { return 32, nil }

	running := runningFixture(t)
	u, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	o := discovery.Options{Shells: []string{"zsh", "bash"}, Editors: []string{"vscodium", "neovim"}, Browsers: []string{"firefox"}, Themes: []string{"noctalia"}}
	running.Shell, running.Editors, running.PreferredEditor = "bash", []string{"neovim"}, "neovim"

	var output bytes.Buffer
	ui := prompt.New(strings.NewReader(strings.Repeat("\n", 200)), &output)
	if err := collectInteractive(context.Background(), ui, "/home/alex/gjallarOS", true, "", true, true, "SYNTHETIC: no JODS in this test", discovery.Hardware{}, o, &running, &u); err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}

	for _, field := range []string{
		"Hostname", "Username", "Name", "Email", "GitHubUsername", "Timezone", "Locale",
		"KeyboardLayout", "KeyboardVariant", "TouchpadWorkspaceSwipe", "ClamshellEnable",
		"DotfilesDir", "Shell", "Editors", "PreferredEditor", "PrintingEnable", "NetworkPrintingEnable",
		"USBGuardEnable", "USBTrustEnforce", "USBTrustLapseDays", "USBTrustTPMHandle", "Theme",
		"NextcloudEnable", "NextcloudHost", "Apps", "AIEndpoint", "AIRemoteModel",
		"AIRemoteContextTokens", "AIAgentMode", "Tailscale",
	} {
		got := reflect.ValueOf(u).FieldByName(field).Interface()
		want := reflect.ValueOf(running).FieldByName(field).Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v after Enter everywhere, want the running %#v", field, got, want)
		}
	}
	for _, prompt := range []string{"Username [alex]:", "Full name [Alex Example]:", "Git email [alex@example.com]:", "GitHub username [alex-example]:"} {
		if !strings.Contains(output.String(), prompt) {
			t.Errorf("no %q prompt; a personal question did not offer the running answer:\n%s", prompt, output.String())
		}
	}
}

func TestNewInstallInterviewKeepsPersonalValuesEmpty(t *testing.T) {
	a := interviewDefaults(nil, discovery.Options{Shells: []string{"zsh"}, Browsers: []string{"firefox"}})
	if a.Username != "" || a.Name != "" || a.Email != "" || a.GitHubUsername != "" {
		t.Fatalf("new-install defaults carry personal values: %+v", a)
	}
	if a.Hostname != "gjallarOS" || a.KeyboardLayout != "de" || !a.TouchpadWorkspaceSwipe {
		t.Fatalf("new-install defaults changed: %+v", a)
	}
}

func TestInterviewDefaultsDropWhatTheCheckoutNoLongerOffers(t *testing.T) {
	running := config.User{Shell: "fish", Editors: []string{"retired-editor"}, Browsers: []string{"firefox", "retired-browser"}}
	a := interviewDefaults(&running, discovery.Options{Shells: []string{"zsh"}, Editors: []string{"vscodium"}, Browsers: []string{"firefox"}})
	if a.Shell != "zsh" || !reflect.DeepEqual(a.Editors, []string{"vscodium"}) || !reflect.DeepEqual(a.Browsers, []string{"firefox"}) {
		t.Fatalf("defaults offer values the options lack: %+v", a)
	}
	if a.Hostname != "gjallarOS" || a.Timezone != "Europe/Berlin" {
		t.Fatalf("empty running values did not fall back: %+v", a)
	}
}

func TestReadRunningAnswers(t *testing.T) {
	checkout := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.WriteFile(repository, []byte(checkout+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunningAnswers(context.Background(), repository); err == nil {
		t.Fatal("a checkout without user.config.json gave answers")
	}
	want := runningFixture(t)
	if err := config.WriteAtomic(filepath.Join(checkout, "user.config.json"), want); err != nil {
		t.Fatal(err)
	}
	got, err := readRunningAnswers(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != want.Hostname || got.Username != want.Username || got.KeyboardVariant != want.KeyboardVariant {
		t.Fatalf("read %+v", got)
	}

	if err := os.WriteFile(repository, []byte("relative/checkout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunningAnswers(context.Background(), repository); err == nil {
		t.Fatal("a relative checkout path was read")
	}
	if _, err := readRunningAnswers(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing repository file gave answers")
	}
}

func TestReadRunningAnswersAsksRootForAPrivateFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	checkout := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.WriteFile(repository, []byte(checkout), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkout, "user.config.json")
	if err := config.WriteAtomic(path, runningFixture(t)); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	original := readPrivileged
	t.Cleanup(func() { readPrivileged = original })
	asked := ""
	readPrivileged = func(_ context.Context, p string) ([]byte, error) {
		asked = p
		return contents, nil
	}
	if _, err := readRunningAnswers(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	if asked != path {
		t.Fatalf("asked root for %q, want %q", asked, path)
	}
}

func TestJoinListRoundTrips(t *testing.T) {
	items := []string{"home", "cafe, upstairs", " padded ", `"quoted`, "plain"}
	got, err := config.SplitList(config.JoinList(items))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, items) {
		t.Fatalf("SplitList(JoinList(%q)) = %q", items, got)
	}
}

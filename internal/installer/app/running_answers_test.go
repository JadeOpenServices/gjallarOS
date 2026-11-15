package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/region"
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
	u.SettingsSigningKey = syntheticSigningKey()
	u.NextcloudEnable, u.NextcloudHost = true, "https://cloud.example.test"
	u.ThunderbirdSmartCard = true
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
	running := runningFixture(t)
	u, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	o := discovery.Options{Shells: []string{"zsh", "bash"}, Editors: []string{"vscodium", "neovim"}, Browsers: []string{"firefox"}, Themes: []string{"noctalia"}}
	running.Shell, running.Editors, running.PreferredEditor = "bash", []string{"neovim"}, "neovim"
	stubLiveRegion(t, region.Session{Timezone: "Asia/Tokyo", Locale: "ja_JP.UTF-8", KeyboardLayout: "jp"})

	var output bytes.Buffer
	ui := prompt.New(strings.NewReader(strings.Repeat("\n", 200)), &output)
	if err := collectInteractive(context.Background(), ui, "/home/alex/gjallarOS", "SYNTHETIC: no JODS in this test", discovery.Hardware{}, o, &running, &u); err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}

	for _, field := range []string{
		"Hostname", "Username", "Name", "Email", "GitHubUsername", "Timezone", "Locale",
		"KeyboardLayout", "KeyboardVariant", "TouchpadWorkspaceSwipe", "ClamshellEnable",
		"DotfilesDir", "Shell", "Editors", "PreferredEditor", "Browsers", "PreferredBrowser", "PrintingEnable", "NetworkPrintingEnable",
		"USBTrustTPMHandle", "Theme", "SettingsSigningKey",
		"NextcloudEnable", "NextcloudHost", "Apps", "AIEndpoint", "AIRemoteModel",
		"AIRemoteContextTokens", "AIAgentMode", "Tailscale", "ThunderbirdSmartCard",
	} {
		got := reflect.ValueOf(u).FieldByName(field).Interface()
		want := reflect.ValueOf(running).FieldByName(field).Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v after Enter everywhere, want the running %#v", field, got, want)
		}
	}
	if !strings.Contains(output.String(), "Username [alex]:") {
		t.Errorf("the username question did not offer the running answer:\n%s", output.String())
	}
	assertNoDroppedQuestions(t, output.String())
}

// droppedQuestions left the installer; Settings changes their values.
var droppedQuestions = []string{
	"Full name", "Git email", "GitHub username", "Login shell", "Editors", "Browsers",
	"Preferred editor", "Preferred browser", "workspace swipes", "clamshell",
	// Q-L: gjallarOS signs; an own key is a Settings > Security change.
	"signing key", "OpenSSH",
	// Q-K: approvals expire after 30 days; Settings changes it.
	"expire when unused", "Days without use",
	// Q-O: one summary and "Install now?" replace these.
	"Write configuration to", "Install the next NixOS boot generation",
}

func assertNoDroppedQuestions(t *testing.T, output string) {
	t.Helper()
	for _, question := range droppedQuestions {
		if strings.Contains(output, question) {
			t.Errorf("the installer still asks %q:\n%s", question, output)
		}
	}
}

func TestNewInstallInterviewSetsDroppedValues(t *testing.T) {
	o := discovery.Options{Shells: []string{"zsh"}, Editors: []string{"vscode", "vscodium"}, Browsers: []string{"firefox"}, Themes: []string{"noctalia"}}
	for _, form := range []struct {
		factor    string
		clamshell bool
	}{{"laptop", true}, {"desktop", false}, {"", false}} {
		u, err := config.Defaults()
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		// Region: Enter three times; Username: alex (required on a new
		// install); then Enter.
		stubLiveRegion(t, region.Session{})
		ui := prompt.New(strings.NewReader("\n\n\nalex\n"+strings.Repeat("\n", 200)), &output)
		if err := collectInteractive(context.Background(), ui, "/home/alex/gjallarOS", "SYNTHETIC: no JODS in this test", discovery.Hardware{FormFactor: form.factor}, o, nil, &u); err != nil {
			t.Fatalf("%s: %v\n%s", form.factor, err, output.String())
		}
		assertNoDroppedQuestions(t, output.String())
		if u.Username != "alex" || u.Hostname != "gjallarOS" || u.Timezone != "Etc/UTC" || u.KeyboardLayout != "us" {
			t.Errorf("new install: user=%q host=%q tz=%q kbd=%q", u.Username, u.Hostname, u.Timezone, u.KeyboardLayout)
		}
		if strings.Index(output.String(), "Timezone") > strings.Index(output.String(), "Username") ||
			strings.Index(output.String(), "Username") > strings.Index(output.String(), "Hostname") {
			t.Errorf("questions are not in the O1 order (region, username, hostname):\n%s", output.String())
		}
		if u.ClamshellEnable != form.clamshell || !u.TouchpadWorkspaceSwipe {
			t.Errorf("%q chassis: clamshell=%v swipes=%v, want clamshell=%v swipes=true", form.factor, u.ClamshellEnable, u.TouchpadWorkspaceSwipe, form.clamshell)
		}
		if u.Name != "" || u.Email != "" || u.GitHubUsername != "" {
			t.Errorf("new install filled personal values: %q %q %q", u.Name, u.Email, u.GitHubUsername)
		}
		if u.Shell != "zsh" || !reflect.DeepEqual(u.Editors, []string{"vscodium"}) || !reflect.DeepEqual(u.Browsers, []string{"firefox"}) ||
			u.PreferredEditor != "vscodium" || u.PreferredBrowser != "firefox" {
			t.Errorf("new install apps: shell=%q editors=%v browsers=%v preferred=%q/%q", u.Shell, u.Editors, u.Browsers, u.PreferredEditor, u.PreferredBrowser)
		}
	}
}

func TestNewInstallInterviewKeepsPersonalValuesEmpty(t *testing.T) {
	a := interviewDefaults(nil, discovery.Options{Shells: []string{"zsh"}, Browsers: []string{"firefox"}})
	if a.Username != "" || a.Name != "" || a.Email != "" || a.GitHubUsername != "" {
		t.Fatalf("new-install defaults carry personal values: %+v", a)
	}
	if a.Hostname != "gjallarOS" || a.KeyboardLayout != "us" || a.Timezone != "Etc/UTC" || !a.TouchpadWorkspaceSwipe {
		t.Fatalf("new-install defaults changed: %+v", a)
	}
}

func TestInterviewDefaultsDropWhatTheCheckoutNoLongerOffers(t *testing.T) {
	running := config.User{Shell: "fish", Editors: []string{"retired-editor"}, Browsers: []string{"firefox", "retired-browser"}}
	a := interviewDefaults(&running, discovery.Options{Shells: []string{"zsh"}, Editors: []string{"vscodium"}, Browsers: []string{"firefox"}})
	if a.Shell != "zsh" || !reflect.DeepEqual(a.Editors, []string{"vscodium"}) || !reflect.DeepEqual(a.Browsers, []string{"firefox"}) {
		t.Fatalf("defaults offer values the options lack: %+v", a)
	}
	if a.Hostname != "gjallarOS" || a.Timezone != "Etc/UTC" {
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

// syntheticSigningKey is 32 bytes of "k" as an Ed25519 key, not a real key.
func syntheticSigningKey() string {
	blob := []byte{0, 0, 0, 11}
	blob = append(blob, "ssh-ed25519"...)
	blob = append(blob, 0, 0, 0, 32)
	blob = append(blob, strings.Repeat("k", 32)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " SYNTHETIC"
}

// stubLiveRegion makes the region step see session instead of the machine
// the test runs on.
func stubLiveRegion(t *testing.T, session region.Session) {
	t.Helper()
	old := liveRegion
	liveRegion = func(context.Context) region.Session { return session }
	t.Cleanup(func() { liveRegion = old })
}

// setRegionLists points the region lists at small made-up tables.
func setRegionLists(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("zone.tab", "AT\t+4813+01620\tEurope/Vienna\nJP\t+353916+1394441\tAsia/Tokyo\nJP\t+0000+00000\tAsia/Example\n")
	locales := write("SUPPORTED", "de_AT.UTF-8/UTF-8 \\\nja_JP.UTF-8/UTF-8 \\\n")
	rules := write("base.lst", "! layout\n  us  English (US)\n  jp  Japanese\n  at  Example\n")
	oldZone, oldLocale, oldRules := region.ZoneDir, region.LocaleList, region.XKBRules
	region.ZoneDir, region.LocaleList, region.XKBRules = dir, locales, rules
	t.Cleanup(func() { region.ZoneDir, region.LocaleList, region.XKBRules = oldZone, oldLocale, oldRules })
}

func TestRegionStepStartsFromTheLiveSession(t *testing.T) {
	setRegionLists(t)
	for _, tc := range []struct {
		name               string
		live               region.Session
		input              string
		tz, locale, layout string
	}{
		{"live values", region.Session{Timezone: "Asia/Tokyo", Locale: "ja_JP.UTF-8", KeyboardLayout: "jp"}, "\n\n\n", "Asia/Tokyo", "ja_JP.UTF-8", "jp"},
		{"nothing live", region.Session{}, "\n\n\n", "Etc/UTC", "en_US.UTF-8", "us"},
		{"unknown live values", region.Session{Timezone: "../../etc/shadow", Locale: "xx_XX.UTF-8", KeyboardLayout: "$(id)"}, "\n\n\n", "Etc/UTC", "en_US.UTF-8", "us"},
		{"search", region.Session{}, "Tokyo\nja\njp\n", "Asia/Tokyo", "ja_JP.UTF-8", "jp"},
		{"search with several matches", region.Session{}, "Asia\n1\n\n\n", "Asia/Example", "en_US.UTF-8", "us"},
	} {
		stubLiveRegion(t, tc.live)
		var output bytes.Buffer
		u := config.User{}
		ui := prompt.New(strings.NewReader(tc.input), &output)
		if err := collectRegion(context.Background(), ui, nil, interviewDefaults(nil, discovery.Options{}), &u); err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, output.String())
		}
		if u.Timezone != tc.tz || u.Locale != tc.locale || u.KeyboardLayout != tc.layout || u.KeyboardVariant != "" {
			t.Errorf("%s: got %q %q %q/%q, want %q %q %q\n%s", tc.name, u.Timezone, u.Locale, u.KeyboardLayout, u.KeyboardVariant, tc.tz, tc.locale, tc.layout, output.String())
		}
	}
}

func TestRegionStepRerunKeepsRunningValues(t *testing.T) {
	setRegionLists(t)
	stubLiveRegion(t, region.Session{Timezone: "Asia/Tokyo", Locale: "ja_JP.UTF-8", KeyboardLayout: "jp"})
	// "de,us" and Europe/Berlin are not in the made-up lists; a rerun still
	// keeps them, and the running variant stays with its layout.
	running := config.User{Timezone: "Europe/Berlin", Locale: "de_AT.UTF-8", KeyboardLayout: "de,us", KeyboardVariant: "nodeadkeys,"}
	var output bytes.Buffer
	u := config.User{}
	if err := collectRegion(context.Background(), prompt.New(strings.NewReader("\n\n\n"), &output), &running, interviewDefaults(&running, discovery.Options{}), &u); err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}
	if u.Timezone != running.Timezone || u.Locale != running.Locale || u.KeyboardLayout != running.KeyboardLayout || u.KeyboardVariant != running.KeyboardVariant {
		t.Fatalf("rerun changed the region: %+v\n%s", u, output.String())
	}
	// A new layout drops the running variant.
	u = config.User{}
	if err := collectRegion(context.Background(), prompt.New(strings.NewReader("\n\njp\n"), &output), &running, interviewDefaults(&running, discovery.Options{}), &u); err != nil {
		t.Fatal(err)
	}
	if u.KeyboardLayout != "jp" || u.KeyboardVariant != "" {
		t.Fatalf("new layout kept the old variant: %q/%q", u.KeyboardLayout, u.KeyboardVariant)
	}
}

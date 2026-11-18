package move

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

// oldSettings is what the helper hands out for an old disk: a fresh
// install's settings with made-up personal and device values.
func oldSettings(t *testing.T) config.User {
	t.Helper()
	u, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	u.Username = "example"
	u.Name = "Example Person"
	u.Hostname = "example-old"
	u.Theme = "nord"
	return u
}

func settingsFlow(mode string, answer any) (Flow, *bytes.Buffer) {
	var out bytes.Buffer
	h := &fakeHelper{answers: map[string][]any{"config": {answer}}}
	return Flow{UI: prompt.New(strings.NewReader(""), &out), Helper: h, Mode: mode}, &out
}

func TestSettingsModes(t *testing.T) {
	fresh, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	old := oldSettings(t)
	for _, c := range []struct {
		mode, hostname string
	}{
		{NewDisk, "example-old"},
		{BringOver, fresh.Hostname},
	} {
		f, _ := settingsFlow(c.mode, movehelper.ConfigResult{User: old})
		got, err := f.Settings(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", c.mode, err)
		}
		if got.Username != "example" || got.Name != "Example Person" || got.Theme != "nord" {
			t.Errorf("%s: personal keys lost: %+v", c.mode, got)
		}
		if got.Hostname != c.hostname {
			t.Errorf("%s: hostname %q, want %q", c.mode, got.Hostname, c.hostname)
		}
	}
}

// The installer checks the helper's answer again: values the schema refuses
// stop the move with the bad-config text, even when the helper sent them.
func TestSettingsRecheck(t *testing.T) {
	bad := oldSettings(t)
	bad.Theme = "../SYNTHETIC"
	managed := oldSettings(t)
	managed.Management.Enable = true
	for name, c := range map[string]struct {
		user   config.User
		reason oldroot.Reason
	}{
		"theme is a path": {bad, oldroot.BadConfig},
		"managed":         {managed, oldroot.Managed},
	} {
		got := marks(t)
		f, out := settingsFlow(NewDisk, movehelper.ConfigResult{User: c.user})
		if _, err := f.Settings(context.Background()); !errors.Is(err, ErrStopped) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if !strings.Contains(out.String(), "settings file") && c.reason == oldroot.BadConfig {
			t.Errorf("%s: text not shown:\n%s", name, out)
		}
		if m := got(); !strings.Contains(m, "reason="+string(c.reason)) {
			t.Errorf("%s: marks:\n%s", name, m)
		}
	}
}

func TestSettingsRefused(t *testing.T) {
	got := marks(t)
	f, out := settingsFlow(BringOver, refusal(oldroot.BadConfig))
	if _, err := f.Settings(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "text for bad-config") {
		t.Errorf("text not shown:\n%s", out)
	}
	if m := got(); !strings.Contains(m, "bad-config") {
		t.Errorf("marks:\n%s", m)
	}
}

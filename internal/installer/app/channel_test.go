package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func TestApplyChannelFlag(t *testing.T) {
	user := config.User{}
	if err := applyChannelFlag(&user, "", false, "p"); err != nil || user.ReleaseChannel != "" {
		t.Fatalf("no flag: %v %q", err, user.ReleaseChannel)
	}
	if err := applyChannelFlag(&user, "staging", false, "p"); err != nil || user.ReleaseChannel != "staging" {
		t.Fatalf("interactive: %v %q", err, user.ReleaseChannel)
	}
	if err := applyChannelFlag(&user, "nightly", false, "p"); err == nil {
		t.Fatal("unknown channel accepted")
	}
	preset := config.User{ReleaseChannel: "dev"}
	if err := applyChannelFlag(&preset, "dev", true, "p"); err != nil {
		t.Fatalf("matching preset: %v", err)
	}
	err := applyChannelFlag(&preset, "stable", true, "/r/user.config.json")
	if err == nil || !strings.Contains(err.Error(), "follows the dev channel") || preset.ReleaseChannel != "dev" {
		t.Fatalf("conflicting preset: %v %q", err, preset.ReleaseChannel)
	}
	// An older preset without releaseChannel follows stable.
	old := config.User{}
	if err := applyChannelFlag(&old, "staging", true, "p"); err == nil {
		t.Fatal("flag silently changed a preset's channel")
	}
}

func TestDeveloperConfirmNotice(t *testing.T) {
	stable := config.User{ReleaseChannel: "stable"}
	staging := config.User{ReleaseChannel: "staging"}
	devMode := config.User{ReleaseChannel: "stable", DeveloperMode: true}
	if got := developerConfirmNotice(stable, false, true); got != "" {
		t.Fatalf("stable: %q", got)
	}
	if got := developerConfirmNotice(staging, true, false); got != "" {
		t.Fatalf("existing install: %q", got)
	}
	if got := developerConfirmNotice(staging, false, true); !strings.Contains(got, "first start confirms the staging channel") {
		t.Fatalf("chip unlock: %q", got)
	}
	if got := developerConfirmNotice(staging, false, false); !strings.Contains(got, "confirm-developer") {
		t.Fatalf("no chip unlock: %q", got)
	}
	if got := developerConfirmNotice(devMode, false, true); !strings.Contains(got, "developer mode") {
		t.Fatalf("developer mode: %q", got)
	}
}

type fakeConfirm struct {
	answer bool
	asked  int
}

func (f *fakeConfirm) Confirm(context.Context, string, bool) (bool, error) {
	f.asked++
	return f.answer, nil
}

func TestStableOnUnencryptedDisk(t *testing.T) {
	ctx := context.Background()
	var out strings.Builder

	// Encrypted disk or stable without developer mode: nothing to ask.
	for _, c := range []struct {
		user      config.User
		encrypted bool
	}{
		{config.User{ReleaseChannel: "staging"}, true},
		{config.User{ReleaseChannel: "dev", DeveloperMode: true}, true},
		{config.User{ReleaseChannel: "stable"}, false},
		{config.User{}, false},
	} {
		ui := &fakeConfirm{}
		user := c.user
		if err := stableOnUnencryptedDisk(ctx, ui, &out, c.encrypted, &user, false, "p"); err != nil || ui.asked != 0 || user.ReleaseChannel != c.user.ReleaseChannel || user.DeveloperMode != c.user.DeveloperMode {
			t.Fatalf("%+v encrypted=%v: err=%v asked=%d user=%+v", c.user, c.encrypted, err, ui.asked, user)
		}
	}

	// Plain disk, staging, person says no: refused, nothing changed.
	user := config.User{ReleaseChannel: "staging"}
	err := stableOnUnencryptedDisk(ctx, &fakeConfirm{}, &out, false, &user, false, "p")
	if err == nil || !strings.Contains(err.Error(), "the staging channel needs an encrypted system disk; nothing was changed") || !strings.Contains(err.Error(), "--channel stable") || user.ReleaseChannel != "staging" {
		t.Fatalf("refusal: %v %+v", err, user)
	}

	// Preset with developer mode: the fix names the preset file.
	user = config.User{ReleaseChannel: "stable", DeveloperMode: true}
	err = stableOnUnencryptedDisk(ctx, &fakeConfirm{}, &out, false, &user, true, "/r/user.config.json")
	if err == nil || !strings.Contains(err.Error(), "developer mode needs") || !strings.Contains(err.Error(), "/r/user.config.json") || !user.DeveloperMode {
		t.Fatalf("preset refusal: %v %+v", err, user)
	}

	// Person takes stable: channel and developer mode switch.
	out.Reset()
	user = config.User{ReleaseChannel: "dev", DeveloperMode: true}
	ui := &fakeConfirm{answer: true}
	if err := stableOnUnencryptedDisk(ctx, ui, &out, false, &user, false, "p"); err != nil || ui.asked != 1 {
		t.Fatalf("accept: %v asked=%d", err, ui.asked)
	}
	if user.ReleaseChannel != "stable" || user.DeveloperMode {
		t.Fatalf("accept left %+v", user)
	}
	if !strings.Contains(out.String(), "The dev channel and developer mode need an encrypted disk") {
		t.Fatalf("why text: %q", out.String())
	}
	// Preset accepted: the file says stable, so the rebuild agrees.
	path := filepath.Join(t.TempDir(), "user.config.json")
	user, err = config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	user.ReleaseChannel, user.DeveloperMode, user.Hostname = "staging", true, "example"
	if err := config.WriteAtomic(path, user); err != nil {
		t.Fatal(err)
	}
	if err := stableOnUnencryptedDisk(ctx, &fakeConfirm{answer: true}, &out, false, &user, true, path); err != nil {
		t.Fatalf("preset accept: %v", err)
	}
	saved, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ReleaseChannel != "stable" || saved.DeveloperMode || saved.Hostname != "example" {
		t.Fatalf("preset file left %+v", saved)
	}
}

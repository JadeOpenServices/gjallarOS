package app

import (
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

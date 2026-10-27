package config

import (
	"encoding/json"
	"os"
	"testing"
)

func presetFields(t *testing.T) map[string]any {
	t.Helper()
	preset, err := os.ReadFile("../../../scripts/installation/user_PresetJSON/default.user.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(preset, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestParseOldConfigGetsSnapshotDefaults(t *testing.T) {
	fields := presetFields(t)
	delete(fields, "snapshotsEnable")
	delete(fields, "snapshotsKeepHours")
	delete(fields, "snapshotsKeepDays")
	contents, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	user, err := Parse(contents)
	if err != nil {
		t.Fatalf("Parse() rejected a config without snapshot keys: %v", err)
	}
	if !user.SnapshotsEnable || user.SnapshotsKeepHours != DefaultSnapshotsKeepHours || user.SnapshotsKeepDays != DefaultSnapshotsKeepDays {
		t.Fatalf("snapshot defaults = %v %d %d", user.SnapshotsEnable, user.SnapshotsKeepHours, user.SnapshotsKeepDays)
	}
}

func TestParseKeepsSnapshotsOff(t *testing.T) {
	fields := presetFields(t)
	fields["snapshotsEnable"] = false
	contents, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	user, err := Parse(contents)
	if err != nil {
		t.Fatal(err)
	}
	if user.SnapshotsEnable {
		t.Fatal("snapshotsEnable false was turned back on")
	}
}

func TestValidateSnapshotRetention(t *testing.T) {
	base, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(base); err != nil {
		t.Fatalf("default snapshot retention rejected: %v", err)
	}
	for _, edit := range []func(*User){
		func(u *User) { u.SnapshotsKeepHours = -1 },
		func(u *User) { u.SnapshotsKeepHours = MaxSnapshotsKeepHours + 1 },
		func(u *User) { u.SnapshotsKeepDays = 0 },
		func(u *User) { u.SnapshotsKeepDays = MaxSnapshotsKeepDays + 1 },
	} {
		got := base
		edit(&got)
		if err := Validate(got); err == nil {
			t.Errorf("retention %d h %d d accepted", got.SnapshotsKeepHours, got.SnapshotsKeepDays)
		}
	}
	off := base
	off.SnapshotsEnable = false
	off.SnapshotsKeepDays = 0
	if err := Validate(off); err != nil {
		t.Fatalf("retention checked while snapshots are off: %v", err)
	}
}

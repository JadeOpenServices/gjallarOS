package config

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// presetWithoutManagement is the shipped preset with the new management and
// prebootLock keys taken out and overlay added, as an older file would look.
func presetWithoutManagement(t *testing.T, overlay string) []byte {
	t.Helper()
	preset, err := os.ReadFile("../../../scripts/installation/user_PresetJSON/default.user.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(preset, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "management")
	delete(fields, "prebootLock")
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(overlay), &extra); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		fields[key] = value
	}
	contents, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func TestParseMovesOldManagementKeys(t *testing.T) {
	// EXAMPLE values; the keys are made up.
	contents := presetWithoutManagement(t, `{
		"endpointManagedDevice": true,
		"jodsEndpoint": "https://jods.example.test",
		"jodsPolicySigningPublicKey": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"jodsRecoveryCommandSigningPublicKey": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"jodsEnrollmentMode": "auto",
		"jodsAllowInsecureTls": false,
		"jodsDeviceClass": "workstation",
		"jodsDesktopProfile": "office",
		"jodsFingerprintEnrollmentAllowed": true,
		"jodsPrebootLockEnable": true
	}`)
	user, err := Parse(contents)
	if err != nil {
		t.Fatal(err)
	}
	want := ManagementIntent{
		Enable:                          true,
		Endpoint:                        "https://jods.example.test",
		PolicySigningPublicKey:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecoveryCommandSigningPublicKey: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		EnrollmentMode:                  "auto",
		DeviceClass:                     "workstation",
		DesktopProfile:                  "office",
		FingerprintEnrollmentAllowed:    true,
	}
	if user.Management != want {
		t.Fatalf("management = %+v, want %+v", user.Management, want)
	}
	if !user.PrebootLock.Enable {
		t.Fatal("jodsPrebootLockEnable was not moved to prebootLock.enable")
	}
}

// A standalone install from before the rename holds the off switch and the
// preset values. It must load unchanged.
func TestParseOldStandaloneFileKeepsDefaults(t *testing.T) {
	user, err := Parse(presetWithoutManagement(t, `{
		"endpointManagedDevice": false,
		"jodsAllowInsecureTls": false,
		"jodsDesktopProfile": "headless",
		"jodsDeviceClass": "laptop",
		"jodsEndpoint": "",
		"jodsEnrollmentMode": "manual",
		"jodsFingerprintEnrollmentAllowed": false,
		"jodsPolicySigningPublicKey": "",
		"jodsPrebootLockEnable": false,
		"jodsRecoveryCommandSigningPublicKey": ""
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := ManagementIntent{EnrollmentMode: "manual", DeviceClass: "laptop", DesktopProfile: "headless"}
	if user.Management != want || user.PrebootLock.Enable {
		t.Fatalf("management = %+v, prebootLock = %+v; want the defaults", user.Management, user.PrebootLock)
	}
}

func TestParseRefusesOldAndNewManagementKeys(t *testing.T) {
	for _, overlay := range []string{
		`{"management": {"enable": false}, "jodsEndpoint": "https://jods.example.test"}`,
		`{"prebootLock": {"enable": false}, "jodsPrebootLockEnable": true}`,
	} {
		_, err := Parse(presetWithoutManagement(t, overlay))
		if err == nil || !strings.Contains(err.Error(), "both set") {
			t.Fatalf("%s: err = %v, want a refusal", overlay, err)
		}
	}
}

func TestLegacyManagementKeys(t *testing.T) {
	got := LegacyManagementKeys([]byte(`{"jodsPrebootLockEnable": true, "endpointManagedDevice": false, "theme": "obsidian"}`))
	if !slices.Equal(got, []string{"endpointManagedDevice", "jodsPrebootLockEnable"}) {
		t.Fatalf("LegacyManagementKeys = %v", got)
	}
	if got := LegacyManagementKeys([]byte(`{"management": {"enable": false}}`)); len(got) != 0 {
		t.Fatalf("new file lists old keys: %v", got)
	}
	if got := LegacyManagementName("jodsAllowInsecureTls"); got != "management.allowInsecureTls" {
		t.Fatalf("LegacyManagementName = %q", got)
	}
}

// After the move, writing the file back holds only the new names.
func TestOldManagementKeysWriteBackNested(t *testing.T) {
	user, err := Parse(presetWithoutManagement(t, `{"endpointManagedDevice": false, "jodsPrebootLockEnable": false}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	if keys := LegacyManagementKeys(data); len(keys) != 0 {
		t.Fatalf("written file still holds %v", keys)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["management"]; !ok {
		t.Fatal("written file has no management")
	}
	if _, ok := fields["prebootLock"]; !ok {
		t.Fatal("written file has no prebootLock")
	}
}

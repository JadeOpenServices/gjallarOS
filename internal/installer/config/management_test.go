package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The flat names from before JODS-565 are not read; a file that still
// holds one stops with an unknown-field error that names the new key from
// system/tools/legacy/renames.json (wiki 752 R39).
func TestParseRefusesOldManagementKeys(t *testing.T) {
	for key, now := range map[string]string{
		"endpointManagedDevice": "management.enable",
		"jodsEndpoint":          "management.endpoint",
		"jodsPrebootLockEnable": "prebootLock.enable",
	} {
		fields := presetFields(t)
		delete(fields, "management")
		delete(fields, "prebootLock")
		fields[key] = false
		contents, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		want := "unknown field: " + key + " is now " + now + "; move the value there in user.config.json"
		if _, err := Parse(contents); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", key, err, want)
		}
	}
}

// An old key is refused even when the new key is set too, and every old
// key in the file is named at once.
func TestParseNamesEveryOldManagementKey(t *testing.T) {
	fields := presetFields(t)
	fields["jodsEndpoint"] = json.RawMessage(`"https://jods.example.test"`)
	fields["jodsDeviceClass"] = json.RawMessage(`"workstation"`)
	contents, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	want := "jodsDeviceClass is now management.deviceClass, jodsEndpoint is now management.endpoint;"
	if _, err := Parse(contents); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A key the table does not know keeps the plain decoder error.
func TestParseUnknownKeyNotInRenames(t *testing.T) {
	fields := presetFields(t)
	fields["madeUpKey"] = json.RawMessage(`true`)
	contents, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(contents); err == nil || !strings.Contains(err.Error(), `json: unknown field "madeUpKey"`) {
		t.Fatalf("err = %v, want the decoder's unknown-field error", err)
	}
}

func TestParseReadsManagement(t *testing.T) {
	fields := presetFields(t)
	// EXAMPLE values; the keys are made up.
	fields["management"] = json.RawMessage(`{
		"enable": true,
		"endpoint": "https://jods.example.test",
		"policySigningPublicKey": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"recoveryCommandSigningPublicKey": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"enrollmentMode": "auto",
		"allowInsecureTls": false,
		"deviceClass": "workstation",
		"desktopProfile": "office",
		"fingerprintEnrollmentAllowed": true
	}`)
	fields["prebootLock"] = map[string]any{"enable": true}
	contents, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
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
	if user.Management != want || !user.PrebootLock.Enable {
		t.Fatalf("management = %+v, prebootLock = %+v", user.Management, user.PrebootLock)
	}
}

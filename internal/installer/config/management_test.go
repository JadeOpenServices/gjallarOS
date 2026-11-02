package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The flat names from before JODS-565 are not read; a file that still
// holds one stops with an unknown-field error.
func TestParseRefusesOldManagementKeys(t *testing.T) {
	for _, key := range []string{"endpointManagedDevice", "jodsEndpoint", "jodsPrebootLockEnable"} {
		fields := presetFields(t)
		delete(fields, "management")
		delete(fields, "prebootLock")
		fields[key] = false
		contents, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(contents); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("%s: err = %v, want an unknown-field error", key, err)
		}
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

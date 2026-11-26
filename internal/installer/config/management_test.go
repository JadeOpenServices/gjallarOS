package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The flat names from before JODS-565 are not read. Their rename rows
// left the legacy table after v2026.10.70, so a file that still holds one
// stops with the decoder's unknown-field error.
func TestParseRefusesOldManagementKeys(t *testing.T) {
	for _, key := range []string{"endpointManagedDevice", "jodsEndpoint", "jodsPrebootLockEnable"} {
		fields := presetFields(t)
		fields[key] = json.RawMessage(`false`)
		contents, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(contents); err == nil || !strings.Contains(err.Error(), `json: unknown field "`+key+`"`) {
			t.Fatalf("%s: err = %v, want the decoder's unknown-field error", key, err)
		}
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

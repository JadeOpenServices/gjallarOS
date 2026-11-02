package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ManagementIntent says whether a JODS server manages the device and how
// it reaches it (JODS-565). With Enable off the device is a standalone
// install and the other fields are not used.
type ManagementIntent struct {
	Enable                          bool   `json:"enable"`
	Endpoint                        string `json:"endpoint"`
	PolicySigningPublicKey          string `json:"policySigningPublicKey"`
	RecoveryCommandSigningPublicKey string `json:"recoveryCommandSigningPublicKey"`
	EnrollmentMode                  string `json:"enrollmentMode"`
	AllowInsecureTLS                bool   `json:"allowInsecureTls"`
	DeviceClass                     string `json:"deviceClass"`
	DesktopProfile                  string `json:"desktopProfile"`
	FingerprintEnrollmentAllowed    bool   `json:"fingerprintEnrollmentAllowed"`
}

// PrebootLockIntent turns on the JODS preboot lock.
type PrebootLockIntent struct {
	Enable bool `json:"enable"`
}

// Top-level keys before JODS-565 and where they live now. Parse reads them
// for one release; rebuild writes the file back with the new names.
var (
	legacyManagementKeys = map[string]string{
		"endpointManagedDevice":               "enable",
		"jodsEndpoint":                        "endpoint",
		"jodsPolicySigningPublicKey":          "policySigningPublicKey",
		"jodsRecoveryCommandSigningPublicKey": "recoveryCommandSigningPublicKey",
		"jodsEnrollmentMode":                  "enrollmentMode",
		"jodsAllowInsecureTls":                "allowInsecureTls",
		"jodsDeviceClass":                     "deviceClass",
		"jodsDesktopProfile":                  "desktopProfile",
		"jodsFingerprintEnrollmentAllowed":    "fingerprintEnrollmentAllowed",
	}
	legacyPrebootLockKeys = map[string]string{
		"jodsPrebootLockEnable": "enable",
	}
)

// LegacyManagementKeys lists the pre-JODS-565 keys that configuration text
// still holds, sorted. Rebuild uses it to write the file back once.
func LegacyManagementKeys(contents []byte) []string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(contents, &fields) != nil {
		return nil
	}
	return legacyManagementPresent(fields)
}

// LegacyManagementName is the new name of an old management key, such as
// management.endpoint for jodsEndpoint, or "" for any other key.
func LegacyManagementName(key string) string {
	if name, ok := legacyManagementKeys[key]; ok {
		return "management." + name
	}
	if name, ok := legacyPrebootLockKeys[key]; ok {
		return "prebootLock." + name
	}
	return ""
}

func legacyManagementPresent(fields map[string]json.RawMessage) []string {
	var keys []string
	for key := range fields {
		if LegacyManagementName(key) != "" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// migrateLegacyManagement moves the old flat keys under management and
// prebootLock. A file with both the old and the new form is refused: which
// one counts would be a guess, and these keys decide who controls the
// device.
func migrateLegacyManagement(fields map[string]json.RawMessage) error {
	for _, group := range []struct {
		key    string
		legacy map[string]string
	}{{"management", legacyManagementKeys}, {"prebootLock", legacyPrebootLockKeys}} {
		nested := map[string]json.RawMessage{}
		var old []string
		for oldKey, name := range group.legacy {
			if raw, ok := fields[oldKey]; ok {
				nested[name] = raw
				old = append(old, oldKey)
			}
		}
		if len(old) == 0 {
			continue
		}
		slices.Sort(old)
		if _, ok := fields[group.key]; ok {
			return fmt.Errorf("%s and the old %s are both set; remove the old keys", group.key, strings.Join(old, ", "))
		}
		raw, err := json.Marshal(nested)
		if err != nil {
			return err
		}
		fields[group.key] = raw
		for _, key := range old {
			delete(fields, key)
		}
	}
	return nil
}

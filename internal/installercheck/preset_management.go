package installercheck

import "encoding/json"

// matchesPresetManagement checks the management object (JODS-565).
func matchesPresetManagement(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false
	}
	for key, value := range fields {
		var expected presetValueType
		switch key {
		case "enable", "allowInsecureTls", "fingerprintEnrollmentAllowed":
			expected = presetBool
		case "endpoint", "policySigningPublicKey", "recoveryCommandSigningPublicKey",
			"enrollmentMode", "deviceClass", "desktopProfile":
			expected = presetString
		default:
			return false
		}
		if !matchesPresetType(value, expected) {
			return false
		}
	}
	return true
}

// matchesPresetPrebootLock checks the prebootLock object (JODS-565).
func matchesPresetPrebootLock(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false
	}
	for key, value := range fields {
		if key != "enable" || !matchesPresetType(value, presetBool) {
			return false
		}
	}
	return true
}

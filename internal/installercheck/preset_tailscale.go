package installercheck

import "encoding/json"

func matchesPresetTailscale(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false
	}

	for key, value := range fields {
		var expected presetValueType
		switch key {
		case "routerHome", "siteRouterTrust":
			expected = presetBool
		case "exitNode":
			expected = presetString
		case "homeSubnets", "trustedWifis", "siteRouterTargets":
			expected = presetStringList
		case "wifiExitNodes":
			expected = presetStringMap
		default:
			return false
		}
		if !matchesPresetType(value, expected) {
			return false
		}
	}

	return true
}

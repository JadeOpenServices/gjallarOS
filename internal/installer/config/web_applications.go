package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/apps"
)

const (
	WebApplicationPlane = "plane"
	WebApplicationTeams = "teams"
)

// WebApplicationIntent describes which web application the user wants
// integrated and, when applicable, which endpoint that integration should use.
//
// Browser choice, profile layout, MIME routing and application-specific launch
// behavior remain owned by the corresponding application module.
type WebApplicationIntent struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint,omitempty"`
}

// SupportedWebApplicationIDs returns the stable integration identifiers exposed
// by the installer contract.
func SupportedWebApplicationIDs() []string {
	return []string{
		WebApplicationPlane,
		WebApplicationTeams,
	}
}

func supportedWebApplication(id string) bool {
	for _, candidate := range SupportedWebApplicationIDs() {
		if id == candidate {
			return true
		}
	}
	return false
}

// NormalizeWebApplications canonicalizes the generic integration contract.
// Retired web applications (see apps/retired.json) are dropped.
func NormalizeWebApplications(user *User) error {
	retired, err := apps.Retired()
	if err != nil {
		return err
	}
	normalized := make([]WebApplicationIntent, 0, len(user.WebApplications))
	seen := make(map[string]bool, len(user.WebApplications))

	for index, application := range user.WebApplications {
		id := strings.ToLower(strings.TrimSpace(application.ID))
		if slices.Contains(retired, id) {
			continue
		}
		if !supportedWebApplication(id) {
			return fmt.Errorf(
				"webApplications[%d].id: unsupported web application %q",
				index,
				application.ID,
			)
		}
		if seen[id] {
			return fmt.Errorf(
				"webApplications[%d].id: duplicate web application %q",
				index,
				id,
			)
		}
		seen[id] = true

		endpoint := strings.TrimSpace(application.Endpoint)
		if endpoint != "" {
			value, err := NormalizeExternalServiceEndpoint(endpoint)
			if err != nil {
				return fmt.Errorf(
					"webApplications[%d].endpoint: %w",
					index,
					err,
				)
			}
			endpoint = value
		}

		if id == WebApplicationPlane && endpoint == "" {
			return fmt.Errorf(
				"webApplications[%d].endpoint: Plane requires an endpoint",
				index,
			)
		}

		normalized = append(normalized, WebApplicationIntent{
			ID:       id,
			Endpoint: endpoint,
		})
	}

	user.WebApplications = normalized
	return nil
}

// legacyWebApplicationKeys held Plane and Draw.io before GJAL-95 moved them
// into webApplications.
var legacyWebApplicationKeys = []string{"planeEnable", "planeHost", "drawioEnable", "drawioSelfHosted", "drawioHost"}

// RetiredEntries returns the retired app and web application IDs that
// configuration text still lists, so rebuild can write the result back.
func RetiredEntries(contents []byte) []string {
	var fields struct {
		Apps            []string               `json:"apps"`
		WebApplications []WebApplicationIntent `json:"webApplications"`
	}
	if json.Unmarshal(contents, &fields) != nil {
		return nil
	}
	retired, err := apps.Retired()
	if err != nil {
		return nil
	}
	var found []string
	for _, id := range fields.Apps {
		if slices.Contains(retired, id) && !slices.Contains(found, id) {
			found = append(found, id)
		}
	}
	for _, application := range fields.WebApplications {
		id := strings.ToLower(strings.TrimSpace(application.ID))
		if slices.Contains(retired, id) && !slices.Contains(found, id) {
			found = append(found, id)
		}
	}
	return found
}

// HasLegacyWebApplications reports whether configuration text still needs the
// one-time move into webApplications, so rebuild can write the result back.
func HasLegacyWebApplications(contents []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(contents, &fields) != nil {
		return false
	}
	return legacyWebApplications(fields)
}

func legacyWebApplications(fields map[string]json.RawMessage) bool {
	if value, ok := fields["webApplications"]; !ok || string(value) == "null" {
		return true
	}
	for _, key := range legacyWebApplicationKeys {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

// migrateLegacyWebApplications moves pre-GJAL-95 Plane fields into
// webApplications and drops them, Draw.io fields too (Draw.io is retired).
// A webApplications list that is already there wins. Teams was always on
// back then, so it stays on.
func migrateLegacyWebApplications(fields map[string]json.RawMessage) error {
	if value, ok := fields["webApplications"]; !ok || string(value) == "null" {
		var legacy struct {
			PlaneEnable bool   `json:"planeEnable"`
			PlaneHost   string `json:"planeHost"`
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return fmt.Errorf("legacy web application fields: %w", err)
		}
		applications := []WebApplicationIntent{{ID: WebApplicationTeams}}
		if legacy.PlaneEnable {
			endpoint, err := NormalizeExternalServiceEndpoint(legacy.PlaneHost)
			if err != nil {
				return fmt.Errorf("planeHost: %w", err)
			}
			applications = append(applications, WebApplicationIntent{ID: WebApplicationPlane, Endpoint: endpoint})
		}
		if fields["webApplications"], err = json.Marshal(applications); err != nil {
			return err
		}
	}
	for _, key := range legacyWebApplicationKeys {
		delete(fields, key)
	}
	return nil
}

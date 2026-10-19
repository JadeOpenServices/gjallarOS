package config

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Tier says how a change to a user.config.json key is confirmed after
// install (ROADMAP §14, decided with Jade 2026-10-18 and 2026-10-19).
type Tier string

const (
	// TierEveryday changes need one password per apply.
	TierEveryday Tier = "everyday"
	// TierSecurity changes show old and new value, ask the password every
	// apply and wait 10 s at home, 1 h elsewhere.
	TierSecurity Tier = "security"
	// TierCritical changes always wait 1 h and ask the password when asking
	// and again when applying.
	TierCritical Tier = "critical"
	// TierInstaller keys steer one installer run. The settings editor never
	// shows them and the settings service refuses to change them.
	TierInstaller Tier = "installer"
)

var userTiers = sync.OnceValues(func() (map[string]Tier, error) {
	t := reflect.TypeFor[User]()
	tiers := make(map[string]Tier, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if key == "" || key == "-" {
			return nil, fmt.Errorf("User.%s has no json key", field.Name)
		}
		tier := Tier(field.Tag.Get("tier"))
		switch tier {
		case TierEveryday, TierSecurity, TierCritical, TierInstaller:
		case "":
			return nil, fmt.Errorf("%s has no tier; every user.config.json key needs one", key)
		default:
			return nil, fmt.Errorf("%s has tier %q; it must be everyday, security, critical or installer", key, tier)
		}
		tiers[key] = tier
	}
	return tiers, nil
})

// Tiers returns the tier of every user.config.json key. It fails when a key
// has no valid tier, so a new setting cannot ship without one.
func Tiers() (map[string]Tier, error) {
	tiers, err := userTiers()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Tier, len(tiers))
	for key, tier := range tiers {
		out[key] = tier
	}
	return out, nil
}

// TierOf returns the tier of one top-level user.config.json key. An unknown
// key is an error, like in Load.
func TierOf(key string) (Tier, error) {
	tiers, err := userTiers()
	if err != nil {
		return "", err
	}
	tier, ok := tiers[key]
	if !ok {
		return "", fmt.Errorf("%s is not a user.config.json key", key)
	}
	return tier, nil
}

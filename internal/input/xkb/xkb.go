// Package xkb normalizes installer keyboard-layout input.
package xkb

import (
	"fmt"
	"regexp"
	"strings"
)

type Layout struct {
	Name    string
	Variant string
}

// listEntry is one xkb layout or variant name: every xkeyboard-config
// name fits (the longest variant has 26 characters). An entry may be empty:
// variant ",nodeadkeys" sets a variant for the second layout only. xkb
// takes at most 4 groups, 8 leaves room.
var list = regexp.MustCompile(`^[A-Za-z0-9_-]{0,32}(,[A-Za-z0-9_-]{0,32}){0,7}$`)

// ValidList reports whether value is an xkb layout or variant, or a comma
// list of them ("de,us"). The value reaches the Hyprland config text, so it
// holds no space, newline or other separator (L145). This is the one rule
// for layouts and variants: config.Validate and Normalize both use it.
func ValidList(value string) bool {
	return value != "" && list.MatchString(value)
}

func Normalize(requested string) (Layout, error) {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name == "" {
		name = "de"
	}
	if !ValidList(name) {
		return Layout{}, fmt.Errorf("keyboard layout may contain only letters, numbers, underscore and hyphen, up to 8 comma-separated names of at most 32 characters")
	}
	// xkeyboard-config no longer provides de(latin1). The regular German map
	// is the supported replacement and works in both SDDM and Hyprland.
	if name == "de" || name == "de-latin1" {
		name = "de"
	}
	return Layout{Name: name}, nil
}

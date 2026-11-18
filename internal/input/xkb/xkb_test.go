package xkb

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"":          "de",
		"DE":        "de",
		"de-latin1": "de",
		"us":        "us",
	}
	for input, want := range tests {
		got, err := Normalize(input)
		if err != nil || got.Name != want || got.Variant != "" {
			t.Fatalf("Normalize(%q) = %#v, %v", input, got, err)
		}
	}
}

func TestNormalizeRejectsUnsafeInput(t *testing.T) {
	for _, input := range []string{"de; rm", "us+inet", "de\nexec-once = SYNTHETIC"} {
		if _, err := Normalize(input); err == nil {
			t.Errorf("Normalize(%q) accepted", input)
		}
	}
}

// G2/G3: one rule for layouts and variants, with empty entries for "the
// default variant of this layout".
func TestValidList(t *testing.T) {
	for value, want := range map[string]bool{
		"de":                          true,
		"de,us":                       true,
		",nodeadkeys":                 true,
		"nodeadkeys,":                 true,
		",":                           true,
		"altgr-intl":                  true,
		"de_Sun_type6":                true,
		"":                            false,
		"us+inet":                     false,
		"de us":                       false,
		"de\nexec-once = SYNTHETIC":   false,
		"de;rm":                       false,
		strings.Repeat("a", 33):       false,
		strings.Repeat("a,", 8) + "a": false,
	} {
		if ValidList(value) != want {
			t.Errorf("ValidList(%q) = %v", value, !want)
		}
	}
}

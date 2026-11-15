package region

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setLists(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "zone.tab", "# comment\nDE\t+5230+01322\tEurope/Berlin\nJP\t+353916+1394441\tAsia/Tokyo\n")
	locales := write(t, dir, "SUPPORTED", "de_DE.UTF-8/UTF-8 \\\nde_DE/ISO-8859-1 \\\nsr_RS@latin/UTF-8 \\\nsr_RS.UTF-8@latin/UTF-8 \\\nC.UTF-8/UTF-8 \\\nam_ET/UTF-8 \\\nde_DE.ISO-8859-15@euro/ISO-8859-15 \\\nja_JP.UTF-8/UTF-8 \\\n")
	rules := write(t, dir, "base.lst", "! model\n  pc104 Generic 104-key PC\n\n! layout\n  us              English (US)\n  de              German\n  fr              French\n\n! variant\n  nodeadkeys      de: German (no dead keys)\n")
	oldZone, oldLocale, oldRules := ZoneDir, LocaleList, XKBRules
	ZoneDir, LocaleList, XKBRules = dir, locales, rules
	t.Cleanup(func() { ZoneDir, LocaleList, XKBRules = oldZone, oldLocale, oldRules })
}

func TestListsComeFromTheTables(t *testing.T) {
	setLists(t)
	if got, want := Timezones(), []string{"Asia/Tokyo", "Etc/UTC", "Europe/Berlin"}; !slices.Equal(got, want) {
		t.Fatalf("Timezones() = %v, want %v", got, want)
	}
	if got, want := Locales(), []string{"am_ET", "de_DE.UTF-8", "en_US.UTF-8", "ja_JP.UTF-8", "sr_RS.UTF-8@latin", "sr_RS@latin"}; !slices.Equal(got, want) {
		t.Fatalf("Locales() = %v, want %v", got, want)
	}
	if got, want := KeyboardLayouts(), []string{"de", "fr", "us"}; !slices.Equal(got, want) {
		t.Fatalf("KeyboardLayouts() = %v, want %v", got, want)
	}
}

func TestListsWithoutTablesOfferTheNeutralDefaults(t *testing.T) {
	oldZone, oldLocale, oldRules := ZoneDir, LocaleList, XKBRules
	ZoneDir, LocaleList, XKBRules = "", "", ""
	t.Cleanup(func() { ZoneDir, LocaleList, XKBRules = oldZone, oldLocale, oldRules })
	if got := Timezones(); !slices.Equal(got, []string{DefaultTimezone}) {
		t.Fatalf("Timezones() = %v", got)
	}
	if got := Locales(); !slices.Equal(got, []string{DefaultLocale}) {
		t.Fatalf("Locales() = %v", got)
	}
	if got := KeyboardLayouts(); !slices.Equal(got, []string{DefaultKeyboard}) {
		t.Fatalf("KeyboardLayouts() = %v", got)
	}
}

func TestParseLocalectl(t *testing.T) {
	for _, tc := range []struct{ out, locale, layout string }{
		{"System Locale: LANG=en_US.UTF-8\n    VC Keymap: (unset)\n   X11 Layout: de\n    X11 Model: pc104\n", "en_US.UTF-8", "de"},
		{"System Locale: LANG=fr_FR.UTF-8\n                LC_TIME=de_DE.UTF-8\n    VC Keymap: fr\n   X11 Layout: n/a\n", "fr_FR.UTF-8", "fr"},
		{"System Locale: LANG=en_GB.UTF-8\n    VC Keymap: n/a\n   X11 Layout: gb,us\n", "en_GB.UTF-8", "gb"},
		{"System Locale: n/a\n    VC Keymap: (unset)\n   X11 Layout: (unset)\n", "", ""},
	} {
		locale, layout := parseLocalectl(tc.out)
		if locale != tc.locale || layout != tc.layout {
			t.Errorf("parseLocalectl(%q) = %q, %q; want %q, %q", tc.out, locale, layout, tc.locale, tc.layout)
		}
	}
}

func TestPrefillOffersOnlyListedValues(t *testing.T) {
	options := []string{"Etc/UTC", "Europe/Berlin"}
	if got := Prefill(options, "Etc/UTC", "", "Mars/Olympus", "Europe/Berlin"); got != "Europe/Berlin" {
		t.Fatalf("Prefill() = %q", got)
	}
	if got := Prefill(options, "Etc/UTC", "../../etc/shadow"); got != "Etc/UTC" {
		t.Fatalf("Prefill() = %q, want the fallback", got)
	}
}

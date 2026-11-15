// Package region lists the timezones, locales and keyboard layouts the
// installer offers, and reads the ones the live session already uses so the
// region step can start from them. Everything comes from local tables in
// the Nix store and from the running session; nothing goes over the
// network.
package region

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The package recipe sets these to tzdata's zoneinfo folder, glibc's list
// of supported locales and xkeyboard-config's base.lst. Empty means no list,
// and the step then falls back to the neutral defaults below.
var (
	ZoneDir    string
	LocaleList string
	XKBRules   string
)

// Neutral defaults for a machine whose live session says nothing usable.
// They are the same everywhere, so no install inherits a country by
// accident.
const (
	DefaultTimezone = "Etc/UTC"
	DefaultLocale   = "en_US.UTF-8"
	DefaultKeyboard = "us"
)

// Timezones returns the zone.tab zones, sorted, plus Etc/UTC.
func Timezones() []string {
	zones := []string{DefaultTimezone}
	if ZoneDir == "" {
		return zones
	}
	scanLines(filepath.Join(ZoneDir, "zone.tab"), func(line string) {
		fields := strings.Split(line, "\t")
		if len(fields) >= 3 && fields[2] != "" {
			zones = append(zones, fields[2])
		}
	})
	return sortedUnique(zones)
}

var localeName = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?(\.UTF-8)?(@[a-z]+)?$`)

// Locales returns the locales glibc supports with the UTF-8 charset,
// sorted, named as the SUPPORTED file names them: "de_DE.UTF-8/UTF-8",
// "am_ET/UTF-8" and "be_BY@latin/UTF-8" give de_DE.UTF-8, am_ET and
// be_BY@latin. NixOS adds "/UTF-8" to the chosen name, which gives the
// same line back; a name with another charset ("de_DE/ISO-8859-1") would
// not, so it is left out.
func Locales() []string {
	locales := []string{DefaultLocale}
	if LocaleList == "" {
		return locales
	}
	scanLines(LocaleList, func(line string) {
		name, charset, _ := strings.Cut(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), `\`)), "/")
		if charset == "UTF-8" && localeName.MatchString(name) {
			locales = append(locales, name)
		}
	})
	return sortedUnique(locales)
}

var layoutName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// KeyboardLayouts returns the layout names in the "! layout" section of
// base.lst, sorted.
func KeyboardLayouts() []string {
	layouts := []string{DefaultKeyboard}
	if XKBRules == "" {
		return layouts
	}
	section := ""
	scanLines(XKBRules, func(line string) {
		if strings.HasPrefix(line, "!") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "!"))
			return
		}
		if section != "layout" {
			return
		}
		fields := strings.Fields(line)
		if len(fields) >= 1 && layoutName.MatchString(fields[0]) {
			layouts = append(layouts, fields[0])
		}
	})
	return sortedUnique(layouts)
}

// Session is what the live session uses. A field is empty when the session
// does not say.
type Session struct {
	Timezone, Locale, KeyboardLayout string
}

// Live reads the session's timezone, locale and keyboard layout through
// timedatectl and localectl. It runs as the installing user and only reads.
func Live(ctx context.Context) Session {
	var s Session
	if out, err := exec.CommandContext(ctx, "timedatectl", "show", "-p", "Timezone", "--value").Output(); err == nil {
		s.Timezone = strings.TrimSpace(string(out))
	}
	if out, err := exec.CommandContext(ctx, "localectl", "status").Output(); err == nil {
		s.Locale, s.KeyboardLayout = parseLocalectl(string(out))
	}
	return s
}

// parseLocalectl takes LANG from "System Locale:" and the first X11
// layout, or the console keymap when no X11 layout is set.
func parseLocalectl(out string) (locale, layout string) {
	keymap := ""
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			// Further LC_* lines of "System Locale" have no key.
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "System Locale":
			if lang, ok := strings.CutPrefix(value, "LANG="); ok {
				locale = lang
			}
		case "X11 Layout":
			layout, _, _ = strings.Cut(value, ",")
		case "VC Keymap":
			keymap = value
		}
	}
	unset := func(v string) bool { return v == "" || v == "n/a" || v == "(unset)" }
	if unset(layout) {
		layout = keymap
	}
	if unset(layout) {
		layout = ""
	}
	return locale, layout
}

// Prefill picks the first of candidates that options lists, then fallback.
// A value the lists do not know is never offered.
func Prefill(options []string, fallback string, candidates ...string) string {
	for _, c := range candidates {
		if c != "" && slices.Contains(options, c) {
			return c
		}
	}
	return fallback
}

func scanLines(path string, each func(string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := lines.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		each(line)
	}
}

func sortedUnique(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

// Package zonecity guesses the weather city and country from the IANA
// timezone the user picked, using the tzdata tables. It sends nothing over
// the network; the bar later asks the user whether the guess is right.
package zonecity

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Dir is the tzdata zoneinfo folder holding zone.tab and iso3166.tab. The
// package recipe sets it; empty means no guess.
var Dir string

type Location struct {
	City    string
	Country string
}

// Guess returns the city and country of timezone, or false when the zone
// names no country (UTC, Etc/*) or the tables are missing.
func Guess(timezone string) (Location, bool) {
	return guess(Dir, strings.TrimSpace(timezone))
}

func guess(dir, timezone string) (Location, bool) {
	if dir == "" || timezone == "" {
		return Location{}, false
	}
	code := ""
	if !scan(filepath.Join(dir, "zone.tab"), func(fields []string) bool {
		if len(fields) >= 3 && fields[2] == timezone {
			code = fields[0]
			return true
		}
		return false
	}) {
		return Location{}, false
	}
	country := ""
	if !scan(filepath.Join(dir, "iso3166.tab"), func(fields []string) bool {
		if len(fields) >= 2 && fields[0] == code {
			country = strings.TrimSpace(fields[1])
			return true
		}
		return false
	}) || country == "" {
		return Location{}, false
	}
	city := strings.ReplaceAll(timezone[strings.LastIndex(timezone, "/")+1:], "_", " ")
	if city == "" {
		return Location{}, false
	}
	return Location{City: city, Country: country}, true
}

// scan calls match with the tab-separated fields of each data line until
// it returns true, and reports whether one did.
func scan(path string, match func([]string) bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := lines.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if match(strings.Split(line, "\t")) {
			return true
		}
	}
	return false
}

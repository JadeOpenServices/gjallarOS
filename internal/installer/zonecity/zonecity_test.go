package zonecity

import (
	"os"
	"path/filepath"
	"testing"
)

func tables(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	zone := "# comment\nDE\t+5230+01322\tEurope/Berlin\tmost of Germany\n" +
		"AR\t-3436-05827\tAmerica/Argentina/Buenos_Aires\tBuenos Aires (BA, CF)\n" +
		"XX\t+0000+00000\tAtlantis/Nowhere\n"
	iso := "# comment\nAR\tArgentina\nDE\tGermany\n"
	if err := os.WriteFile(filepath.Join(dir, "zone.tab"), []byte(zone), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iso3166.tab"), []byte(iso), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGuessNamesCityAndCountry(t *testing.T) {
	dir := tables(t)
	cases := map[string]Location{
		"Europe/Berlin":                  {City: "Berlin", Country: "Germany"},
		"America/Argentina/Buenos_Aires": {City: "Buenos Aires", Country: "Argentina"},
	}
	for zone, want := range cases {
		got, ok := guess(dir, zone)
		if !ok || got != want {
			t.Errorf("guess(%q) = %+v, %t; want %+v", zone, got, ok, want)
		}
	}
}

func TestGuessGivesNothingWithoutACountry(t *testing.T) {
	dir := tables(t)
	for _, zone := range []string{"UTC", "Etc/GMT+1", "Atlantis/Nowhere", ""} {
		if got, ok := guess(dir, zone); ok {
			t.Errorf("guess(%q) = %+v, want no guess", zone, got)
		}
	}
	if got, ok := guess("", "Europe/Berlin"); ok {
		t.Errorf("guess without tables = %+v, want no guess", got)
	}
	if got, ok := guess(t.TempDir(), "Europe/Berlin"); ok {
		t.Errorf("guess with missing tables = %+v, want no guess", got)
	}
}

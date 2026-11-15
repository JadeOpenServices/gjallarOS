package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/zonecity"
)

func zoneTables(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zone.tab"), []byte("DE\t+5230+01322\tEurope/Berlin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iso3166.tab"), []byte("DE\tGermany\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := zonecity.Dir
	zonecity.Dir = dir
	t.Cleanup(func() { zonecity.Dir = original })
}

func TestConfigureWeatherLocationKeepsConfiguredValues(t *testing.T) {
	zoneTables(t)
	u := &config.User{Timezone: "Europe/Berlin", WeatherCity: "  Frankfurt am Main  ", WeatherCountry: "  Germany  "}
	var out bytes.Buffer
	configureWeatherLocation(u, true, &out)
	if u.WeatherCity != "Frankfurt am Main" || u.WeatherCountry != "Germany" {
		t.Fatalf("weather = %q, %q", u.WeatherCity, u.WeatherCountry)
	}
	if !strings.Contains(out.String(), "Weather location: Frankfurt am Main, Germany") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestConfigureWeatherLocationGuessesFromTheTimezone(t *testing.T) {
	zoneTables(t)
	u := &config.User{Timezone: "Europe/Berlin"}
	var out bytes.Buffer
	configureWeatherLocation(u, true, &out)
	if u.WeatherCity != "Berlin" || u.WeatherCountry != "Germany" {
		t.Fatalf("weather = %q, %q", u.WeatherCity, u.WeatherCountry)
	}
	if !strings.Contains(out.String(), "the bar asks you to confirm it") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestConfigureWeatherLocationLeavesItEmpty(t *testing.T) {
	zoneTables(t)
	cases := map[string]struct {
		user  config.User
		guess bool
	}{
		"rerun with the weather off": {config.User{Timezone: "Europe/Berlin"}, false},
		"zone without a country":     {config.User{Timezone: "UTC"}, true},
		"half a location":            {config.User{Timezone: "UTC", WeatherCity: "Berlin"}, true},
	}
	for name, c := range cases {
		u := c.user
		var out bytes.Buffer
		configureWeatherLocation(&u, c.guess, &out)
		if u.WeatherCity != "" || u.WeatherCountry != "" {
			t.Errorf("%s: weather = %q, %q; want empty", name, u.WeatherCity, u.WeatherCountry)
		}
		if !strings.Contains(out.String(), "the bar asks for your city") {
			t.Errorf("%s: output = %q", name, out.String())
		}
	}
}

func TestPrintingIsOneChoice(t *testing.T) {
	cases := []struct {
		input             string
		running           config.User
		printing, network bool
	}{
		{"\n", config.User{}, false, false},
		{"\n", config.User{PrintingEnable: true}, true, false},
		{"\n", config.User{PrintingEnable: true, NetworkPrintingEnable: true}, true, true},
		{"1\n", config.User{PrintingEnable: true, NetworkPrintingEnable: true}, false, false},
		{"2\n", config.User{}, true, false},
		{"3\n", config.User{}, true, true},
	}
	for _, c := range cases {
		var out bytes.Buffer
		ui := prompt.New(strings.NewReader(c.input), &out)
		var u config.User
		if err := collectPrinting(context.Background(), ui, c.running, &u); err != nil {
			t.Fatal(err)
		}
		if u.PrintingEnable != c.printing || u.NetworkPrintingEnable != c.network {
			t.Errorf("input %q from %+v: printing %t network %t; want %t %t",
				c.input, c.running, u.PrintingEnable, u.NetworkPrintingEnable, c.printing, c.network)
		}
	}
}

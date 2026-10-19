package noctalia

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SYNTHETIC colors, not taken from any session.
const testNoctaliaColors = "base00=101010,base01=111111,base02=121212,base03=131313," +
	"base04=141414,base05=151515,base06=161616,base07=171717," +
	"base08=181818,base09=191919,base0A=1a1a1a,base0B=1b1b1b," +
	"base0C=1c1c1c,base0D=1d1d1d,base0E=1e1e1e,base0F=1f1f1f"

const testNoctaliaPalette = `{
  "base00": "101010", "base01": "111111", "base02": "121212", "base03": "131313",
  "base04": "141414", "base05": "151515", "base06": "161616", "base07": "171717",
  "base08": "181818", "base09": "191919", "base0A": "1A1A1A", "base0B": "1b1b1b",
  "base0C": "1c1c1c", "base0D": "1d1d1d", "base0E": "1e1e1e", "base0F": "1f1f1f"
}
`

func writeTestPalette(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "state", "noctalia")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stylix-override.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestReadNoctaliaPaletteWritesCheckedColors(t *testing.T) {
	colors, err := ReadPalette(writeTestPalette(t, testNoctaliaPalette))
	if err != nil {
		t.Fatal(err)
	}
	if colors != testNoctaliaColors {
		t.Fatalf("colors = %q, want %q", colors, testNoctaliaColors)
	}
	if err := CheckColors(colors); err != nil {
		t.Fatalf("root refuses what the user side wrote: %v", err)
	}
}

func TestReadNoctaliaPaletteWithoutFileIsNoPalette(t *testing.T) {
	colors, err := ReadPalette(t.TempDir())
	if err != nil || colors != "" {
		t.Fatalf("colors = %q, err = %v; want no palette", colors, err)
	}
}

func TestReadNoctaliaPaletteRefusesOtherContent(t *testing.T) {
	for name, content := range map[string]string{
		"empty object":  "{}\n",
		"extra key":     strings.Replace(testNoctaliaPalette, `"base0F"`, `"extra": "000000", "base0F"`, 1),
		"missing key":   strings.Replace(testNoctaliaPalette, `"base0F": "1f1f1f"`, `"base10": "1f1f1f"`, 1),
		"short value":   strings.Replace(testNoctaliaPalette, `"101010"`, `"10101"`, 1),
		"hash value":    strings.Replace(testNoctaliaPalette, `"101010"`, `"#10101"`, 1),
		"non-hex value": strings.Replace(testNoctaliaPalette, `"101010"`, `"10101g"`, 1),
		"number value":  strings.Replace(testNoctaliaPalette, `"101010"`, `101010`, 1),
		"not json":      "base00=101010\n",
		"too large":     testNoctaliaPalette + strings.Repeat(" ", MaxPaletteSize),
	} {
		if colors, err := ReadPalette(writeTestPalette(t, content)); err == nil {
			t.Errorf("%s: colors = %q, want an error", name, colors)
		}
	}
}

func TestReadNoctaliaPaletteRefusesLinks(t *testing.T) {
	home := writeTestPalette(t, testNoctaliaPalette)
	path := filepath.Join(home, ".local", "state", "noctalia", "stylix-override.json")
	target := filepath.Join(home, "palette.json")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	colors, err := ReadPalette(home)
	if err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Fatalf("colors = %q, err = %v through a link, want the link refused", colors, err)
	}
}

func TestCheckNoctaliaColorsRefusesOtherValues(t *testing.T) {
	for _, colors := range []string{
		"base00=101010",
		testNoctaliaColors + ",base10=000000",
		strings.Replace(testNoctaliaColors, "base0A", "base0B", 1),
		strings.Replace(testNoctaliaColors, "1a1a1a", "1A1A1A", 1),
		strings.Replace(testNoctaliaColors, "101010", "../etc", 1),
		strings.Replace(testNoctaliaColors, ",", " ", 1),
	} {
		if err := CheckColors(colors); err == nil {
			t.Errorf("CheckColors(%q) accepted", colors)
		}
	}
}

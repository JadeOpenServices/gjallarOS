package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Noctalia writes the session's live colors to the user's state directory,
// and the rebuild snapshots them into build-time Stylix targets. Root never
// opens that file: the user side reads it, keeps the sixteen base16 colors
// and hands the evaluation only those values, which root checks again.

const noctaliaColorsEnv = "GJALLAR_NOCTALIA_COLORS"

// The template in user/wm/shells/noctalia writes well under 1 KiB.
const noctaliaPaletteMaxSize = 4096

var noctaliaPaletteKeys = []string{
	"base00", "base01", "base02", "base03", "base04", "base05", "base06", "base07",
	"base08", "base09", "base0A", "base0B", "base0C", "base0D", "base0E", "base0F",
}

// rebuildColors returns the checked colors for this rebuild, or "" to build
// with the theme's own. Root takes only the values the user side handed
// over in GJALLAR_NOCTALIA_COLORS; the user side reads its own palette.
func rebuildColors(stderr io.Writer) string {
	var colors string
	var err error
	if os.Geteuid() == 0 {
		colors = os.Getenv(noctaliaColorsEnv)
		if colors != "" {
			err = checkNoctaliaColors(colors)
		}
	} else {
		var home string
		if home, err = os.UserHomeDir(); err == nil {
			colors, err = readNoctaliaPalette(home)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "WARN: Noctalia colors: %v; building with the theme's colors\n", err)
		return ""
	}
	return colors
}

// readNoctaliaPalette reads the palette under home. A missing file is no
// palette.
func readNoctaliaPalette(home string) (string, error) {
	path := filepath.Join(home, ".local", "state", "noctalia", "stylix-override.json")
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return "", fmt.Errorf("%s is a link; only a plain file is read", path)
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, noctaliaPaletteMaxSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > noctaliaPaletteMaxSize {
		return "", fmt.Errorf("%s is larger than %d bytes", path, noctaliaPaletteMaxSize)
	}
	var palette map[string]string
	if err := json.Unmarshal(data, &palette); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(palette) != len(noctaliaPaletteKeys) {
		return "", fmt.Errorf("%s has %d entries, want base00 to base0F", path, len(palette))
	}
	fields := make([]string, 0, len(noctaliaPaletteKeys))
	for _, key := range noctaliaPaletteKeys {
		value := strings.ToLower(palette[key])
		if !isHexColor(value) {
			return "", fmt.Errorf("%s: %s is not a six-digit hex color", path, key)
		}
		fields = append(fields, key+"="+value)
	}
	return strings.Join(fields, ","), nil
}

// checkNoctaliaColors accepts exactly what readNoctaliaPalette writes:
// base00=rrggbb,...,base0F=rrggbb in that order, lowercase hex.
func checkNoctaliaColors(colors string) error {
	fields := strings.Split(colors, ",")
	if len(fields) != len(noctaliaPaletteKeys) {
		return fmt.Errorf("%s has %d colors, want 16", noctaliaColorsEnv, len(fields))
	}
	for i, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key != noctaliaPaletteKeys[i] || !isHexColor(value) {
			return fmt.Errorf("%s: color %d is not %s=rrggbb", noctaliaColorsEnv, i+1, noctaliaPaletteKeys[i])
		}
	}
	return nil
}

func isHexColor(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

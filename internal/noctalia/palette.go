// Package noctalia reads the colors of the Noctalia session for a rebuild
// and checks them on the root side.
package noctalia

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

// ColorsEnv carries the checked colors into the system evaluation.
const ColorsEnv = "GJALLAR_NOCTALIA_COLORS"

// MaxPaletteSize bounds the palette file; the template in
// user/wm/shells/noctalia writes well under 1 KiB.
const MaxPaletteSize = 4096

var paletteKeys = []string{
	"base00", "base01", "base02", "base03", "base04", "base05", "base06", "base07",
	"base08", "base09", "base0A", "base0B", "base0C", "base0D", "base0E", "base0F",
}

// ReadPalette reads the palette under home and returns it as checked
// colors. A missing file is no palette.
func ReadPalette(home string) (string, error) {
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
	data, err := io.ReadAll(io.LimitReader(file, MaxPaletteSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxPaletteSize {
		return "", fmt.Errorf("%s is larger than %d bytes", path, MaxPaletteSize)
	}
	var palette map[string]string
	if err := json.Unmarshal(data, &palette); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(palette) != len(paletteKeys) {
		return "", fmt.Errorf("%s must hold 16 entries, base00 to base0F; it holds %d", path, len(palette))
	}
	fields := make([]string, 0, len(paletteKeys))
	for _, key := range paletteKeys {
		value := strings.ToLower(palette[key])
		if !isHexColor(value) {
			return "", fmt.Errorf("%s: %s is not a six-digit hex color", path, key)
		}
		fields = append(fields, key+"="+value)
	}
	return strings.Join(fields, ","), nil
}

// CheckColors accepts exactly what ReadPalette writes:
// base00=rrggbb,...,base0F=rrggbb in that order, lowercase hex.
func CheckColors(colors string) error {
	fields := strings.Split(colors, ",")
	if len(fields) != len(paletteKeys) {
		return fmt.Errorf("%s must hold 16 colors, base00 to base0F; it holds %d", ColorsEnv, len(fields))
	}
	for i, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key != paletteKeys[i] || !isHexColor(value) {
			return fmt.Errorf("%s: color %d is not %s=rrggbb", ColorsEnv, i+1, paletteKeys[i])
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

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/JadeOpenServices/gjallarOS/internal/noctalia"
)

// Noctalia writes the session's live colors to the user's state directory,
// and the rebuild snapshots them into build-time Stylix targets. Root never
// opens that file: the user side reads it, keeps the sixteen base16 colors
// and hands the evaluation only those values, which root checks again.

const noctaliaColorsEnv = noctalia.ColorsEnv

// rebuildColors returns the checked colors for this rebuild, or "" to build
// with the theme's own. Root takes only the values the user side handed
// over in GJALLAR_NOCTALIA_COLORS; the user side reads its own palette.
func rebuildColors(stderr io.Writer) string {
	var colors string
	var err error
	if os.Geteuid() == 0 {
		colors = os.Getenv(noctalia.ColorsEnv)
		if colors != "" {
			err = noctalia.CheckColors(colors)
		}
	} else {
		var home string
		if home, err = os.UserHomeDir(); err == nil {
			colors, err = noctalia.ReadPalette(home)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "WARN: Noctalia colors: %v; building with the theme's colors\n", err)
		return ""
	}
	return colors
}

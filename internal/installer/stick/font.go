package stick

import (
	"io"
	"os"
	"regexp"
	"strconv"
)

// The stick's terminal is sized for a page FrameWidth (72) columns wide
// with a margin at each side, and for the main menu at its longest (25
// rows) with a margin above and below; in the window also the logo above
// it (up to 6 rows and one empty row). Its font is the largest that still
// gives screenCols by windowRows cells in the window and screenCols by
// consoleRows on the text console (Jade 2026-11-23, "88 cols, rows floor"
// and "same floor, both"; 35 rows in the window so the logo shows, "floor
// 35 rows"). TestMainMenuFitsTheFloor draws the menu to check.
const (
	screenCols  = 88
	windowRows  = 35
	consoleRows = 29
)

// Jade Code's metrics, in font units (THEMEBRAIN section 18): every cell
// is 1200 wide and 1800 above the baseline plus 600 below, 2:1. A test
// reads them back from the font file.
const (
	jadeUnitsPerEm = 1950
	jadeAdvance    = 1200
	jadeAscent     = 1800
	jadeDescent    = 600
)

// fbSize is the file the kernel reports the screen's size in pixels in.
const fbSize = "/sys/class/graphics/fb0/virtual_size"

var fbSizeLine = regexp.MustCompile(`^([0-9]{1,5}),([0-9]{1,5})\n?$`)

// screenPixels reads the screen's size in pixels from path; ok is false
// when it reports nothing usable.
func screenPixels(path string) (width, height int, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return 0, 0, false
	}
	m := fbSizeLine.FindSubmatch(data)
	if m == nil {
		return 0, 0, false
	}
	width, _ = strconv.Atoi(string(m[1]))
	height, _ = strconv.Atoi(string(m[2]))
	return width, height, width > 0 && height > 0
}

// windowPixelSize is the largest Jade Code size in pixels at which foot
// still shows screenCols by windowRows cells on a screen width by height
// pixels; 0 when none from 8 up does. foot rounds the cell's width and
// each half of its height to whole pixels, so the cell is taken one pixel
// wider and two pixels taller than the font's own size, to be safe.
func windowPixelSize(width, height int) int {
	for px := 128; px >= 8; px-- {
		cw := (jadeAdvance*px + jadeUnitsPerEm - 1) / jadeUnitsPerEm
		ch := (jadeAscent+jadeDescent)*px/jadeUnitsPerEm + 2
		if width/cw >= screenCols && height/ch >= windowRows {
			return px
		}
	}
	return 0
}

// consoleCellWidth is the widest console cell, in pixels, that still gives
// screenCols by consoleRows cells on a screen width by height pixels. The
// console font's cells are twice as tall as they are wide.
func consoleCellWidth(width, height int) int {
	return min(width/screenCols, height/(2*consoleRows))
}

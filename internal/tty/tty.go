// Package tty tells a terminal apart from other character devices.
//
// A character-device mode bit is not enough: /dev/null and /dev/zero are
// character devices too, so a run with stdin from /dev/null looked like a
// person at a terminal (loophole 42, e2e-r292 2026-11-10). Only a terminal
// answers the TCGETS ioctl.
package tty

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
)

// Is reports whether file is a terminal.
func Is(file *os.File) bool {
	if file == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}

// Size is file's size in cells and pixels: 80 by 24 cells and no pixel
// size when file is not a terminal, and 80 or 24 for a side the terminal
// does not say. The pixel size is what the terminal reports, 0 on the
// Linux console; the page frame uses it for the shape of a cell.
func Size(file *os.File) calmmenu.Size {
	ws, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return calmmenu.Size{Cols: 80, Rows: 24}
	}
	s := calmmenu.Size{Cols: int(ws.Col), Rows: int(ws.Row), XPixel: int(ws.Xpixel), YPixel: int(ws.Ypixel)}
	if s.Cols == 0 {
		s.Cols = 80
	}
	if s.Rows == 0 {
		s.Rows = 24
	}
	return s
}

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
)

// Is reports whether file is a terminal.
func Is(file *os.File) bool {
	if file == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}

// Size is file's size in cells: 80 by 24 when file is not a terminal,
// and 80 or 24 for a side the terminal does not say.
func Size(file *os.File) (cols, rows int) {
	ws, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 80, 24
	}
	cols, rows = int(ws.Col), int(ws.Row)
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	return cols, rows
}

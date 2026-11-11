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

package prompt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Secret asks for a key or password with the typed text hidden. term is the
// terminal the answer is typed on. Echo is switched off in the kernel, not
// by a program found on PATH, and comes back when the answer is read, when
// the question is given up on, or when the read fails. Enter still moves to
// the next line (ECHONL). The answer goes nowhere but the caller: not to Out
// and not to a log.
func (u UI) Secret(ctx context.Context, label string, term *os.File) (string, error) {
	if u.GTK {
		return "", errors.New("a hidden answer is only asked on a terminal")
	}
	fd := int(term.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", fmt.Errorf("a hidden answer is only asked on a terminal: %w", err)
	}
	hidden := *old
	hidden.Lflag &^= unix.ECHO
	hidden.Lflag |= unix.ECHONL
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &hidden); err != nil {
		return "", fmt.Errorf("hide the answer: %w", err)
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, old)
	fmt.Fprintf(u.Out, "%s: ", label)
	line, err := u.readLine(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

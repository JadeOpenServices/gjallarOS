package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// usbReadRecoveryKey reads the disk recovery key or passphrase that authorizes
// disarming USB enforcement. A terminal prompts without echo; otherwise the
// passphrase is read from stdin, so a graphical prompt can pipe it in.
func usbReadRecoveryKey(stdin *os.File, stderr io.Writer) (string, error) {
	if restore, ok := usbDisableEcho(stdin.Fd()); ok {
		defer restore()
		fmt.Fprint(stderr, "Disk recovery key or passphrase: ")
		defer fmt.Fprintln(stderr)
	}
	return usbParseRecoveryKey(stdin)
}

// usbParseRecoveryKey takes one line and drops only its line ending, because
// cryptsetup compares the key bytes exactly.
func usbParseRecoveryKey(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return "", errors.New("no disk recovery key or passphrase given")
	}
	return line, nil
}

// usbDisableEcho turns off terminal echo on fd and reports false when fd is
// not a terminal.
func usbDisableEcho(fd uintptr) (func(), bool) {
	var saved syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&saved))); errno != 0 {
		return nil, false
	}
	quiet := saved
	quiet.Lflag &^= syscall.ECHO
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&quiet))); errno != 0 {
		return nil, false
	}
	return func() {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&saved)))
	}, true
}

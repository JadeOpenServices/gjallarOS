package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Some gjallarctl commands are shell scripts that NixOS builds with fixed
// store paths for every tool they call. They live in the system profile's
// libexec, which only root writes, so they stay off PATH and gjallarctl is
// the one name to type (Plane 282).

// systemLibexec is the system profile's libexec; tests point it elsewhere.
var (
	systemLibexec = "/run/current-system/sw/libexec"
	helperExec    = syscall.Exec
)

// forwardSystemHelper replaces gjallarctl with the helper name.
func forwardSystemHelper(name, feature string, args []string, stderr io.Writer) int {
	helper := filepath.Join(systemLibexec, name)
	if _, err := os.Stat(helper); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "ERROR: %s is not part of this system\n", feature)
			return 1
		}
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	err := helperExec(helper, append([]string{name}, args...), os.Environ())
	fmt.Fprintf(stderr, "ERROR: start %s: %v\n", helper, err)
	return 1
}

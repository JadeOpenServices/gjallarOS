package oldroot

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
)

// The programs root runs for the move, by absolute path and never found
// through PATH (L114): a cryptsetup or lsblk planted in a folder the caller
// can change is never run as root. The installer package sets them to
// their store paths; the defaults are the system's own, which only root
// can change.
var (
	Cryptsetup = "/run/current-system/sw/bin/cryptsetup"
	Lsblk      = "/run/current-system/sw/bin/lsblk"
)

// toolEnv is the whole environment of those programs. Nothing comes from
// the caller: libdevmapper and libcryptsetup read variables such as
// DM_DEV_DIR, and none of them may point root elsewhere.
var toolEnv = []string{"PATH=/run/current-system/sw/bin", "LC_ALL=C"}

// Tool returns the command for one of those programs with the fixed
// environment. A path that is not absolute fails at Start and runs nothing.
func Tool(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = toolEnv
	if !filepath.IsAbs(path) {
		cmd.Err = fmt.Errorf("%q is not an absolute path, so it is not run as root", path)
	}
	return cmd
}

// ExitCode is the exit code of a program that ran and failed, or -1 for
// any other error. Callers check codes through it, so os/exec stays in
// this reviewed file (L137).
func ExitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

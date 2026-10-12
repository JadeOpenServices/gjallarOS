package oddccli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Path is the `oddc` command, which the ODDC NixOS module installs.
var Path = "oddc"

// runODDC runs `oddc ARGS...`. On a deployed system oddc itself defaults
// to /etc/oddc, the deployed model and its host overlay.
func runODDC(args []string, stdout, stderr io.Writer) int {
	return execute(stdout, stderr, "oddc", Path, args...)
}

// execute runs the command at path, reporting a failure to start as label's.
func execute(stdout, stderr io.Writer, label, path string, args ...string) int {
	cmd := exec.Command(path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(stderr, "ERROR: run %s: %v\n", label, err)
		return 1
	}

	return 0
}

// HardwareUpdate moves the oddc pin of the flake at root with
// `oddc update --flake ROOT [--channel CHANNEL]`. It never switches; the caller
// rebuilds afterwards and stops on a non-zero code.
func HardwareUpdate(root, channel string, stdout, stderr io.Writer) int {
	args := []string{"update", "--flake", root}
	if channel != "" {
		args = append(args, "--channel", channel)
	}
	return execute(stdout, stderr, "oddc", Path, args...)
}

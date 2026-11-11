// Package oddccli is `gjallarctl oddc`. ODDC owns its commands; gjallarctl
// passes them to the `oddc` command unchanged except validate-device, which
// validates this gjallarOS checkout on the device, and update, which is
// refused: the oddc pin moves only with gjallarctl rebuild --hardware-update.
package oddccli

import (
	"fmt"
	"io"
)

// Run executes `gjallarctl oddc ARGS...` and returns its exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "ERROR: oddc requires a subcommand")
		return 2
	}

	switch args[0] {
	case "validate-device":
		return runValidateDevice(args[1:], stdout, stderr)
	case "update":
		// Passed through, oddc would move the pin of /etc/nixos, which a
		// gjallarOS install does not build from. The pin of the system
		// checkout moves only through rebuild, which checks the checkout
		// and the developer grant first.
		fmt.Fprintln(stderr, "ERROR: gjallarctl oddc update is not used on gjallarOS; move the oddc pin with: gjallarctl rebuild --hardware-update [--channel stable|staging] [--switch 0|1]")
		return 2
	}

	return runODDC(args, stdout, stderr)
}

// Usage lists the commands for gjallarctl's help.
func Usage(out io.Writer) {
	fmt.Fprintln(out, "       gjallarctl oddc validate-device [--repo PATH]")
	fmt.Fprintln(out, "       gjallarctl oddc ODDC-COMMAND [ODDC-ARGS...]")
}

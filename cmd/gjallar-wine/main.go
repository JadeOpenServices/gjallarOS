// Command gjallar-wine starts a Wine app in its own sandbox (Plane 272,
// wiki 746). Each app has its own Wine prefix and files folder, sees only
// those and the Nix store, and reaches the network only through its two
// switches in the settings service.
package main

import (
	"fmt"
	"io"
	"os"
)

// Tool paths, set by the package.
var (
	bwrapPath   = "bwrap"
	pastaPath   = "pasta"
	nftPath     = "nft"
	nsenterPath = "nsenter"
	ipPath      = "ip"
	winePath    = "wine"
)

const usage = `usage: gjallar-wine run ID [ARGS...]   start the app ID in its sandbox
ID is the app's id, as gjallarctl wine list shows it.`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		if len(args) < 2 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		return runApp(args[1], args[2:], stderr)
	// hold and lock are the launcher's own steps inside the sandbox.
	case "hold":
		return hold()
	case "lock":
		return lock(args[1:], stderr)
	}
	fmt.Fprintf(stderr, "ERROR: unknown command %q\n%s\n", args[0], usage)
	return 2
}

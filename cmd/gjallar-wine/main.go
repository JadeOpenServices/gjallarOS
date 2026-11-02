// Command gjallar-wine starts a Wine app in its own sandbox (Plane 272,
// wiki 746). It runs the commands of gjallarctl wine that need Wine, and
// is not on PATH (internal/winesandbox/helper.go). Each app has its own
// Wine prefix and files folder, sees only those and the Nix store, and
// reaches the network only through its two switches in the settings
// service.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
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

// usage is gjallarctl wine's: users reach these commands only through it.
var usage = "usage:\n" + winesandbox.CommandsUsage

var errUsage = errors.New("wrong arguments")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "add":
		err = addApp(args[1:], stdout, stderr)
	case "install":
		err = installCmd(args[1:], stdout, stderr)
	case "program":
		err = programCmd(args[1:], stdout)
	case "remove":
		err = removeCmd(args[1:], stdout)
	case "reset":
		err = resetCmd(args[1:], stdout, stderr)
	case "repair":
		err = repairCmd(args[1:], stdout, stderr)
	case "undo":
		err = undoCmd(args[1:], stdout, stderr)
	case "import":
		err = importCmd(args[1:], stdout, stderr)
	case "check":
		err = checkCmd(args[1:], stdout, stderr)
	case "fixes":
		err = fixesCmd(args[1:], stdout)
	// menu brings the app menu in line with the apps, after each rebuild.
	case "menu":
		err = menuCmd(args[1:], stderr)
	case "run":
		if len(args) < 2 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		return runApp(args[1], args[2:], stderr)
	// hold, lock and stay are the launcher's own steps inside the sandbox.
	case "hold":
		return hold()
	case "lock":
		return lock(args[1:], stderr)
	// icon reads an app's icon inside its own sandbox (icon.go).
	case "icon":
		return iconCmd(args[1:], stdout, stderr)
	// scan reads an installer's headers inside its own sandbox (scan.go).
	case "scan":
		return scanCmd(args[1:], stdout, stderr)
	case "stay":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		return stay(args[1:], signals, stderr)
	default:
		fmt.Fprintf(stderr, "ERROR: unknown command %q\n%s\n", args[0], usage)
		return 2
	}
	switch {
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, usage)
		return 2
	case err != nil:
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

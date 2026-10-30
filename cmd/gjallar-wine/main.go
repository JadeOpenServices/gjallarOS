// Command gjallar-wine starts a Wine app in its own sandbox (Plane 272,
// wiki 746). Each app has its own Wine prefix and files folder, sees only
// those and the Nix store, and reaches the network only through its two
// switches in the settings service.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
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

const usage = `usage:
  gjallar-wine add NAME [INSTALLER]        add an app with ~/WineApps/NAME as its
                                           files folder, and run its installer
  gjallar-wine install ID INSTALLER        run an installer in the app ID
  gjallar-wine program ID [CHOICE]         show or pick the program the app starts
  gjallar-wine run ID [ARGS...]            start the app ID in its sandbox
  gjallar-wine remove ID [--delete-files]  remove the app ID; its files folder
                                           stays unless --delete-files is given
ID is the app's id, as gjallarctl wine list shows it. CHOICE is a number from
the list or a program's path on C: or D:, such as D:\Tool\tool.exe. An
installer is an .exe or .msi file.`

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

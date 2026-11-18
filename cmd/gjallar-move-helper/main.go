// Command gjallar-move-helper does the root work of the disk move (345). It
// reads one request as JSON on stdin and writes the result as JSON on
// stdout. See internal/movehelper.
package main

import (
	"log"
	"os"

	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 1 {
		log.Fatal("Usage: gjallar-move-helper < request.json")
	}
	if os.Geteuid() != 0 {
		log.Fatal("gjallar-move-helper runs as root")
	}
	// Nothing of the caller's environment is used: the tools run by fixed
	// path with their own fixed environment (L114), and this keeps it so
	// for anything the helper might start later.
	os.Clearenv()
	os.Setenv("PATH", "/run/current-system/sw/bin")
	os.Setenv("LC_ALL", "C")
	if err := movehelper.Run(movehelper.Base, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

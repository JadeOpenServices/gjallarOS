// Command gjallar-namecheck answers one question from the settings service:
// does anyone on the network answer for <name>.local? systemd starts it per
// connection as an unprivileged user, with the socket as stdin and stdout,
// so the root service never parses packets from the network.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/namecheck"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: gjallar-namecheck (started by systemd for the settings service)")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := namecheck.Serve(ctx, os.Stdin, os.Stdout, namecheck.Probe); err != nil {
		log.Fatal(err)
	}
}

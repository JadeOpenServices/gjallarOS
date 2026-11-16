// gjallar-stick is the installer stick's menu; see internal/installer/stick.
package main

import (
	"os"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/stick"
)

func main() {
	os.Exit(stick.Main(os.Args[1:], os.Stdout, os.Stderr))
}

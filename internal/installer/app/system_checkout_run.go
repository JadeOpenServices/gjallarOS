package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

// geteuid is os.Geteuid; tests replace it.
var geteuid = os.Geteuid

// requireSystemCheckoutRun keeps the installer on the system checkout of a
// host that builds from it (wiki 752 Q214). A run from any other checkout
// wrote settings the system never reads, and the next gjallarctl rebuild
// built the old ones (e2e-shim, 2026-11-10). install.sh starts the installer
// as root on /etc/gjallar/system; nothing is written before this check.
// Hosts without a system checkout (standalone installs that still build
// from a user checkout) are not affected.
func requireSystemCheckoutRun(repositoryFile, root string, euid int) error {
	if !systemcheckout.Active(repositoryFile) {
		return nil
	}
	if filepath.Clean(root) != systemcheckout.Path {
		return fmt.Errorf(
			"this GjallarOS builds from %s, not from %s; nothing was changed. "+
				"Start the installer with scripts/installation/install.sh, which runs it there as root",
			systemcheckout.Path, root,
		)
	}
	if euid != 0 {
		return fmt.Errorf(
			"%s belongs to root; nothing was changed. "+
				"Start the installer with scripts/installation/install.sh, which runs it as root",
			systemcheckout.Path,
		)
	}
	return nil
}

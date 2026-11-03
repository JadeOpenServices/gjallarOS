package oddccli

import (
	"fmt"
	"io"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

// CheckUpdate tells whether ODDC's branch moved past the commit flake.lock
// pins. It only reports: the pin moves with `gjallarctl rebuild --hardware-update`.
// Offline or on any error it stays quiet, so a rebuild never waits on it.
func CheckUpdate(repo string, stdout io.Writer) {
	pinned, ref, err := oddc.LockedInput(repo)
	if err != nil {
		return
	}

	upstream, err := oddc.UpstreamRevision(oddc.Remote, ref, 5*time.Second)
	if err != nil || upstream == pinned {
		return
	}

	fmt.Fprintf(
		stdout,
		"[GjallarOS] ODDC update available: %s -> %s. Apply with: gjallarctl rebuild --hardware-update\n",
		short(pinned),
		short(upstream),
	)
}

package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/bootnext"
)

// freshUndo takes back what a failed fresh install left for the firmware
// (Q257). On friend's T480s (.68, 2026-11-22) the run stopped after the
// new ESP held shim and a firmware entry, but before the gjallarOS key was
// queued for MokManager. The firmware then started shim from the internal
// disk, shim refused the unknown key and fell back to MokManager on every
// start.
//
// It is armed just before the new partition table is written and disarmed
// once the new system can start. Run then deletes every firmware entry that
// starts on the new ESP and zeroes that partition, so the firmware finds no
// loader there, the EFI\BOOT fallback included. Both names come from this
// run's own disk plan: the disk lsblk resolved and an ESP PARTUUID made at
// random for this run. No path under the target mount is opened.
type freshUndo struct {
	disk    string
	espUUID string
	armed   bool
}

func (u *freshUndo) arm(disk, espUUID string) {
	if u == nil {
		return
	}
	u.disk, u.espUUID, u.armed = disk, espUUID, true
}

func (u *freshUndo) disarm() {
	if u != nil {
		u.armed = false
	}
}

// run undoes an armed install once; it also runs after an interrupt.
func (u *freshUndo) run(ctx context.Context, out io.Writer) {
	if u == nil || !u.armed {
		return
	}
	u.armed = false
	ctx = context.WithoutCancel(ctx)

	removed, err := bootnext.RemoveForESP(ctx, bootNextRunner, u.espUUID)
	if len(removed) > 0 {
		fmt.Fprintf(out, "Removed the firmware boot entries of the failed install: Boot%s.\n", strings.Join(removed, ", Boot"))
	}
	if err != nil {
		fmt.Fprintf(out, "WARNING: the firmware boot entries of the failed install could not all be removed (%v). Pick the USB stick in the firmware boot menu next time.\n", err)
	}

	dev, err := bootnext.FindESP(ctx, bootNextRunner, u.disk, u.espUUID)
	if err != nil {
		fmt.Fprintf(out, "WARNING: the boot partition of the failed install was left as it is (%v).\n", err)
		return
	}
	if dev == "" {
		return
	}
	if mounts, err := bootNextRunner.Output(ctx, "findmnt", "-n", "-r", "-o", "TARGET", "-S", dev); err == nil && len(strings.TrimSpace(string(mounts))) > 0 {
		if _, err := privilegedCommand(ctx, "umount", "--all-targets", "--", dev); err != nil {
			fmt.Fprintf(out, "WARNING: the boot partition of the failed install is still mounted, so it was left as it is (%v).\n", err)
			return
		}
	}
	// blkdiscard opens the partition exclusively, so it refuses one that is
	// still mounted or in use. Without --force it never skips that check.
	if _, err := privilegedCommand(ctx, "blkdiscard", "--zeroout", "--", dev); err != nil {
		fmt.Fprintf(out, "WARNING: the boot partition of the failed install could not be cleared (%v).\n", err)
		return
	}
	fmt.Fprintf(out, "Cleared the boot partition of the failed install (%s); the firmware no longer starts it.\n", dev)
}

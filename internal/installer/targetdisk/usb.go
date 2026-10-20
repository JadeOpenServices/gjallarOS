package targetdisk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// USBAttached reports whether the whole disk under path is attached over
// USB. path may be a whole disk, a partition, or a crypt/LVM device: lsblk
// -s walks from it down to the disks it sits on. Any USB disk among them
// counts. It fails when lsblk reports no whole disk at all.
func USBAttached(ctx context.Context, path string) (bool, error) {
	return usbAttached(ctx, path, execRunner{})
}

func usbAttached(ctx context.Context, path string, runner commandRunner) (bool, error) {
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/dev/") {
		return false, fmt.Errorf("not a device under /dev: %q", path)
	}
	raw, err := runner.Output(ctx, "lsblk", "-J", "-p", "-s", "-o", "PATH,TYPE,TRAN", "--", path)
	if err != nil {
		return false, fmt.Errorf("inspect %q with lsblk: %w", path, err)
	}
	var tree struct {
		BlockDevices []transportDevice `json:"blockdevices"`
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&tree); err != nil {
		return false, fmt.Errorf("decode lsblk transport metadata: %w", err)
	}
	disks, usb := 0, false
	var walk func([]transportDevice)
	walk = func(devices []transportDevice) {
		for _, device := range devices {
			if strings.TrimSpace(device.Type) == "disk" {
				disks++
				if strings.EqualFold(strings.TrimSpace(device.Tran), "usb") {
					usb = true
				}
			}
			walk(device.Children)
		}
	}
	walk(tree.BlockDevices)
	if disks == 0 {
		return false, fmt.Errorf("lsblk found no whole disk under %q", path)
	}
	return usb, nil
}

type transportDevice struct {
	Path     string            `json:"path"`
	Type     string            `json:"type"`
	Tran     string            `json:"tran"`
	Children []transportDevice `json:"children"`
}

// USBSystemDiskReason says why USB blocking must stay off because of disk,
// the disk the system runs from, or returns "" when disk is known and not on
// USB (wiki 752 Q11). Blocking could cut off a USB system disk, so a disk
// that cannot be checked counts as one.
func USBSystemDiskReason(ctx context.Context, disk string) string {
	return usbSystemDiskReason(ctx, disk, execRunner{})
}

// MountedUSBReason is USBSystemDiskReason for the disk mounted at
// mountpoint, the way an installed system sees its own disk.
func MountedUSBReason(ctx context.Context, mountpoint string) string {
	return mountedUSBReason(ctx, mountpoint, execRunner{})
}

func mountedUSBReason(ctx context.Context, mountpoint string, runner commandRunner) string {
	raw, err := runner.Output(ctx, "findmnt", "-nvro", "SOURCE", "--target", mountpoint)
	if err != nil {
		return fmt.Sprintf("the system disk could not be found (%v).", err)
	}
	source := strings.TrimSpace(string(raw))
	// A Btrfs subvolume reads "/dev/mapper/cryptroot[/@]" without
	// --nofsroot; the suffix is not part of the device name.
	if i := strings.IndexByte(source, '['); i > 0 && strings.HasSuffix(source, "]") {
		source = source[:i]
	}
	return usbSystemDiskReason(ctx, source, runner)
}

func usbSystemDiskReason(ctx context.Context, disk string, runner commandRunner) string {
	disk = strings.TrimSpace(disk)
	if disk == "" {
		return "it is not known which disk the system runs from."
	}
	usb, err := usbAttached(ctx, disk, runner)
	switch {
	case err != nil:
		return fmt.Sprintf("it could not be checked whether the system disk is a USB drive (%v).", err)
	case usb:
		return fmt.Sprintf("the system disk (%s) is a USB drive, and blocking could cut off the disk the system runs from.", disk)
	}
	return ""
}

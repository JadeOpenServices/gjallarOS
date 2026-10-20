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

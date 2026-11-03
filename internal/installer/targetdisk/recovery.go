package targetdisk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// MountedDisk returns the one whole disk under the file system mounted at
// mountpoint, the way an installed system finds its own disk: findmnt names
// the device, lsblk -s walks from it (crypt, partition) down to the disk.
// A device on more than one disk is an error, so the caller never has to
// guess which of them is the system disk.
func MountedDisk(ctx context.Context, mountpoint string) (string, error) {
	return mountedDisk(ctx, mountpoint, execRunner{})
}

func mountedDisk(ctx context.Context, mountpoint string, runner commandRunner) (string, error) {
	raw, err := runner.Output(ctx, "findmnt", "-nvro", "SOURCE", "--target", mountpoint)
	if err != nil {
		return "", fmt.Errorf("find the device mounted at %s: %w", mountpoint, err)
	}
	source := strings.TrimSpace(string(raw))
	// A Btrfs subvolume reads "/dev/mapper/cryptroot[/@]" without
	// --nofsroot; the suffix is not part of the device name.
	if i := strings.IndexByte(source, '['); i > 0 && strings.HasSuffix(source, "]") {
		source = source[:i]
	}
	if !strings.HasPrefix(source, "/dev/") {
		return "", fmt.Errorf("%s is not mounted from a device under /dev: %q", mountpoint, source)
	}
	raw, err = runner.Output(ctx, "lsblk", "-J", "-p", "-s", "-o", "PATH,TYPE", "--", source)
	if err != nil {
		return "", fmt.Errorf("inspect %s with lsblk: %w", source, err)
	}
	devices, err := decodeBlockDevices(raw)
	if err != nil {
		return "", err
	}
	var disks []string
	walkBlockDevices(devices, func(device blockDevice) {
		if strings.TrimSpace(device.Type) == "disk" && !slices.Contains(disks, device.Path) {
			disks = append(disks, device.Path)
		}
	})
	switch len(disks) {
	case 0:
		return "", fmt.Errorf("lsblk found no whole disk under %s", source)
	case 1:
		return disks[0], nil
	}
	return "", fmt.Errorf("%s spans more than one disk (%s)", source, strings.Join(disks, ", "))
}

// PartitionByLabel returns the partition on disk whose GPT name is label, or
// "" when disk has none. Only disk is searched: a second disk or a stick
// with the same GPT name never counts (Plane 307). Two partitions with that
// name on disk are an error, since either could be the wrong one.
func PartitionByLabel(ctx context.Context, disk, label string) (string, error) {
	return partitionByLabel(ctx, disk, label, execRunner{})
}

func partitionByLabel(ctx context.Context, disk, label string, runner commandRunner) (string, error) {
	disk = strings.TrimSpace(disk)
	if !strings.HasPrefix(disk, "/dev/") {
		return "", fmt.Errorf("not a device under /dev: %q", disk)
	}
	raw, err := runner.Output(ctx, "lsblk", "-J", "-p", "-o", "PATH,TYPE,PARTLABEL", "--", disk)
	if err != nil {
		return "", fmt.Errorf("inspect %s with lsblk: %w", disk, err)
	}
	devices, err := decodeBlockDevices(raw)
	if err != nil {
		return "", err
	}
	if len(devices) != 1 || devices[0].Path != disk || strings.TrimSpace(devices[0].Type) != "disk" {
		return "", fmt.Errorf("%s is not a whole disk", disk)
	}
	var found []string
	walkBlockDevices(devices[0].Children, func(device blockDevice) {
		if strings.TrimSpace(device.Type) == "part" && device.PartLabel == label {
			found = append(found, device.Path)
		}
	})
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%s has more than one partition named %s (%s)", disk, label, strings.Join(found, ", "))
}

func decodeBlockDevices(raw []byte) ([]blockDevice, error) {
	var tree lsblkTree
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&tree); err != nil {
		return nil, fmt.Errorf("decode lsblk output: %w", err)
	}
	return tree.BlockDevices, nil
}

func walkBlockDevices(devices []blockDevice, visit func(blockDevice)) {
	for _, device := range devices {
		visit(device)
		walkBlockDevices(device.Children, visit)
	}
}

package oldroot

import (
	"encoding/json"
	"errors"
	"fmt"
)

// RootLabel is the PARTLABEL the installer gives the root partition.
const RootLabel = "GJALLAROS"

// LsblkArgs are the lsblk arguments whose output Find and Partitions read:
// JSON, one flat list, full paths, sizes in bytes.
var LsblkArgs = []string{"-J", "-l", "-p", "-b", "-o", "PATH,TYPE,FSTYPE,PARTLABEL,PKNAME,TRAN,RM,MOUNTPOINT,SIZE,MODEL"}

// maxLsblk caps the lsblk output Find reads.
const maxLsblk = 1 << 20

// Part is the root partition of an installed gjallarOS.
type Part struct {
	Path      string `json:"path"`
	Disk      string `json:"disk"`
	Encrypted bool   `json:"encrypted"`
	// Internal is false for a disk on USB or marked removable.
	Internal bool `json:"internal"`
	// Size in bytes and Model of the disk, for the screen.
	Size  int64  `json:"size"`
	Model string `json:"model,omitempty"`
}

// Find picks the gjallarOS root partitions from the output of lsblk
// LsblkArgs. A disk with anything mounted from it is left out: that is
// the stick itself, or a disk something else is using.
func Find(raw []byte) ([]Part, error) {
	if len(raw) > maxLsblk {
		return nil, errors.New("lsblk printed too much")
	}
	var out struct {
		BlockDevices []struct {
			Path       string `json:"path"`
			Type       string `json:"type"`
			FSType     string `json:"fstype"`
			PartLabel  string `json:"partlabel"`
			PKName     string `json:"pkname"`
			Tran       string `json:"tran"`
			RM         any    `json:"rm"`
			MountPoint string `json:"mountpoint"`
			Size       int64  `json:"size"`
			Model      string `json:"model"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode lsblk: %w", err)
	}
	type disk struct {
		internal bool
		size     int64
		model    string
	}
	disks := map[string]disk{}
	busy := map[string]bool{}
	for _, d := range out.BlockDevices {
		if d.Type == "disk" {
			disks[d.Path] = disk{internal: d.Tran != "usb" && !removable(d.RM), size: d.Size, model: printable(d.Model)}
			if d.MountPoint != "" {
				busy[d.Path] = true
			}
		}
	}
	for _, d := range out.BlockDevices {
		if d.Type == "part" && d.MountPoint != "" {
			busy[d.PKName] = true
		}
	}
	var parts []Part
	for _, d := range out.BlockDevices {
		dk, ok := disks[d.PKName]
		if d.Type != "part" || d.PartLabel != RootLabel || !ok || busy[d.PKName] {
			continue
		}
		p := Part{Path: d.Path, Disk: d.PKName, Internal: dk.internal, Size: dk.size, Model: dk.model}
		switch d.FSType {
		case "crypto_LUKS":
			p.Encrypted = true
		case "btrfs":
		default:
			continue
		}
		parts = append(parts, p)
	}
	return parts, nil
}

// Partitions lists every partition of disk in the same lsblk output, so all
// of them can be set read-only.
func Partitions(raw []byte, disk string) ([]string, error) {
	if len(raw) > maxLsblk {
		return nil, errors.New("lsblk printed too much")
	}
	var out struct {
		BlockDevices []struct {
			Path   string `json:"path"`
			Type   string `json:"type"`
			PKName string `json:"pkname"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode lsblk: %w", err)
	}
	var parts []string
	for _, d := range out.BlockDevices {
		if d.Type == "part" && d.PKName == disk {
			parts = append(parts, d.Path)
		}
	}
	return parts, nil
}

// removable reads lsblk's RM column, a bool in newer util-linux and "0" or
// "1" in older ones. Anything else counts as removable.
func removable(v any) bool {
	switch rm := v.(type) {
	case bool:
		return rm
	case string:
		return rm != "0"
	case float64:
		return rm != 0
	}
	return true
}

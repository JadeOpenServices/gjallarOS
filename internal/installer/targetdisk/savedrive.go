package targetdisk

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// LogsLabel is the label of the log partition the USB maker puts on the
// boot stick (Plane 344). The installer only uses it; it never creates it.
const LogsLabel = "GJLOGS"

// The makers write GJLOGS as a 32 MiB FAT image into MBR slot 3 (L76). On a
// gjallarOS stick only a partition that still looks like that is used
// (L77): the stick's other FAT partition is its boot partition.
const (
	logsPartN    = 3
	logsFSType   = "vfat"
	logsMaxBytes = 64 << 20
)

// saveFSTypes are the filesystems a report or log is saved to. Both are
// read on any computer, and both are simple for the kernel to mount: the
// mount runs as root on a drive anyone could have handed over.
var saveFSTypes = map[string]bool{"vfat": true, "exfat": true}

// SaveDrive is a partition on a USB drive a failed run may save its report
// and log to.
type SaveDrive struct {
	Path   string
	FSType string
	UUID   string
	// Label is the drive's own text: model, label and size, cleaned so it
	// prints safely.
	Label string
	// BootStick is the GJLOGS partition of the stick this run started from:
	// the disk below the live system's /iso mount, not a disk that only
	// carries the stick's label.
	BootStick bool
	// MountPoint is set when the partition is mounted already, by the
	// desktop for its user; the files are written there as that user.
	MountPoint string
}

type saveDevice struct {
	Path        string       `json:"path"`
	Type        string       `json:"type"`
	PartN       int          `json:"partn"`
	Tran        string       `json:"tran"`
	FSType      string       `json:"fstype"`
	UUID        string       `json:"uuid"`
	Label       string       `json:"label"`
	Model       string       `json:"model"`
	Size        uint64       `json:"size"`
	RO          bool         `json:"ro"`
	MountPoints []string     `json:"mountpoints"`
	Children    []saveDevice `json:"children"`
}

// SaveDrives lists the partitions a failed run may save to. target is the
// disk the run installs to, "" when none was chosen yet. A partition
// qualifies when its disk is attached over USB and writable, is not target,
// holds nothing the system uses, and the partition is FAT or exFAT. On a
// gjallarOS stick only its GJLOGS partition qualifies: the stick's own boot
// partition is FAT as well. Only the stick this run started from is marked
// and listed first; another gjallarOS stick is listed like any drive.
func SaveDrives(ctx context.Context, target string) ([]SaveDrive, error) {
	return saveDrives(ctx, target, execRunner{})
}

func saveDrives(ctx context.Context, target string, runner commandRunner) ([]SaveDrive, error) {
	raw, err := runner.Output(ctx, "lsblk", saveArgs()...)
	if err != nil {
		return nil, fmt.Errorf("list drives with lsblk: %w", err)
	}
	var tree struct {
		BlockDevices []saveDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("parse lsblk drive list: %w", err)
	}
	target = resolveDisk(target)
	var drives []SaveDrive
	for _, disk := range tree.BlockDevices {
		if disk.Type != "disk" || !strings.EqualFold(disk.Tran, "usb") || disk.RO {
			continue
		}
		if resolveDisk(disk.Path) == target || saveSystemUse(disk) {
			continue
		}
		bootStick := mountsInstallMedia(disk)
		stick := bootStick || saveInstallMedia(disk)
		for _, part := range disk.Children {
			drive, ok := savePartition(disk, part, stick, bootStick)
			if ok {
				drives = append(drives, drive)
			}
		}
	}
	// The boot stick's GJLOGS partition first: the USB maker made it for
	// exactly this.
	sort.SliceStable(drives, func(i, j int) bool { return drives[i].BootStick && !drives[j].BootStick })
	return drives, nil
}

// CheckSaveDrive looks at path again right before it is mounted: it must
// still be the same filesystem on a drive that qualifies.
func CheckSaveDrive(ctx context.Context, target string, want SaveDrive) error {
	return checkSaveDrive(ctx, target, want, execRunner{})
}

func checkSaveDrive(ctx context.Context, target string, want SaveDrive, runner commandRunner) error {
	drives, err := saveDrives(ctx, target, runner)
	if err != nil {
		return err
	}
	for _, drive := range drives {
		if drive.Path == want.Path && drive.UUID == want.UUID && drive.FSType == want.FSType && drive.MountPoint == want.MountPoint {
			return nil
		}
	}
	return fmt.Errorf("%s changed since it was listed; nothing written", want.Path)
}

func saveArgs() []string {
	return []string{"-J", "-b", "-p", "--tree", "-o", "PATH,TYPE,PARTN,TRAN,FSTYPE,UUID,LABEL,MODEL,SIZE,RO,MOUNTPOINTS"}
}

func savePartition(disk, part saveDevice, stick, bootStick bool) (SaveDrive, bool) {
	if part.Type != "part" || part.RO || len(part.Children) > 0 || !saveFSTypes[part.FSType] {
		return SaveDrive{}, false
	}
	if strings.TrimSpace(part.UUID) == "" || !strings.HasPrefix(part.Path, "/dev/") || strings.Contains(part.Path, "..") {
		return SaveDrive{}, false
	}
	if stick && !logsPartition(part) {
		return SaveDrive{}, false
	}
	var mountPoint string
	for _, mount := range part.MountPoints {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			continue
		}
		// Only the desktop's mounts for a user count; anything else on it
		// belongs to the system.
		if mountPoint != "" || !strings.HasPrefix(filepath.Clean(mount)+"/", "/run/media/") {
			return SaveDrive{}, false
		}
		mountPoint = filepath.Clean(mount)
	}
	label := strings.Join(nonEmpty(printable(disk.Model), printable(part.Label), humanSize(part.Size)), ", ")
	if bootStick {
		label += ", the stick this run started from"
	}
	return SaveDrive{
		Path:       part.Path,
		FSType:     part.FSType,
		UUID:       part.UUID,
		Label:      printable(part.Path) + " (" + label + ")",
		BootStick:  bootStick,
		MountPoint: mountPoint,
	}, true
}

// saveSystemUse is true when the system uses something on the disk: a
// mount other than a desktop mount for a user or the boot stick's own,
// swap, or a crypt, LVM or RAID device on top. An install target or a
// recovery disk mounted at /mnt is skipped this way even before the run
// chose its target.
func saveSystemUse(device saveDevice) bool {
	switch device.Type {
	case "disk", "part":
	default:
		// The live system's /iso is a device-mapper copy of its boot
		// stick. It marks that stick, not a use of it.
		return !installMediaMap(device)
	}
	if device.FSType == "swap" || strings.HasPrefix(device.FSType, "crypto_") || strings.HasSuffix(device.FSType, "_member") {
		return true
	}
	for _, mount := range device.MountPoints {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			continue
		}
		mount = filepath.Clean(mount)
		if installationMediaMounts[mount] || strings.HasPrefix(mount+"/", "/run/media/") {
			continue
		}
		return true
	}
	for _, child := range device.Children {
		if saveSystemUse(child) {
			return true
		}
	}
	return false
}

// logsPartition is the GJLOGS partition as the makers write it.
func logsPartition(part saveDevice) bool {
	return part.Label == LogsLabel && part.PartN == logsPartN && part.FSType == logsFSType &&
		part.Size > 0 && part.Size <= logsMaxBytes
}

// mountsInstallMedia is true for the disk the live system started from:
// its /iso mount sits on it.
func mountsInstallMedia(device saveDevice) bool {
	for _, mount := range device.MountPoints {
		if installationMediaMounts[filepath.Clean(strings.TrimSpace(mount))] {
			return true
		}
	}
	for _, child := range device.Children {
		if mountsInstallMedia(child) {
			return true
		}
	}
	return false
}

// installMediaMap is the device-mapper or loop device stage 1 puts over
// the boot stick, mounted only at the installation media mount.
func installMediaMap(device saveDevice) bool {
	if (device.Type != "dm" && device.Type != "loop") || len(device.Children) > 0 {
		return false
	}
	found := false
	for _, mount := range device.MountPoints {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			continue
		}
		if !installationMediaMounts[filepath.Clean(mount)] {
			return false
		}
		found = true
	}
	return found
}

func saveInstallMedia(device saveDevice) bool {
	if bootMediaLabel(device.Label) || device.FSType == "iso9660" {
		return true
	}
	for _, mount := range device.MountPoints {
		if installationMediaMounts[filepath.Clean(strings.TrimSpace(mount))] {
			return true
		}
	}
	for _, child := range device.Children {
		if saveInstallMedia(child) {
			return true
		}
	}
	return false
}

// printable keeps letters, digits, punctuation and spaces of a drive's own
// text: a stick's label or model must not carry terminal escape codes.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	text := []rune(strings.TrimSpace(b.String()))
	if len(text) > 40 {
		text = text[:40]
	}
	return string(text)
}

// resolveDisk turns a /dev/disk/by-id link into the device it names, so a
// target given by its ID still matches lsblk's path.
func resolveDisk(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func humanSize(bytes uint64) string {
	const unit = 1000
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value, suffix := float64(bytes), ""
	for _, s := range []string{"kB", "MB", "GB", "TB"} {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}

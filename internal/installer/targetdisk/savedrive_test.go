package targetdisk

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// saveTree is a SYNTHETIC lsblk tree. targetPath stands in for the disk the
// run installs to; the test names it through a by-id style link.
func saveTree(t *testing.T, targetPath string) []byte {
	t.Helper()
	part := func(path, fstype, uuid, label string, mounts ...any) map[string]any {
		if len(mounts) == 0 {
			mounts = []any{nil}
		}
		return map[string]any{"path": path, "type": "part", "tran": nil, "fstype": fstype, "uuid": uuid,
			"label": label, "model": nil, "size": 2000000000, "ro": false, "mountpoints": mounts}
	}
	disk := func(path, tran, model string, ro bool, children ...map[string]any) map[string]any {
		return map[string]any{"path": path, "type": "disk", "tran": tran, "fstype": nil, "uuid": nil,
			"label": nil, "model": model, "size": 16000000000, "ro": ro, "mountpoints": []any{nil}, "children": children}
	}
	// A stick as the USB maker writes it (L76): the image's two partitions,
	// GJLOGS in slot 3, and stage 1's /iso map over the whole disk.
	stick := func(path, model string, iso bool, logs map[string]any) map[string]any {
		d := disk(path, "usb", model, false,
			numbered(part(path+"1", "iso9660", "2026-11-15-00-00-00-00", "GJALLAR_STICK"), 1),
			numbered(part(path+"2", "vfat", "AAAA-0001", "EFIBOOT"), 2),
			logs)
		d["fstype"], d["label"] = "iso9660", "GJALLAR_STICK"
		if iso {
			d["children"] = append(d["children"].([]map[string]any),
				map[string]any{"path": "/dev/mapper/gjallar-iso", "type": "dm", "fstype": "iso9660", "label": "GJALLAR_STICK",
					"size": 1900000000, "ro": true, "mountpoints": []any{"/iso"}})
		}
		return d
	}
	logs := func(path, uuid string) map[string]any {
		p := numbered(part(path, "vfat", uuid, LogsLabel), 3)
		p["size"] = 32 << 20
		return p
	}
	bootStick := stick("/dev/sdc", "Boot Stick", true, logs("/dev/sdc3", "474A-4C47"))
	// Another gjallarOS stick: its GJLOGS is listed, but not as this run's.
	otherStick := stick("/dev/sdl", "Other Stick", false, logs("/dev/sdl3", "474A-4C47"))
	// gjallarOS sticks whose third partition no longer looks like GJLOGS.
	bigLogs := logs("/dev/sdm3", "MMMM-0003")
	bigLogs["size"] = 65 << 20
	exfatLogs := logs("/dev/sdn3", "NNNN-0003")
	exfatLogs["fstype"] = "exfat"
	tree := map[string]any{"blockdevices": []any{
		disk("/dev/nvme0n1", "nvme", "Internal", false, part("/dev/nvme0n1p1", "vfat", "BBBB-0001", "")),
		disk(targetPath, "usb", "Target Disk", false, part("/dev/sdb1", "vfat", "CCCC-0001", "")),
		disk("/dev/sdd", "usb", "Desktop Stick", false, part("/dev/sdd1", "vfat", "DDDD-0001", "STICK", "/run/media/nixos/STICK")),
		disk("/dev/sde", "usb", "Mounted Disk", false,
			part("/dev/sde1", "vfat", "EEEE-0001", "", "/mnt/boot"),
			part("/dev/sde2", "exfat", "EEEE-0002", "DATA")),
		disk("/dev/sdf", "usb", "\x1b]0;owned\x07Odd\x1b[31mStick", false,
			part("/dev/sdf1", "ext4", "ffffffff-0001", "LINUX"),
			part("/dev/sdf2", "exfat", "FFFF-0002", "\x1b[2Jwipe")),
		disk("/dev/sdg", "usb", "Locked Stick", true, part("/dev/sdg1", "vfat", "GGGG-0001", "")),
		disk("/dev/sdh", "usb", "No UUID", false, part("/dev/sdh1", "vfat", "", "")),
		disk("/dev/sdi", "usb", "Encrypted", false,
			part("/dev/sdi1", "crypto_LUKS", "iiiiiiii-0001", ""),
			part("/dev/sdi2", "vfat", "IIII-0002", "")),
		disk("/dev/sdj", "usb", "Swap Stick", false, part("/dev/sdj1", "swap", "jjjjjjjj-0001", "")),
		disk("/dev/sdk", "usb", "Path Stick", false, part("/dev/../etc/sdk1", "vfat", "KKKK-0001", "")),
		bootStick,
		otherStick,
		stick("/dev/sdm", "Big Logs", false, bigLogs),
		stick("/dev/sdn", "exFAT Logs", false, exfatLogs),
		stick("/dev/sdo", "Slot Two", false, numbered(logs("/dev/sdo4", "OOOO-0004"), 4)),
	}}
	raw, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func numbered(part map[string]any, n int) map[string]any {
	part["partn"] = n
	return part
}

// targetLink makes a disk file and a link to it, the way /dev/disk/by-id
// names a disk.
func targetLink(t *testing.T) (disk, link string) {
	t.Helper()
	dir := t.TempDir()
	disk = filepath.Join(dir, "sdb")
	if err := os.WriteFile(disk, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(dir, "usb-Target_Disk-0:0")
	if err := os.Symlink(disk, link); err != nil {
		t.Fatal(err)
	}
	return disk, link
}

func TestSaveDrivesOffersOnlyFreeUSBPartitions(t *testing.T) {
	disk, link := targetLink(t)
	runner := &fakeRunner{output: saveTree(t, disk)}
	drives, err := saveDrives(context.Background(), link, runner)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, drive := range drives {
		paths = append(paths, drive.Path)
	}
	want := []string{"/dev/sdc3", "/dev/sdd1", "/dev/sdf2", "/dev/sdl3"}
	if strings.Join(paths, " ") != strings.Join(want, " ") {
		t.Fatalf("drives = %v, want %v", paths, want)
	}
	if !drives[0].BootStick || !strings.Contains(drives[0].Label, "the stick this run started from") {
		t.Errorf("boot stick GJLOGS not marked: %+v", drives[0])
	}
	if last := drives[len(drives)-1]; last.BootStick || strings.Contains(last.Label, "started from") {
		t.Errorf("another stick's GJLOGS marked as this run's: %+v", last)
	}
	if drives[1].MountPoint != "/run/media/nixos/STICK" {
		t.Errorf("desktop mount lost: %+v", drives[1])
	}
	for _, drive := range drives {
		if strings.ContainsRune(drive.Label, 0x1b) || strings.ContainsRune(drive.Label, 0x07) {
			t.Errorf("label carries control codes: %q", drive.Label)
		}
	}
	if got := strings.Join(runner.calls[0], " "); got != "lsblk "+strings.Join(saveArgs(), " ") {
		t.Errorf("lsblk call = %q", got)
	}
}

// Before the run chose a target, the same disk is offered: only the
// target filter keeps it out above.
func TestSaveDrivesWithoutTarget(t *testing.T) {
	disk, _ := targetLink(t)
	drives, err := saveDrives(context.Background(), "", &fakeRunner{output: saveTree(t, disk)})
	if err != nil {
		t.Fatal(err)
	}
	if len(drives) != 5 || drives[1].Path != "/dev/sdb1" {
		t.Fatalf("drives = %+v, want the target disk's partition second", drives)
	}
}

func TestCheckSaveDriveRefusesChangedDrive(t *testing.T) {
	disk, link := targetLink(t)
	runner := &fakeRunner{output: saveTree(t, disk)}
	drives, err := saveDrives(context.Background(), link, runner)
	if err != nil || len(drives) == 0 {
		t.Fatalf("drives = %v, %v", drives, err)
	}
	if err := checkSaveDrive(context.Background(), link, drives[2], runner); err != nil {
		t.Fatalf("unchanged drive refused: %v", err)
	}
	swapped := drives[2]
	swapped.UUID = "ZZZZ-9999"
	if err := checkSaveDrive(context.Background(), link, swapped, runner); err == nil {
		t.Fatal("drive with another filesystem accepted")
	}
	// The drive listed as unmounted got mounted somewhere since.
	moved := drives[1]
	moved.MountPoint = ""
	if err := checkSaveDrive(context.Background(), link, moved, runner); err == nil {
		t.Fatal("drive with another mount accepted")
	}
	// The run picked the drive as its target since.
	if err := checkSaveDrive(context.Background(), "/dev/sdf", drives[2], runner); err == nil {
		t.Fatal("drive that became the target accepted")
	}
}

func TestPrintableCutsControlCodes(t *testing.T) {
	if got := printable("\x1b[31mRED\x1b[0m\r\n" + strings.Repeat("x", 60)); strings.ContainsAny(got, "\x1b\r\n") || len([]rune(got)) > 40 {
		t.Fatalf("printable = %q", got)
	}
}

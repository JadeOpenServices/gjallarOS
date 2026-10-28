package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	sysfs := t.TempDir()
	dev := filepath.Join(sysfs, "devices/pci0000:00/usb1/1-1")
	write(t, filepath.Join(dev, "idVendor"), "090c\n")
	write(t, filepath.Join(dev, "idProduct"), "1000\n")
	write(t, filepath.Join(dev, "product"), "SYNTHETIC\n")
	write(t, filepath.Join(dev, "1-1:1.0/bInterfaceClass"), "08\n")
	lun := filepath.Join(dev, "1-1:1.0/host6/target6:0:0/6:0:0:0")
	write(t, filepath.Join(lun, "type"), "0\n")
	write(t, filepath.Join(lun, "model"), "SD/MMC\n")
	write(t, filepath.Join(lun, "inquiry"), "\x00\x80")
	if err := os.MkdirAll(filepath.Join(sysfs, "bus/scsi/devices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lun, filepath.Join(sysfs, "bus/scsi/devices/6:0:0:0")); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(lun, "block/sda")
	if err := os.MkdirAll(disk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lun, filepath.Join(disk, "device")); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	devpath := strings.TrimPrefix(disk, sysfs)
	if err := run(sysfs, devpath, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "GJALLAR_STORAGE_KIND=sd-card\n" {
		t.Fatalf("output = %q", out.String())
	}

	for _, bad := range []string{"/sys/../etc", "devices/x", "/devices/../../etc"} {
		if err := run(sysfs, bad, &out); err == nil {
			t.Errorf("run(%q) accepted", bad)
		}
	}
}

package installreport

import (
	"os"
	"path/filepath"
	"strings"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// ModeFlags reads Secure Boot and Setup Mode; secureboot.ReadModeFlags on
// a live machine.
type ModeFlags func() (secureBoot, setupMode secureboot.TriState, err error)

// Collect fills the machine part of a report from root ("/" when live).
// Every value here is readable without root, and none is a serial number:
// ODDC's identity leaves out DMI serials and UUIDs, and its device IDs
// are vendor and model numbers.
func Collect(root string, flags ModeFlags) Report {
	facts := portable.ReadFacts(filepath.Join(root, "sys"))
	r := Report{
		Machine: facts.Identity,
		Devices: facts.Devices,
		Kernel:  readLine(filepath.Join(root, "proc/sys/kernel/osrelease")),
	}
	if _, err := os.Stat(filepath.Join(root, "sys/firmware/efi")); err == nil {
		r.Firmware.UEFI = true
		if flags != nil {
			if secureBoot, setupMode, err := flags(); err == nil {
				r.Firmware.SecureBoot = string(secureBoot)
				r.Firmware.SetupMode = string(setupMode)
			}
		}
	}
	r.Firmware.TPM = diskcrypto.TPMMajorVersion(root)
	return r
}

// DiskSerials lists the serial numbers and WWIDs of the disks under root,
// for Redact to replace. They are only read, never put in the report.
func DiskSerials(root string) []string {
	var serials []string
	disks, _ := filepath.Glob(filepath.Join(root, "sys/block/*"))
	for _, disk := range disks {
		for _, name := range []string{"device/serial", "device/wwid", "wwid"} {
			if v := readLine(filepath.Join(disk, name)); v != "" {
				serials = append(serials, v)
			}
		}
	}
	return serials
}

func readLine(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	buf := make([]byte, 256)
	n, _ := file.Read(buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	return strings.TrimSpace(line)
}

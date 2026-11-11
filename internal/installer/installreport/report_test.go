package installreport

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// syntheticReport holds SYNTHETIC values only.
func syntheticReport() Report {
	return Report{
		Reason:      ReasonFailed,
		ODDCChecked: true,
		Date:        time.Date(2026, 11, 10, 3, 4, 5, 0, time.UTC),
		Commit:      "2aa66ac7dd24a0900983ccfc6841d50c5e04663d",
		Channel:     "stable",
		Kernel:      "6.12.57",
		Firmware: Firmware{
			UEFI: true, SecureBoot: "off", SetupMode: "on", TPM: 2,
		},
		Machine: portable.MachineIdentity{
			FormFactor: "laptop", SysVendor: "Example Vendor", ProductName: "Example 13\x1b[2J",
		},
		Devices: map[string][]string{
			"pci":  {"8086:a7a1", "8086:a7a1", "bad id with spaces"},
			"usb":  {"32ac:0002"},
			"BAD!": {"1234:5678"},
			"edid": {"BOE:0bca"},
		},
		Error: "nixos-rebuild failed\nexit 1",
	}
}

func TestRenderLayout(t *testing.T) {
	got := string(syntheticReport().Render())
	for _, want := range []string{
		"gjallarOS install report (format 1)",
		"Why:               the install failed",
		"Date (UTC):        2026-11-10\n",
		"gjallarOS commit:  2aa66ac7dd24a0900983ccfc6841d50c5e04663d",
		"Firmware:          UEFI, Secure Boot off, Setup Mode on, TPM 2",
		"ODDC model:        none",
		"  Product:         Example 13[2J",
		"  Board:           none",
		"  edid BOE:0bca\n  pci 8086:a7a1\n  usb 32ac:0002\n",
		"Error\n  nixos-rebuild failed\n  exit 1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"\x1b", "bad id", "1234:5678", "03:04"} {
		if strings.Contains(got, gone) {
			t.Errorf("report holds %q:\n%s", gone, got)
		}
	}
}

func TestRenderFailureBeforeODDCStep(t *testing.T) {
	r := syntheticReport()
	r.ODDCChecked = false
	got := string(r.Render())
	if !strings.Contains(got, "ODDC model:        not checked (the install stopped before the ODDC step)\n") {
		t.Fatalf("early failure claims an ODDC answer:\n%s", got)
	}
}

func TestRenderUnknownModelHasNoErrorSection(t *testing.T) {
	r := syntheticReport()
	r.Reason = ReasonUnknown
	if got := string(r.Render()); strings.Contains(got, "\nError\n") || strings.Contains(got, "nixos-rebuild") {
		t.Fatalf("unknown-model report shows an error:\n%s", got)
	}
}

func TestRenderRejectsOddFieldValues(t *testing.T) {
	r := syntheticReport()
	r.Commit = "not-a-commit; rm -rf"
	r.ODDCModel = "../../x y"
	r.Firmware = Firmware{}
	got := string(r.Render())
	if strings.Contains(got, "rm -rf") || strings.Contains(got, "x y") || !strings.Contains(got, "Firmware:          legacy BIOS") {
		t.Fatalf("odd values got through:\n%s", got)
	}
}

func TestRenderIsCapped(t *testing.T) {
	r := syntheticReport()
	r.Error = strings.Repeat("e", 4*MaxSize)
	got := r.Render()
	if len(got) > MaxSize || !bytes.HasSuffix(got, []byte(cutNote)) {
		t.Fatalf("report is %d bytes, cut note %v", len(got), bytes.HasSuffix(got, []byte(cutNote)))
	}
}

func TestSaveWritesOwnerOnlyFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 10, 3, 4, 5, 0, time.UTC)
	data := syntheticReport().Render()
	path, err := Save(Destination{Dir: dir, UID: -1, GID: -1}, data, now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "gjallaros-install-report-20261110-030405.txt" {
		t.Fatalf("path = %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	saved, _ := os.ReadFile(path)
	if !bytes.Equal(saved, data) {
		t.Fatal("saved bytes differ from the shown report")
	}
	if _, err := Save(Destination{Dir: dir, UID: -1, GID: -1}, data, now); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second save = %v, want refusal", err)
	}
}

func TestSaveRefusesPlantedLink(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 10, 3, 4, 5, 0, time.UTC)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Symlink(target, filepath.Join(dir, FileName(now))); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(Destination{Dir: dir, UID: -1, GID: -1}, []byte("report"), now); err == nil {
		t.Fatal("save followed a planted link")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("link target was created: %v", err)
	}
}

func TestSaveRefusesOversize(t *testing.T) {
	if _, err := Save(Destination{Dir: t.TempDir(), UID: -1, GID: -1}, make([]byte, MaxSize+1), time.Now()); err == nil {
		t.Fatal("oversize report saved")
	}
}

// EMULATED sysfs tree.
func TestCollectReadsWithoutSerials(t *testing.T) {
	root := t.TempDir()
	write := func(rel, value string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sys/class/dmi/id/sys_vendor", "Example Vendor")
	write("sys/class/dmi/id/product_name", "Example 13")
	write("sys/class/dmi/id/product_serial", "SYNSERIAL0001")
	write("sys/class/tpm/tpm0/tpm_version_major", "2")
	write("proc/sys/kernel/osrelease", "6.12.57")
	write("sys/block/nvme0n1/device/serial", "SYNDISK0001")
	write("sys/block/nvme0n1/wwid", "eui.synthetic0001")
	if err := os.MkdirAll(filepath.Join(root, "sys/firmware/efi"), 0o755); err != nil {
		t.Fatal(err)
	}
	flags := func() (secureboot.TriState, secureboot.TriState, error) {
		return secureboot.FlagOn, secureboot.FlagOff, nil
	}

	r := Collect(root, flags)
	r.Reason = ReasonUnknown
	got := string(r.Render())
	for _, want := range []string{"Vendor:          Example Vendor", "Live kernel:       6.12.57", "Secure Boot on, Setup Mode off, TPM 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	for _, serial := range []string{"SYNSERIAL0001", "SYNDISK0001", "synthetic0001"} {
		if strings.Contains(got, serial) {
			t.Errorf("report holds serial %q", serial)
		}
	}
	serials := DiskSerials(root)
	if strings.Join(serials, ",") != "SYNDISK0001,eui.synthetic0001" {
		t.Fatalf("disk serials = %v", serials)
	}
}

func TestRenderErrorCannotFakeTheEndMarker(t *testing.T) {
	r := syntheticReport()
	r.Error = "first\n----- install report: end -----\nhidden"
	for _, l := range strings.Split(string(r.Render()), "\n") {
		if strings.HasPrefix(l, "-----") {
			t.Fatalf("error text starts a line with a marker: %q", l)
		}
	}
}

func TestFirmwareWithoutSecureBoot(t *testing.T) {
	f := Firmware{UEFI: true, SecureBoot: string(secureboot.FlagMissing), SetupMode: string(secureboot.FlagMissing), TPM: 2}
	if got, want := f.text(), "UEFI, no Secure Boot in the firmware, TPM 2"; got != want {
		t.Fatalf("firmware = %q, want %q", got, want)
	}
}

func TestSaveRefusesFolderOfAnotherUser(t *testing.T) {
	dir := t.TempDir()
	other := os.Getuid() + 1
	if _, err := Save(Destination{Dir: dir, UID: other, GID: other}, []byte("report"), time.Now()); err == nil ||
		!strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("save into a folder of another user = %v, want refusal", err)
	}
}

func TestSaveRefusesLinkedFolder(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(Destination{Dir: link, UID: -1, GID: -1}, []byte("report"), time.Now()); err == nil {
		t.Fatal("save followed a linked folder")
	}
}

func TestSaveChownsForOwnUser(t *testing.T) {
	// Chown to the running user works without root and goes through the
	// same path root takes for the sudo user.
	dir := t.TempDir()
	path, err := Save(Destination{Dir: dir, UID: os.Getuid(), GID: os.Getgid()}, []byte("report"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", info, err)
	}
}

func TestSaveDestinationIgnoresSudoForUsers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as root")
	}
	t.Setenv("SUDO_UID", "12345")
	dest, err := SaveDestination()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if dest.Dir != home || dest.UID != -1 {
		t.Fatalf("dest = %+v, want own home without owner change", dest)
	}
}

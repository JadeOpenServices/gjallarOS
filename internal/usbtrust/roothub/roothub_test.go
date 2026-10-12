package roothub

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

type fakeHub struct {
	pci     string // PCI device dir below devices/
	vendor  string
	device  string
	bus     string
	version string
}

func writeSysfs(t *testing.T, hubs []fakeHub) string {
	t.Helper()

	root := t.TempDir()
	links := filepath.Join(root, "bus", "usb", "devices")

	if err := os.MkdirAll(links, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(path, value string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, hub := range hubs {
		pci := filepath.Join(root, "devices", hub.pci)
		dir := filepath.Join(pci, "usb"+hub.bus)

		write(filepath.Join(pci, "vendor"), hub.vendor)
		write(filepath.Join(pci, "device"), hub.device)
		write(filepath.Join(dir, "version"), hub.version)

		if err := os.Symlink(dir, filepath.Join(links, "usb"+hub.bus)); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

// EMULATED: the root hubs of the e2e-fw13 VM (QEMU xHCI 1b36:000d),
// read from its /sys on 2026-10-12.
var emulatedFW13Hubs = []fakeHub{
	{"pci0000:00/0000:00:04.0", "0x1b36", "0x000d", "1", " 2.00"},
	{"pci0000:00/0000:00:04.0", "0x1b36", "0x000d", "2", " 3.00"},
}

func TestRootHubsEmulatedFW13(t *testing.T) {
	hubs, err := RootHubs(writeSysfs(t, emulatedFW13Hubs))
	if err != nil {
		t.Fatal(err)
	}

	want := []RootHub{
		{Bus: "1", Controller: "1b36:000d", Hub: "usb2"},
		{Bus: "2", Controller: "1b36:000d", Hub: "usb3"},
	}

	if !reflect.DeepEqual(hubs, want) {
		t.Fatalf("got %+v, want %+v", hubs, want)
	}
}

func TestWiringUsesHubVersionNotBusNumber(t *testing.T) {
	hubs := []RootHub{
		{Bus: "1", Controller: "1b36:000d", Hub: "usb2"},
		{Bus: "2", Controller: "1b36:000d", Hub: "usb3"},
	}

	// The VM's storage stick sits at kernel port 2-2: root port 2 of the
	// USB 3 root hub, which the kernel numbers as bus 2.
	got := Wiring("2-2", hubs)
	want := &usbtrust.ObservedPort{Controller: "1b36:000d", Hub: "usb3", RootPort: 2}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	for _, port := range []string{"usb2", "2-2.1", "3-1", "", "2-0"} {
		if got := Wiring(port, hubs); got != nil {
			t.Fatalf("Wiring(%q) = %+v, want nil", port, got)
		}
	}
}

func TestWiringMarksSharedControllerID(t *testing.T) {
	// SYNTHETIC: two copies of the emulated controller, so the same
	// controller ID and hub appear twice.
	hubs := []RootHub{
		{Bus: "1", Controller: "1b36:000d", Hub: "usb2"},
		{Bus: "2", Controller: "1b36:000d", Hub: "usb3"},
		{Bus: "3", Controller: "1b36:000d", Hub: "usb2"},
		{Bus: "4", Controller: "1b36:000d", Hub: "usb3"},
	}

	got := Wiring("3-1", hubs)
	if got == nil || !got.Shared || got.Hub != "usb2" || got.RootPort != 1 {
		t.Fatalf("got %+v, want shared usb2 port 1", got)
	}
}

func TestRootHubsSkipsNonPCIAndOldVersions(t *testing.T) {
	// SYNTHETIC: a USB 1.1 root hub and a hub whose parent has no PCI ID.
	root := writeSysfs(t, []fakeHub{
		{"pci0000:00/0000:00:1d.0", "0x8086", "0x24c2", "1", " 1.10"},
	})

	platform := filepath.Join(root, "devices", "platform", "xhci-hcd.0", "usb2")
	if err := os.MkdirAll(platform, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platform, "version"), []byte(" 2.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(platform, filepath.Join(root, "bus", "usb", "devices", "usb2")); err != nil {
		t.Fatal(err)
	}

	hubs, err := RootHubs(root)
	if err != nil {
		t.Fatal(err)
	}

	if Wiring("1-1", hubs) != nil || Wiring("2-1", hubs) != nil {
		t.Fatalf("want no wiring for %+v", hubs)
	}
}

func TestAttachWiringPrefersViaPort(t *testing.T) {
	hubs := []RootHub{{Bus: "2", Controller: "1b36:000d", Hub: "usb3"}}

	devices := []usbtrust.ObservedDevice{
		{RuntimeID: "31", Identity: usbtrust.Identity{Port: "2-2"}},
		{RuntimeID: "2-3"},
		{RuntimeID: "32"},
	}

	AttachWiring(devices, hubs)

	if devices[0].Wiring == nil || devices[0].Wiring.RootPort != 2 {
		t.Fatalf("via-port device: %+v", devices[0].Wiring)
	}
	if devices[1].Wiring == nil || devices[1].Wiring.RootPort != 3 {
		t.Fatalf("sysfs device: %+v", devices[1].Wiring)
	}
	if devices[2].Wiring != nil {
		t.Fatalf("numeric id must not wire: %+v", devices[2].Wiring)
	}
}

package btaddr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// SYNTHETIC: the addresses below are made up for the tests.

// info builds a struct hci_dev_info with the address given in the usual
// printed order and the bus in the type byte.
func info(bus byte, address ...byte) []byte {
	raw := make([]byte, devInfoSize)
	for i, b := range address {
		raw[devInfoAddress+5-i] = b
	}
	raw[devInfoType] = bus

	return raw
}

// controller adds /sys/class/bluetooth/<hci> pointing at a USB interface.
func controller(t *testing.T, root, hci, device string) {
	t.Helper()

	iface := filepath.Join(root, "devices", "pci0000:00", "usb1", device)
	if err := os.MkdirAll(iface, 0o755); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, "class", "bluetooth", hci)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(iface, filepath.Join(dir, "device")); err != nil {
		t.Fatal(err)
	}
}

func TestAddresses(t *testing.T) {
	root := t.TempDir()
	controller(t, root, "hci0", "1-5/1-5:1.0")
	controller(t, root, "hci1", "1-2/1-2.3/1-2.3:1.0")
	// Not on USB: the parent is not the device the interface names.
	controller(t, root, "hci2", "serial0/serial0-0")
	controller(t, root, "hci3", "1-7/1-7:1.0")
	controller(t, root, "hci4", "1-8/1-8:1.0")

	raw := map[int][]byte{
		0: info(busUSB, 0x02, 0x11, 0x22, 0x33, 0x44, 0x55),
		1: info(busUSB, 0x02, 0x66, 0x77, 0x88, 0x99, 0xAA),
		2: info(busUSB, 0x02, 0x00, 0x00, 0x00, 0x00, 0x01),
		// Still starting: no address read from the controller yet.
		3: info(busUSB),
	}

	reader := Sysfs{Root: root, Info: func(index int) ([]byte, error) {
		if b, ok := raw[index]; ok {
			return b, nil
		}

		return nil, errors.New("no such device")
	}}

	got := reader.Addresses(context.Background())
	want := map[string]string{
		"1-5":   "02:11:22:33:44:55",
		"1-2.3": "02:66:77:88:99:AA",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Addresses() = %v, want %v", got, want)
	}
}

func TestAddressesWithoutBluetooth(t *testing.T) {
	reader := Sysfs{Root: t.TempDir(), Info: func(int) ([]byte, error) {
		t.Fatal("Info called without any controller")
		return nil, nil
	}}

	if got := reader.Addresses(context.Background()); len(got) != 0 {
		t.Fatalf("Addresses() = %v, want none", got)
	}
}

func TestParseInfo(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
		ok   bool
	}{
		{"usb", info(busUSB, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x01), "AA:BB:CC:DD:EE:01", true},
		{"usb with device type bits", info(busUSB|0x10, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x02), "AA:BB:CC:DD:EE:02", true},
		{"uart", info(3, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x03), "", false},
		{"zero", info(busUSB), "", false},
		{"all ones", info(busUSB, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff), "", false},
		{"short", make([]byte, devInfoType), "", false},
	}

	for _, c := range cases {
		got, ok := parseInfo(c.raw)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: parseInfo() = %q, %v, want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

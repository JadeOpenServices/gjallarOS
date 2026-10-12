package usbtrust_test

import (
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

// The ZBook's Bluetooth part sits on usb2 port 7, and ODDC asks for the
// Bluetooth address check (usbIdentity anchor "bluetooth-address").
// SYNTHETIC: the observed part and both addresses are made up.
const (
	bluetoothRole  = "hardware.network.bluetooth.primary"
	storedAddress  = "02:11:22:33:44:55"
	swappedAddress = "02:66:77:88:99:AA"
)

func bluetoothPart(address string) usbtrust.ObservedDevice {
	device := onPort("1-7", "8087:0a2b", "usb2", 7, "")
	device.Identity.BluetoothAddress = address

	return device
}

func bluetoothTrust(address string) *usbtrust.Document {
	stored := bluetoothPart(address).Identity

	return &usbtrust.Document{
		Schema:    usbtrust.SchemaVersion,
		MachineID: "machine-test",
		ODDCModel: "model/hp/zbook-x2-g4",
		Revision:  1,
		Devices:   []usbtrust.Device{storedPart(bluetoothRole, stored)},
	}
}

func TestAuditPortBluetoothAddress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stored   string
		observed string
		state    usbtrust.AuditState
		code     usbtrust.AuditCode
		anchor   string
	}{
		{"same address", storedAddress, storedAddress, usbtrust.AuditPass, usbtrust.CodeInternalMatch, ""},
		{"address changed", storedAddress, swappedAddress, usbtrust.AuditBlock, usbtrust.CodeInternalChanged, usbtrust.AnchorBluetoothAddress},
		{"not read yet", storedAddress, "", usbtrust.AuditPass, usbtrust.CodeInternalMatch, ""},
		{"not stored yet", "", swappedAddress, usbtrust.AuditPass, usbtrust.CodeInternalMatch, ""},
	} {
		result := audit(t, usbtrust.AuditInput{
			Expected: zbookExpected(t),
			Trusted:  bluetoothTrust(tc.stored),
			Observed: []usbtrust.ObservedDevice{bluetoothPart(tc.observed)},
		})

		finding := findingFor(t, result, "1-7")
		if finding.State != tc.state || finding.Code != tc.code || finding.Anchor != tc.anchor {
			t.Errorf("%s: got %s/%s/%q, want %s/%s/%q", tc.name, finding.State, finding.Code, finding.Anchor, tc.state, tc.code, tc.anchor)
		}
	}
}

func TestAuditPortBluetoothAddressNeedsODDC(t *testing.T) {
	// The front camera has no anchor in ODDC: an address on it is ignored.
	camera := onPort("1-9", "04f2:b5e7", "usb2", 9, "")
	stored := camera.Identity
	stored.BluetoothAddress = storedAddress
	camera.Identity.BluetoothAddress = swappedAddress

	result := audit(t, usbtrust.AuditInput{
		Expected: zbookExpected(t),
		Trusted: &usbtrust.Document{
			Schema:    usbtrust.SchemaVersion,
			MachineID: "machine-test",
			ODDCModel: "model/hp/zbook-x2-g4",
			Revision:  1,
			Devices:   []usbtrust.Device{storedPart("hardware.camera.front", stored)},
		},
		Observed: []usbtrust.ObservedDevice{camera},
	})

	if finding := findingFor(t, result, "1-9"); finding.Code != usbtrust.CodeInternalMatch {
		t.Fatalf("got %s, want %s", finding.Code, usbtrust.CodeInternalMatch)
	}
}

func TestLearnBluetoothAddresses(t *testing.T) {
	doc := bluetoothTrust("")
	input := usbtrust.AuditInput{
		Expected: zbookExpected(t),
		Trusted:  doc,
		Observed: []usbtrust.ObservedDevice{bluetoothPart("")},
	}

	changed, err := usbtrust.LearnBluetoothAddresses(input, doc)
	if err != nil || changed {
		t.Fatalf("not read yet: changed=%v err=%v, want no change", changed, err)
	}

	input.Observed = []usbtrust.ObservedDevice{bluetoothPart(storedAddress)}

	changed, err = usbtrust.LearnBluetoothAddresses(input, doc)
	if err != nil || !changed {
		t.Fatalf("first reading: changed=%v err=%v, want change", changed, err)
	}

	if got := doc.Devices[0].Identity.BluetoothAddress; got != storedAddress {
		t.Fatalf("stored %q, want %q", got, storedAddress)
	}

	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}

	// A stored address is never overwritten by learning; a different one is
	// a finding instead.
	input.Observed = []usbtrust.ObservedDevice{bluetoothPart(swappedAddress)}

	changed, err = usbtrust.LearnBluetoothAddresses(input, doc)
	if err != nil || changed {
		t.Fatalf("swapped part: changed=%v err=%v, want no change", changed, err)
	}

	if got := doc.Devices[0].Identity.BluetoothAddress; got != storedAddress {
		t.Fatalf("stored %q, want %q", got, storedAddress)
	}
}

func TestValidateBluetoothAddress(t *testing.T) {
	doc := bluetoothTrust("02:11:22:33:44")
	if err := doc.Validate(); err == nil {
		t.Fatal("Validate accepted a short Bluetooth address")
	}
}

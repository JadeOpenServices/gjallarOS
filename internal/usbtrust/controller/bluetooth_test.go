package controller

import (
	"context"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

// SYNTHETIC: the Bluetooth part and both addresses are made up.
func bluetoothFixture(t *testing.T) (*Controller, *sources) {
	t.Helper()
	c, s := fixture()
	c.Reader.Devices = portDevices{expected: []usbtrust.ExpectedDevice{{
		Role:        "hardware.network.bluetooth.primary",
		VIDPID:      "1234:5678",
		ConnectType: "hardwired",
		Required:    true,
		Port:        &usbtrust.ExpectedPort{Controller: "8086:9d2f", Hub: "usb2", RootPort: 2, Attachment: "internal", Serial: usbtrust.SerialPlaceholder, Anchor: usbtrust.AnchorBluetoothAddress},
	}}}
	c.Reader.RootHubs = oneHub{}
	return c, s
}

func reasonFor(t *testing.T, c *Controller) (usbtrust.Target, string) {
	t.Helper()
	policy, _, err := c.Policy(context.Background())
	if err != nil || len(policy) != 1 {
		t.Fatalf("policy: %+v %v", policy, err)
	}
	return policy[0].Target, policy[0].Reason
}

func TestReconcileLearnsAndChecksBluetoothAddress(t *testing.T) {
	c, s := bluetoothFixture(t)
	ctx := context.Background()

	// Stored before the address can be read.
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if s.doc == nil || len(s.doc.Devices) != 1 || s.doc.Devices[0].Identity.BluetoothAddress != "" {
		t.Fatalf("part not stored without address: %+v", s.doc)
	}

	// Learned the first time it is read, once.
	s.observed[0].Identity.BluetoothAddress = "02:11:22:33:44:55"
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.doc.Devices[0].Identity.BluetoothAddress; got != "02:11:22:33:44:55" {
		t.Fatalf("stored address %q", got)
	}
	revision := s.doc.Revision
	if err := c.Reconcile(ctx); err != nil || s.doc.Revision != revision {
		t.Fatalf("second reconcile wrote again: %v", err)
	}

	// A swapped part on the same port reports another address.
	s.observed = []usbtrust.ObservedDevice{s.observed[0]}
	s.observed[0].RuntimeID = "13"
	s.observed[0].Identity.BluetoothAddress = "02:66:77:88:99:AA"
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if target, reason := reasonFor(t, c); target != usbtrust.TargetBlock || reason != string(usbtrust.CodeInternalChanged) {
		t.Fatalf("swapped part: %s/%s", target, reason)
	}
	if got := s.doc.Devices[0].Identity.BluetoothAddress; got != "02:11:22:33:44:55" {
		t.Fatalf("learning overwrote the stored address with %q", got)
	}

	// Blocking unbinds the driver and the address can no longer be read;
	// the part must stay blocked.
	s.observed[0].Identity.BluetoothAddress = ""
	if target, reason := reasonFor(t, c); target != usbtrust.TargetBlock || reason != string(usbtrust.CodeInternalChanged) {
		t.Fatalf("blocked part allowed again: %s/%s", target, reason)
	}

	// Unplugged: the reading is forgotten, a new connection starts unchecked.
	s.observed = nil
	if _, _, err := c.Policy(ctx); err != nil {
		t.Fatal(err)
	}
	if len(c.bluetooth) != 0 {
		t.Fatalf("readings kept for gone parts: %v", c.bluetooth)
	}
}

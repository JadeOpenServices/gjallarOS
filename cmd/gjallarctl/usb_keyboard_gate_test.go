package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
)

// The shapes removable-media.nix writes for the ZBook x2 G4 and the FW13
// (tests/nix/usbtrust-oddc-devices.nix checks the Nix side).
const (
	zbookGateJSON = `{"earlyGate":false,"enforce":true,"keyboard":{"attachment":"detachable","deviceId":"1fc9:0088","name":"Keyboard"},"reason":"keyboard-on-usb","tier":"usb-keyboard"}`
	fw13GateJSON  = `{"earlyGate":false,"enforce":false,"keyboard":null,"reason":"no-keyboard-facts","tier":"none"}`
)

func writeGate(t *testing.T, text string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usb-keyboard-gate.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	old := usbKeyboardGatePath
	usbKeyboardGatePath = path
	t.Cleanup(func() { usbKeyboardGatePath = old })
}

func TestUSBKeyboardGateMissing(t *testing.T) {
	writeGate(t, "")
	usbKeyboardGatePath += ".absent"
	gate, err := readUSBKeyboardGate()
	if gate != nil || err != nil {
		t.Fatalf("USB trust off must read as no gate: %v, %v", gate, err)
	}
	if gjallarOSFindings(gate) != nil {
		t.Fatal("no gate file must not raise a note")
	}
}

func TestUSBKeyboardGateBroken(t *testing.T) {
	writeGate(t, "{")
	if _, err := readUSBKeyboardGate(); err == nil {
		t.Fatal("a broken gate file must be an error")
	}
}

func TestUSBKeyboardGateZBook(t *testing.T) {
	writeGate(t, zbookGateJSON)
	gate, err := readUSBKeyboardGate()
	if err != nil {
		t.Fatal(err)
	}
	if gate.Tier != "usb-keyboard" || gate.Keyboard == nil || gate.Keyboard.DeviceID != "1fc9:0088" {
		t.Fatalf("got %+v", gate)
	}
	// Checkpoint 2: the tier is known but stage 1 does not gate yet.
	findings := gjallarOSFindings(gate)
	if len(findings) != 1 || findings[0].ID != "gjallaros/usb-early-boot" || findings[0].Summary != gate.Summary {
		t.Fatalf("enforcing without an early gate must raise one note, got %+v", findings)
	}
	gate.EarlyGate = true
	if gjallarOSFindings(gate) != nil {
		t.Fatal("an early gate must not raise the note")
	}
	if strings.Contains(usbKeyboardGateSummary(gate), "every USB port") {
		t.Fatal("the gated text must not say every port is open")
	}
}

func TestUSBKeyboardGateFW13(t *testing.T) {
	writeGate(t, fw13GateJSON)
	gate, err := readUSBKeyboardGate()
	if err != nil {
		t.Fatal(err)
	}
	if gate.Tier != "none" || gate.Keyboard != nil || gate.Summary != "USB blocking is off." {
		t.Fatalf("got %+v", gate)
	}
	if gjallarOSFindings(gate) != nil {
		t.Fatal("blocking off must not raise the note")
	}
	gate.Enforce = true
	if len(gjallarOSFindings(gate)) != 1 {
		t.Fatal("blocking on without facts must raise the note")
	}
}

func TestUSBEarlyBootNoteSeenOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeGate(t, zbookGateJSON)
	gate, err := readUSBKeyboardGate()
	if err != nil {
		t.Fatal(err)
	}
	current := append(gjallarOSFindings(gate), zbookFindings(t)...)
	seen, _ := readSeenFindings()
	if fresh := unseenFindings(current, seen); len(fresh) != 5 {
		t.Fatalf("first login: got %d unseen, want 5", len(fresh))
	}
	if err := writeSeenFindings(current); err != nil {
		t.Fatal(err)
	}
	seen, _ = readSeenFindings()
	if fresh := unseenFindings(current, seen); len(fresh) != 0 {
		t.Fatalf("second login: got %v", fresh)
	}
}

func TestSeenODDCFindingsSurviveODDCError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeGate(t, zbookGateJSON)
	gate, _ := readUSBKeyboardGate()
	own := gjallarOSFindings(gate)
	odd := zbookFindings(t)
	if err := writeSeenFindings(append(own, odd...)); err != nil {
		t.Fatal(err)
	}
	// ODDC fails on the next run: its entries must stay seen.
	seen, _ := readSeenFindings()
	keep := append(own, seenODDCFindings(seen)...)
	if len(keep) != 1+len(odd) {
		t.Fatalf("keep = %d entries, want %d", len(keep), 1+len(odd))
	}
	if err := writeSeenFindings(keep); err != nil {
		t.Fatal(err)
	}
	seen, _ = readSeenFindings()
	if fresh := unseenFindings(odd, seen); len(fresh) != 0 {
		t.Fatalf("ODDC notes came back after an ODDC error: %v", fresh)
	}
}

func TestUSBStatusKeepsBrokerFields(t *testing.T) {
	writeGate(t, zbookGateJSON)
	gate, _ := readUSBKeyboardGate()
	out, err := json.Marshal(struct {
		broker.Response
		KeyboardGate *usbKeyboardGate `json:"keyboardGate,omitempty"`
	}{broker.Response{OK: true, Revision: 17}, gate})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ok", "enforcing", "revision", "keyboardGate"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("status lost %q: %s", key, out)
		}
	}
}

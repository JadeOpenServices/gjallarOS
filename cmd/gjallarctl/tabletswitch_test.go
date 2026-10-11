package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func inputEvent(kind, code uint16, value int32) []byte {
	record := make([]byte, inputEventSize)
	binary.NativeEndian.PutUint16(record[16:], kind)
	binary.NativeEndian.PutUint16(record[18:], code)
	binary.NativeEndian.PutUint32(record[20:], uint32(value))
	return record
}

func TestSwitchEvent(t *testing.T) {
	cases := []struct {
		name                    string
		record                  []byte
		on, changed, wantResync bool
	}{
		{"tablet", inputEvent(evSw, swTabletMode, 1), true, true, false},
		{"laptop", inputEvent(evSw, swTabletMode, 0), false, true, false},
		{"lid switch", inputEvent(evSw, 0x00, 1), false, false, false},
		{"report", inputEvent(evSyn, 0, 0), false, false, false},
		{"dropped", inputEvent(evSyn, synDropped, 0), false, false, true},
	}
	for _, c := range cases {
		on, changed, resync := switchEvent(c.record)
		if on != c.on || changed != c.changed || resync != c.wantResync {
			t.Errorf("%s: got %v,%v,%v want %v,%v,%v", c.name, on, changed, resync, c.on, c.changed, c.wantResync)
		}
	}
}

func TestFindSwitchDevice(t *testing.T) {
	sys := t.TempDir()
	for event, name := range map[string]string{
		"event3": "Intel HID switches",
		"event7": "ODDC Tablet Mode Switch",
	} {
		dir := filepath.Join(sys, "class/input", event, "device")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "name"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := findSwitchDevice(sys, "/dev/input", "ODDC Tablet Mode Switch"); got != "/dev/input/event7" {
		t.Fatalf("got %q", got)
	}
	if got := findSwitchDevice(sys, "/dev/input", "ODDC Tablet Mode"); got != "" {
		t.Fatalf("partial name matched %q", got)
	}
}

func TestRotationGateWithoutSwitch(t *testing.T) {
	g := newRotationGate(false, false)
	if a := g.orientation("left-up"); a != rotationApply {
		t.Fatalf("got %v", a)
	}
}

func TestRotationGateFollowsSwitch(t *testing.T) {
	g := newRotationGate(true, false)
	if a := g.orientation("left-up"); a != rotationNone {
		t.Fatalf("before the first switch state: %v", a)
	}
	if a := g.switchState(tabletSwitchState{present: true, on: false}); a != rotationNone {
		t.Fatalf("laptop mode: %v", a)
	}
	if a := g.orientation("right-up"); a != rotationNone {
		t.Fatalf("sensor in laptop mode: %v", a)
	}
	// Folding into tablet mode turns to where the sensor already points.
	if a := g.switchState(tabletSwitchState{present: true, on: true}); a != rotationApply || g.current != "right-up" {
		t.Fatalf("tablet mode: %v %q", a, g.current)
	}
	g.applied()
	if a := g.orientation("bottom-up"); a != rotationApply {
		t.Fatalf("sensor in tablet mode: %v", a)
	}
	g.applied()
	// A config reload in tablet mode turns the picture again.
	if a := g.reloaded(); a != rotationApply {
		t.Fatalf("reload in tablet mode: %v", a)
	}
	g.applied()
	if a := g.switchState(tabletSwitchState{present: true, on: false}); a != rotationRestore {
		t.Fatalf("back to laptop mode: %v", a)
	}
	if a := g.reloaded(); a != rotationNone {
		t.Fatalf("reload in laptop mode: %v", a)
	}
	if a := g.switchState(tabletSwitchState{present: true, on: false}); a != rotationNone {
		t.Fatalf("laptop mode again: %v", a)
	}
}

func TestRotationGateMissingSwitch(t *testing.T) {
	// Not required and never seen: a convertible without the ODDC switch
	// rotates as before.
	g := newRotationGate(true, false)
	g.orientation("left-up")
	if a := g.switchState(tabletSwitchState{}); a != rotationApply {
		t.Fatalf("no switch: %v", a)
	}

	// Required: missing means laptop mode.
	g = newRotationGate(true, true)
	g.orientation("left-up")
	if a := g.switchState(tabletSwitchState{}); a != rotationNone {
		t.Fatalf("required, missing: %v", a)
	}

	// Seen once, then gone (the ODDC service restarting): laptop mode.
	g = newRotationGate(true, false)
	g.orientation("left-up")
	if a := g.switchState(tabletSwitchState{present: true, on: true}); a != rotationApply {
		t.Fatalf("tablet: %v", a)
	}
	g.applied()
	if a := g.switchState(tabletSwitchState{}); a != rotationRestore {
		t.Fatalf("switch gone: %v", a)
	}
}

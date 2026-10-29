package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// usbKeyboardGatePath is written at build time from the ODDC keyboard facts
// for this model, see system/services/removable-media.nix (Plane 194).
var usbKeyboardGatePath = "/etc/gjallar/usb-keyboard-gate.json"

// usbKeyboardGate says whether USB is gated before the broker starts. Tier
// "usb-keyboard" means the only keyboard sits on a known USB port; "none"
// means there is no early gate, Reason says why.
type usbKeyboardGate struct {
	Tier      string `json:"tier"`
	Reason    string `json:"reason"`
	Enforce   bool   `json:"enforce"`
	EarlyGate bool   `json:"earlyGate"`
	Keyboard  *struct {
		DeviceID   string `json:"deviceId"`
		Attachment string `json:"attachment"`
		Name       string `json:"name"`
	} `json:"keyboard"`
	// Summary is filled in by gjallarctl, not read from the file.
	Summary string `json:"summary,omitempty"`
}

// readUSBKeyboardGate returns nil without an error when USB trust is off and
// the file is not built.
func readUSBKeyboardGate() (*usbKeyboardGate, error) {
	data, err := os.ReadFile(usbKeyboardGatePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var gate usbKeyboardGate
	if err := json.Unmarshal(data, &gate); err != nil {
		return nil, fmt.Errorf("%s: %w", usbKeyboardGatePath, err)
	}
	gate.Summary = usbKeyboardGateSummary(&gate)
	return &gate, nil
}

// usbEarlyBootOpen is true while USB blocking is on but nothing gates USB
// before the broker starts.
func usbEarlyBootOpen(gate *usbKeyboardGate) bool {
	return gate != nil && gate.Enforce && !gate.EarlyGate
}

func usbKeyboardGateSummary(gate *usbKeyboardGate) string {
	switch {
	case !gate.Enforce:
		return "USB blocking is off."
	case gate.EarlyGate:
		return "During boot only the built-in keyboard's USB port takes devices. It takes any device that reports as that keyboard."
	default:
		return "USB protection starts once the system is up. Until then, during boot and at the disk unlock screen, every USB port takes any device. Keep this device in sight while it starts."
	}
}

// gjallarOSFindings are the security notes GjallarOS owns itself, next to
// the ODDC ones. IDs start with "gjallaros/" so they never clash with ODDC.
func gjallarOSFindings(gate *usbKeyboardGate) []securityFinding {
	if !usbEarlyBootOpen(gate) {
		return nil
	}
	return []securityFinding{{
		ID:       "gjallaros/usb-early-boot",
		Name:     "USB is open during boot",
		Severity: "medium",
		Status:   "open",
		// Change the date when the text changes, so the note shows again.
		Modified: "2026-10-28",
		Summary:  usbKeyboardGateSummary(gate),
	}}
}

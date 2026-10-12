package oddcsource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

// Devices is the parsed output of `oddc devices --json`.
type Devices struct {
	Model    string
	Expected []usbtrust.ExpectedDevice
}

type devicesDocument struct {
	Model   string         `json:"model"`
	Devices []deviceRecord `json:"devices"`
}

type deviceRecord struct {
	Path        string          `json:"path"`
	DeviceID    string          `json:"deviceId"`
	Supported   *bool           `json:"supported"`
	Proven      string          `json:"proven"`
	USBPort     *usbPortRecord  `json:"usbPort"`
	USBIdentity *usbIdentityRec `json:"usbIdentity"`
}

type usbPortRecord struct {
	DeviceID    string `json:"deviceId"`
	Controller  string `json:"controller"`
	Hub         string `json:"hub"`
	Port        int    `json:"port"`
	ConnectType string `json:"connectType"`
	Attachment  string `json:"attachment"`
	Supported   *bool  `json:"supported"`
}

type usbIdentityRec struct {
	Serial string `json:"serial"`
	Anchor string `json:"anchor"`
}

var pciID = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}$`)

// ParseDevices reads `oddc devices --json`. Only parts with a usbPort
// become expectations: ODDC lists a usbPort for exactly the parts that
// are wired to a fixed port, and nothing else decides "built-in".
//
// Values this parser does not know fail the whole parse. A new ODDC
// release that adds one shows up when the pin is updated, not as a
// silent allow on a user's machine.
func ParseDevices(data []byte) (Devices, error) {
	var document devicesDocument

	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return Devices{}, fmt.Errorf("parse ODDC devices: %w", err)
	}

	model := strings.TrimSpace(document.Model)
	if model == "" {
		return Devices{}, fmt.Errorf("ODDC devices output has no model")
	}

	result := Devices{Model: model}
	seen := make(map[string]struct{})

	for _, record := range document.Devices {
		if record.USBPort == nil {
			continue
		}

		expected, err := expectedFromRecord(record)
		if err != nil {
			return Devices{}, err
		}

		if _, exists := seen[expected.Role]; exists {
			return Devices{}, fmt.Errorf(
				"ODDC lists part %q twice",
				expected.Role,
			)
		}
		seen[expected.Role] = struct{}{}

		result.Expected = append(result.Expected, expected)
	}

	sort.Slice(result.Expected, func(i, j int) bool {
		return result.Expected[i].Role < result.Expected[j].Role
	})

	return result, nil
}

func expectedFromRecord(record deviceRecord) (usbtrust.ExpectedDevice, error) {
	role := strings.TrimSpace(record.Path)
	if role == "" {
		return usbtrust.ExpectedDevice{}, fmt.Errorf(
			"ODDC part with usbPort has no path",
		)
	}

	fail := func(format string, args ...any) (usbtrust.ExpectedDevice, error) {
		return usbtrust.ExpectedDevice{}, fmt.Errorf(
			"ODDC part %q: %s",
			role,
			fmt.Sprintf(format, args...),
		)
	}

	port := record.USBPort

	deviceID := normalizeID(record.DeviceID)
	if !pciID.MatchString(deviceID) {
		return fail("deviceId %q is not vendor:product", record.DeviceID)
	}

	if normalizeID(port.DeviceID) != deviceID {
		return fail(
			"usbPort deviceId %q differs from part deviceId %q",
			port.DeviceID,
			record.DeviceID,
		)
	}

	controller := normalizeID(port.Controller)
	if !pciID.MatchString(controller) {
		return fail("usbPort controller %q is not a PCI vendor:device", port.Controller)
	}

	hub := strings.TrimSpace(port.Hub)
	switch hub {
	case "usb2", "usb3":
	default:
		return fail("usbPort hub %q is not usb2 or usb3", port.Hub)
	}

	if port.Port <= 0 {
		return fail("usbPort port %d is not a port number", port.Port)
	}

	connectType := strings.TrimSpace(port.ConnectType)
	if connectType == "" {
		return fail("usbPort has no connectType")
	}

	attachment := strings.TrimSpace(port.Attachment)
	switch attachment {
	case "internal", "detachable":
	default:
		return fail("usbPort attachment %q is not internal or detachable", port.Attachment)
	}

	if record.Supported == nil || port.Supported == nil {
		return fail("supported is missing")
	}

	if strings.TrimSpace(record.Proven) == "" {
		return fail("proven is missing")
	}

	expectedPort := usbtrust.ExpectedPort{
		Controller: controller,
		Hub:        hub,
		RootPort:   port.Port,
		Attachment: attachment,
		Debug: !*record.Supported ||
			!*port.Supported ||
			record.Proven != "pass",
	}

	if identity := record.USBIdentity; identity != nil {
		switch mode := usbtrust.SerialMode(identity.Serial); mode {
		case "":
		case usbtrust.SerialPerUnit, usbtrust.SerialPlaceholder, usbtrust.SerialNone:
			expectedPort.Serial = mode
		default:
			return fail("usbIdentity serial %q is unknown", identity.Serial)
		}

		switch identity.Anchor {
		case "":
		case usbtrust.AnchorBluetoothAddress:
			expectedPort.Anchor = identity.Anchor
		default:
			return fail("usbIdentity anchor %q is unknown", identity.Anchor)
		}
	}

	return usbtrust.ExpectedDevice{
		Role:        role,
		VIDPID:      deviceID,
		ConnectType: connectType,
		Required:    attachment == "internal",
		Port:        &expectedPort,
	}, nil
}

func normalizeID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

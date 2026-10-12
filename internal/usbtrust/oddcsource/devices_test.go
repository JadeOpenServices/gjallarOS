package oddcsource

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

// testdata/zbook-x2-g4-devices.json is the real output of
// `oddc devices --json --device model/hp/zbook-x2-g4` from ODDC release 2
// (a24366ae), run in the e2e-fw13 VM against the pinned catalog.
func zbookDevices(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile("testdata/zbook-x2-g4-devices.json")
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestParseDevicesZBook(t *testing.T) {
	devices, err := ParseDevices(zbookDevices(t))
	if err != nil {
		t.Fatal(err)
	}

	if devices.Model != "model/hp/zbook-x2-g4" {
		t.Fatalf("model = %q", devices.Model)
	}

	type want struct {
		vidpid     string
		hub        string
		port       int
		attachment string
		required   bool
		debug      bool
		serial     usbtrust.SerialMode
		anchor     string
	}

	wanted := map[string]want{
		"hardware.camera.front":                 {"04f2:b5e7", "usb2", 9, "internal", true, false, usbtrust.SerialPlaceholder, ""},
		"hardware.camera.rear":                  {"0bda:5666", "usb3", 2, "internal", true, false, usbtrust.SerialPlaceholder, ""},
		"hardware.input.keyboard.primary":       {"1fc9:0088", "usb2", 4, "detachable", false, false, usbtrust.SerialPlaceholder, ""},
		"hardware.input.quickKeys.primary":      {"03f0:0a56", "usb2", 2, "internal", true, false, usbtrust.SerialNone, ""},
		"hardware.network.bluetooth.primary":    {"8087:0a2b", "usb2", 7, "internal", true, false, usbtrust.SerialNone, usbtrust.AnchorBluetoothAddress},
		"hardware.security.fingerprint.primary": {"138a:003f", "usb2", 8, "internal", true, true, usbtrust.SerialPerUnit, ""},
	}

	if len(devices.Expected) != len(wanted) {
		t.Fatalf("got %d expectations, want %d: %+v", len(devices.Expected), len(wanted), devices.Expected)
	}

	for _, expected := range devices.Expected {
		w, ok := wanted[expected.Role]
		if !ok {
			t.Fatalf("unexpected role %q", expected.Role)
		}

		port := expected.Port
		if port == nil {
			t.Fatalf("%s: no port", expected.Role)
		}

		if expected.VIDPID != w.vidpid ||
			expected.ConnectType != "hardwired" ||
			expected.Required != w.required ||
			port.Controller != "8086:9d2f" ||
			port.Hub != w.hub ||
			port.RootPort != w.port ||
			port.Attachment != w.attachment ||
			port.Debug != w.debug ||
			port.Serial != w.serial ||
			port.Anchor != w.anchor {
			t.Fatalf("%s: got %+v port %+v, want %+v", expected.Role, expected, *port, w)
		}
	}
}

// mutateZBook changes one part of the real ZBook output so each check
// below fails on exactly one field.
func mutateZBook(t *testing.T, role string, change func(part map[string]any)) []byte {
	t.Helper()

	var document map[string]any
	if err := json.Unmarshal(zbookDevices(t), &document); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, raw := range document["devices"].([]any) {
		part := raw.(map[string]any)
		if part["path"] == role {
			change(part)
			found = true
		}
	}

	if !found {
		t.Fatalf("role %q not in fixture", role)
	}

	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestParseDevicesRejectsUnknownValues(t *testing.T) {
	const camera = "hardware.camera.front"

	port := func(part map[string]any) map[string]any {
		return part["usbPort"].(map[string]any)
	}

	cases := map[string]func(part map[string]any){
		"hub":         func(p map[string]any) { port(p)["hub"] = "usb4" },
		"port zero":   func(p map[string]any) { port(p)["port"] = 0 },
		"controller":  func(p map[string]any) { port(p)["controller"] = "xhci" },
		"deviceId":    func(p map[string]any) { port(p)["deviceId"] = "04f2:0000" },
		"attachment":  func(p map[string]any) { port(p)["attachment"] = "modular" },
		"connectType": func(p map[string]any) { delete(port(p), "connectType") },
		"supported":   func(p map[string]any) { delete(p, "supported") },
		"proven":      func(p map[string]any) { delete(p, "proven") },
		"serial":      func(p map[string]any) { p["usbIdentity"] = map[string]any{"serial": "sometimes"} },
		"anchor":      func(p map[string]any) { p["usbIdentity"] = map[string]any{"anchor": "wifi-address"} },
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDevices(mutateZBook(t, camera, change))
			if err == nil || !strings.Contains(err.Error(), camera) {
				t.Fatalf("want error naming %s, got %v", camera, err)
			}
		})
	}
}

func TestParseDevicesNotProvenIsDebug(t *testing.T) {
	data := mutateZBook(t, "hardware.camera.front", func(p map[string]any) {
		p["proven"] = "untested"
	})

	devices, err := ParseDevices(data)
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range devices.Expected {
		if expected.Role == "hardware.camera.front" && !expected.Port.Debug {
			t.Fatal("untested part must be in the debug class")
		}
	}
}

func TestParseDevicesNeedsModel(t *testing.T) {
	if _, err := ParseDevices([]byte(`{"devices": []}`)); err == nil {
		t.Fatal("want error for missing model")
	}
}

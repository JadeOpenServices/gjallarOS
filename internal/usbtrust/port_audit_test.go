package usbtrust_test

import (
	"os"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/oddcsource"
)

// The expectations are the real ODDC release 2 output for the ZBook x2 G4.
// The observed devices are SYNTHETIC: no ZBook capture exists yet, so they
// are built from the ports and IDs ODDC lists.
func zbookExpected(t *testing.T) []usbtrust.ExpectedDevice {
	t.Helper()

	data, err := os.ReadFile("oddcsource/testdata/zbook-x2-g4-devices.json")
	if err != nil {
		t.Fatal(err)
	}

	devices, err := oddcsource.ParseDevices(data)
	if err != nil {
		t.Fatal(err)
	}

	return devices.Expected
}

const zbookController = "8086:9d2f"

// runtimeFor gives each kernel port path a numeric usbguard-style id.
func runtimeFor(path string) string {
	return strings.ReplaceAll(path, "-", "0")
}

func onPort(path, vidpid, hub string, port int, serial string) usbtrust.ObservedDevice {
	return usbtrust.ObservedDevice{
		RuntimeID: runtimeFor(path),
		Identity: usbtrust.Identity{
			VIDPID:      vidpid,
			Serial:      serial,
			Hash:        "synthetic-" + vidpid,
			Port:        path,
			ConnectType: "unknown",
		},
		Wiring: &usbtrust.ObservedPort{Controller: zbookController, Hub: hub, RootPort: port},
	}
}

func findingFor(t *testing.T, result usbtrust.AuditResult, key string) usbtrust.Finding {
	t.Helper()

	var found []usbtrust.Finding
	for _, finding := range result.Findings {
		if finding.RuntimeID == runtimeFor(key) || (finding.RuntimeID == "" && finding.Role == key) {
			found = append(found, finding)
		}
	}

	if len(found) != 1 {
		t.Fatalf("%s: want one finding, got %+v", key, found)
	}

	return found[0]
}

func audit(t *testing.T, input usbtrust.AuditInput) usbtrust.AuditResult {
	t.Helper()

	result, err := usbtrust.Audit(input)
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func TestAuditPortFirstBoot(t *testing.T) {
	result := audit(t, usbtrust.AuditInput{
		Expected: zbookExpected(t),
		Observed: []usbtrust.ObservedDevice{
			onPort("1-9", "04f2:b5e7", "usb2", 9, "0001"),
			onPort("1-8", "138a:003f", "usb2", 8, "fp-unit-a"),
			onPort("1-7", "8087:0a2b", "usb2", 7, ""),
		},
	})

	want := map[string]struct {
		state usbtrust.AuditState
		code  usbtrust.AuditCode
	}{
		"1-9":                              {usbtrust.AuditPass, usbtrust.CodeInternalPortMatch},
		"1-8":                              {usbtrust.AuditBlock, usbtrust.CodeInternalDebug},
		"1-7":                              {usbtrust.AuditPass, usbtrust.CodeInternalPortMatch},
		"hardware.camera.rear":             {usbtrust.AuditWarn, usbtrust.CodeInternalMissing},
		"hardware.input.quickKeys.primary": {usbtrust.AuditWarn, usbtrust.CodeInternalMissing},
	}

	for key, w := range want {
		finding := findingFor(t, result, key)
		if finding.State != w.state || finding.Code != w.code {
			t.Fatalf("%s: got %s/%s, want %s/%s", key, finding.State, finding.Code, w.state, w.code)
		}
	}

	// The detachable keyboard is absent: no warning.
	if len(result.Findings) != len(want) {
		t.Fatalf("got %d findings, want %d: %+v", len(result.Findings), len(want), result.Findings)
	}
}

func TestAuditPortWrongPlace(t *testing.T) {
	result := audit(t, usbtrust.AuditInput{
		Expected: zbookExpected(t),
		Observed: []usbtrust.ObservedDevice{
			// The camera's IDs on an outside port: a copy, not the camera.
			onPort("1-1", "04f2:b5e7", "usb2", 1, "0001"),
			// The camera's port with another device on it.
			onPort("1-9", "1234:5678", "usb2", 9, ""),
			// The camera's port number on the other hub.
			onPort("2-9", "04f2:b5e7", "usb3", 9, "0001"),
		},
	})

	for key, code := range map[string]usbtrust.AuditCode{
		"1-1": usbtrust.CodeUnknownExternal,
		"1-9": usbtrust.CodeUnexpectedInternal,
		"2-9": usbtrust.CodeUnknownExternal,
	} {
		finding := findingFor(t, result, key)
		if finding.State != usbtrust.AuditBlock || finding.Code != code {
			t.Fatalf("%s: got %s/%s, want BLOCK/%s", key, finding.State, finding.Code, code)
		}
	}
}

func TestAuditPortKernelOrSharedControllerDisagrees(t *testing.T) {
	hotplug := onPort("1-9", "04f2:b5e7", "usb2", 9, "0001")
	hotplug.Identity.ConnectType = "hotplug"

	shared := onPort("1-7", "8087:0a2b", "usb2", 7, "")
	shared.Wiring.Shared = true

	result := audit(t, usbtrust.AuditInput{
		Expected: zbookExpected(t),
		Observed: []usbtrust.ObservedDevice{hotplug, shared},
	})

	for _, key := range []string{"1-9", "1-7"} {
		finding := findingFor(t, result, key)
		if finding.State != usbtrust.AuditReview || finding.Code != usbtrust.CodeInternalAmbiguous {
			t.Fatalf("%s: got %s/%s, want REVIEW/internal-ambiguous", key, finding.State, finding.Code)
		}
	}
}

func storedPart(role string, identity usbtrust.Identity) usbtrust.Device {
	return usbtrust.Device{
		ID:             "device:" + role,
		Role:           role,
		Class:          usbtrust.ClassInternal,
		ExpectedByODDC: true,
		Strength:       usbtrust.StrengthSerialDescriptorTopology,
		Identity:       identity,
		FirstAccepted:  "2026-10-12T00:00:00Z",
		LastAccepted:   "2026-10-12T00:00:00Z",
	}
}

func TestAuditPortPlaceholderSerialIgnored(t *testing.T) {
	camera := onPort("1-9", "04f2:b5e7", "usb2", 9, "0001")
	stored := camera.Identity
	stored.Serial = "0002"

	changed := onPort("1-9", "04f2:b5e7", "usb2", 9, "0001")
	changed.Identity.Hash = "synthetic-other-descriptor"

	trusted := &usbtrust.Document{
		Schema:    usbtrust.SchemaVersion,
		MachineID: "machine-test",
		ODDCModel: "model/hp/zbook-x2-g4",
		Revision:  1,
		Devices:   []usbtrust.Device{storedPart("hardware.camera.front", stored)},
	}

	for _, tc := range []struct {
		device usbtrust.ObservedDevice
		state  usbtrust.AuditState
		code   usbtrust.AuditCode
	}{
		{camera, usbtrust.AuditPass, usbtrust.CodeInternalMatch},
		{changed, usbtrust.AuditBlock, usbtrust.CodeInternalChanged},
	} {
		result := audit(t, usbtrust.AuditInput{
			Expected: zbookExpected(t),
			Trusted:  trusted,
			Observed: []usbtrust.ObservedDevice{tc.device},
		})

		finding := findingFor(t, result, "1-9")
		if finding.State != tc.state || finding.Code != tc.code {
			t.Fatalf("got %s/%s, want %s/%s", finding.State, finding.Code, tc.state, tc.code)
		}
	}
}

func TestAuditPortPerUnitSerialCounts(t *testing.T) {
	// SYNTHETIC: a supported part with a per-unit serial. The ZBook has
	// none yet; its only per-unit part is the debug-class fingerprint.
	expected := usbtrust.ExpectedDevice{
		Role:        "hardware.test.perUnit",
		VIDPID:      "1234:0001",
		ConnectType: "hardwired",
		Required:    true,
		Port: &usbtrust.ExpectedPort{
			Controller: zbookController,
			Hub:        "usb2",
			RootPort:   3,
			Attachment: "internal",
			Serial:     usbtrust.SerialPerUnit,
		},
	}

	observed := onPort("1-3", "1234:0001", "usb2", 3, "unit-b")
	stored := observed.Identity
	stored.Serial = "unit-a"

	result := audit(t, usbtrust.AuditInput{
		Expected: []usbtrust.ExpectedDevice{expected},
		Trusted: &usbtrust.Document{
			Schema:    usbtrust.SchemaVersion,
			MachineID: "machine-test",
			ODDCModel: "model/test",
			Revision:  1,
			Devices:   []usbtrust.Device{storedPart(expected.Role, stored)},
		},
		Observed: []usbtrust.ObservedDevice{observed},
	})

	if finding := findingFor(t, result, "1-3"); finding.Code != usbtrust.CodeInternalChanged {
		t.Fatalf("got %s, want internal-changed", finding.Code)
	}
}

func TestAuditPortRejectsSharedPort(t *testing.T) {
	expected := zbookExpected(t)
	clone := expected[0]
	clone.Role = "hardware.test.clone"
	clone.VIDPID = "1234:0002"

	if _, err := usbtrust.Audit(usbtrust.AuditInput{Expected: append(expected, clone)}); err == nil {
		t.Fatal("two expectations on one port were accepted")
	}
}

func TestDeriveDebugOffersOnlyKeepBlocked(t *testing.T) {
	input := usbtrust.AuditInput{
		Expected: zbookExpected(t),
		Observed: []usbtrust.ObservedDevice{onPort("1-8", "138a:003f", "usb2", 8, "fp-unit-a")},
	}

	var sessions usbtrust.Sessions
	if err := sessions.Observe(input.Observed); err != nil {
		t.Fatal(err)
	}

	decisions, err := sessions.Derive(input)
	if err != nil {
		t.Fatal(err)
	}

	d := decisions[0]
	if d.Target != usbtrust.TargetBlock ||
		len(d.Actions) != 1 ||
		d.Actions[0] != usbtrust.ActionKeepBlocked {
		t.Fatalf("got %+v", d)
	}
}

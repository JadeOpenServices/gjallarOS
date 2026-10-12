package readmodel

import (
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/oddcsource"
)

const zbookDevicesFile = "../oddcsource/testdata/zbook-x2-g4-devices.json"

type fixedResolved map[string]any

func (r fixedResolved) Resolved(context.Context) (map[string]any, error) { return r, nil }

type fixedDevices oddcsource.Devices

func (d fixedDevices) Devices(context.Context) (oddcsource.Devices, error) {
	return oddcsource.Devices(d), nil
}

func TestExpectedPrefersPortParts(t *testing.T) {
	reader := Reader{
		Resolved: fixedResolved{},
		Devices:  DevicesFile{Path: zbookDevicesFile, Model: "model/hp/zbook-x2-g4"},
	}

	expected, err := reader.Expected(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(expected) != 6 {
		t.Fatalf("got %d expectations, want the 6 ZBook usbPort parts", len(expected))
	}
	for _, device := range expected {
		if device.Port == nil {
			t.Fatalf("%s has no port", device.Role)
		}
	}
}

func TestExpectedFallsBackWithoutPortParts(t *testing.T) {
	reader := Reader{
		Resolved: fixedResolved{},
		Devices:  fixedDevices{Model: "model/test"},
	}

	expected, err := reader.Expected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) != 0 {
		t.Fatalf("got %+v from an empty resolved view", expected)
	}
}

func TestDevicesFileRefusesOtherModel(t *testing.T) {
	_, err := DevicesFile{Path: zbookDevicesFile, Model: "model/framework/laptop-13"}.Devices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "model/hp/zbook-x2-g4") {
		t.Fatalf("want model mismatch, got %v", err)
	}
}

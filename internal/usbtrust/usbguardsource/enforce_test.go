package usbguardsource

import (
	"context"
	"reflect"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

type enforcementRunner struct {
	lines string
	calls [][]string
}

func (r *enforcementRunner) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return []byte(r.lines), nil
}

func TestEnforcementRevalidatesIdentityAndNeverPersists(t *testing.T) {
	line := `12: block id 1234:5678 hash "synthetic" parent-hash "parent" via-port "1-2" with-interface 08:06:50`
	parsed, err := ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	r := &enforcementRunner{lines: line}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	plan := []usbtrust.Decision{{ObservedDevice: parsed.Device, Target: usbtrust.TargetAllow}}
	if err := s.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"list-devices"}, {"allow-device", "12"}}) {
		t.Fatal(r.calls)
	}
	r.calls = nil
	plan[0].Identity.Hash = "old-device"
	if err := s.Apply(context.Background(), plan); err == nil {
		t.Fatal("stale identity allowed")
	}
	if len(r.calls) != 1 {
		t.Fatal("stale plan reached mutation", r.calls)
	}
}

// The controller adds the Bluetooth address it read from the Bluetooth
// stack; USBGuard never reports it, so it must not fail the revalidation.
func TestEnforcementIgnoresBluetoothAddress(t *testing.T) {
	line := `7: block id 13d3:3571 hash "synthetic-radio" parent-hash "parent" via-port "1-5" with-interface { e0:01:01 e0:01:01 }`
	parsed, err := ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	r := &enforcementRunner{lines: line}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	plan := []usbtrust.Decision{{ObservedDevice: parsed.Device, Target: usbtrust.TargetAllow}}
	plan[0].Identity.BluetoothAddress = "00:00:5E:00:53:01"
	if err := s.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"list-devices"}, {"allow-device", "7"}}) {
		t.Fatal(r.calls)
	}
}

func TestRevocationsPrecedeAllows(t *testing.T) {
	r := &enforcementRunner{lines: "12: block id 1234:5678 hash \"a\"\n13: allow id 1234:9999 hash \"b\""}
	observed, err := ParseLines(r.lines)
	if err != nil {
		t.Fatal(err)
	}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	plan := []usbtrust.Decision{{ObservedDevice: observed[0].Device, Target: usbtrust.TargetAllow}, {ObservedDevice: observed[1].Device, Target: usbtrust.TargetBlock}}
	if err := s.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"list-devices"}, {"block-device", "13"}, {"allow-device", "12"}}) {
		t.Fatal(r.calls)
	}
}

func TestImplicitTargetIsRuntimeOnly(t *testing.T) {
	r := &enforcementRunner{}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	if err := s.SetImplicitTarget(context.Background(), usbtrust.TargetBlock); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImplicitTarget(context.Background(), usbtrust.Target("reject")); err == nil {
		t.Fatal("accepted unsupported target")
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"set-parameter", "ImplicitPolicyTarget", "block"}}) {
		t.Fatal(r.calls)
	}
}

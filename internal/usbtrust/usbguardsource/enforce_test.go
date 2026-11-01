package usbguardsource

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
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

// scriptedRunner answers list-devices with the next listing in turn and fails
// the device actions named in refuse.
type scriptedRunner struct {
	listings []string
	refuse   map[string]bool
	calls    [][]string
}

func (r *scriptedRunner) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if args[0] == "list-devices" {
		if len(r.listings) == 0 {
			return nil, errors.New("SYNTHETIC: no listing left")
		}
		out := r.listings[0]
		if len(r.listings) > 1 {
			r.listings = r.listings[1:]
		}
		return []byte(out), nil
	}
	if r.refuse[strings.Join(args, " ")] {
		return nil, errors.New("SYNTHETIC: usbguard refused the action")
	}
	return nil, nil
}

const (
	radioLine = `3: block id 13d3:3571 hash "synthetic-radio" parent-hash "parent" via-port "1-5" with-interface e0:01:01`
	stickLine = `9: block id 1234:5678 hash "synthetic-stick" parent-hash "parent" via-port "1-2" with-interface 08:06:50`
)

func planFor(t *testing.T, lines string, target usbtrust.Target) []usbtrust.Decision {
	t.Helper()
	observed, err := ParseLines(lines)
	if err != nil {
		t.Fatal(err)
	}
	plan := make([]usbtrust.Decision, 0, len(observed))
	for _, o := range observed {
		plan = append(plan, usbtrust.Decision{ObservedDevice: o.Device, Target: target})
	}
	return plan
}

// Plane 288: a stick pulled while the check runs must not fail the plan, or
// the daemon blocks every device and the Bluetooth radio drops.
func TestEnforcementSkipsDeviceUnpluggedBeforeApply(t *testing.T) {
	plan := planFor(t, radioLine+"\n"+stickLine, usbtrust.TargetAllow)
	r := &scriptedRunner{listings: []string{radioLine}}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	if err := s.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"list-devices"}, {"allow-device", "3"}}) {
		t.Fatal(r.calls)
	}
}

func TestEnforcementSkipsDeviceUnpluggedDuringAction(t *testing.T) {
	plan := planFor(t, radioLine+"\n"+stickLine, usbtrust.TargetAllow)
	r := &scriptedRunner{
		listings: []string{radioLine + "\n" + stickLine, radioLine},
		refuse:   map[string]bool{"allow-device 9": true},
	}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	if err := s.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	var allowed []string
	lists := 0
	for _, c := range r.calls {
		if c[0] == "list-devices" {
			lists++
		} else {
			allowed = append(allowed, strings.Join(c, " "))
		}
	}
	sort.Strings(allowed)
	if lists != 2 || !reflect.DeepEqual(allowed, []string{"allow-device 3", "allow-device 9"}) {
		t.Fatal(r.calls)
	}
}

// A refused action on a device that is still there stays an error, so the
// daemon keeps failing closed.
func TestEnforcementKeepsFailureForPresentDevice(t *testing.T) {
	plan := planFor(t, stickLine, usbtrust.TargetAllow)
	r := &scriptedRunner{
		listings: []string{stickLine},
		refuse:   map[string]bool{"allow-device 9": true},
	}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	if err := s.Apply(context.Background(), plan); err == nil {
		t.Fatal("refused allow on a present device was ignored")
	}
}

// If the second listing fails too, nothing proves the device left.
func TestEnforcementKeepsFailureWhenRecheckFails(t *testing.T) {
	plan := planFor(t, stickLine, usbtrust.TargetAllow)
	r := &scriptedRunner{
		listings: []string{stickLine},
		refuse:   map[string]bool{"allow-device 9": true},
	}
	s := LiveSource{Runner: failingRecheck{r}, Binary: "usbguard"}
	if err := s.Apply(context.Background(), plan); err == nil {
		t.Fatal("refused allow ignored without a fresh listing")
	}
}

type failingRecheck struct{ r *scriptedRunner }

func (f failingRecheck) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if args[0] == "list-devices" && len(f.r.calls) > 0 {
		f.r.calls = append(f.r.calls, append([]string(nil), args...))
		return nil, errors.New("SYNTHETIC: list-devices failed")
	}
	return f.r.Output(ctx, name, args...)
}

// A present device whose identity changed still stops the plan before any
// action, also when another device in the plan has gone.
func TestEnforcementStillRejectsChangedIdentityNextToGoneDevice(t *testing.T) {
	plan := planFor(t, radioLine+"\n"+stickLine, usbtrust.TargetAllow)
	for i := range plan {
		if plan[i].RuntimeID == "3" {
			plan[i].Identity.Hash = "other-radio"
		}
	}
	r := &scriptedRunner{listings: []string{radioLine}}
	s := LiveSource{Runner: r, Binary: "usbguard"}
	if err := s.Apply(context.Background(), plan); err == nil {
		t.Fatal("changed identity allowed")
	}
	if len(r.calls) != 1 {
		t.Fatal("stale plan reached mutation", r.calls)
	}
}

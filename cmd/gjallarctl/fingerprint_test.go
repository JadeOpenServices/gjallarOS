package main

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeFprintBus struct {
	activatable []string
	uid         uint32
	deviceErr   error
	fingers     []string
	fingersErr  error
	claimErr    error
	releaseErr  error
	calls       *[]string
}

func (f fakeFprintBus) note(call string) {
	if f.calls != nil {
		*f.calls = append(*f.calls, call)
	}
}

func (f fakeFprintBus) Activatable(context.Context) ([]string, error) {
	return f.activatable, nil
}

func (f fakeFprintBus) OwnerUID(context.Context, string) (uint32, error) {
	return f.uid, nil
}

func (f fakeFprintBus) DefaultDevice(context.Context) (dbus.ObjectPath, error) {
	return "/net/reactivated/Fprint/Device/0", f.deviceErr
}

func (f fakeFprintBus) EnrolledFingers(context.Context, dbus.ObjectPath) ([]string, error) {
	return f.fingers, f.fingersErr
}

func (f fakeFprintBus) Claim(context.Context, dbus.ObjectPath) error {
	f.note("claim")
	return f.claimErr
}

func (f fakeFprintBus) Release(context.Context, dbus.ObjectPath) error {
	f.note("release")
	return f.releaseErr
}

func TestProbeFingerprintStates(t *testing.T) {
	present := []string{"org.freedesktop.systemd1", fprintName}
	cases := []struct {
		name string
		bus  fakeFprintBus
		want fingerprintState
	}{
		{"enrolled", fakeFprintBus{activatable: present, fingers: []string{"right-index-finger"}}, fingerprintReady},
		{"no fprintd", fakeFprintBus{activatable: []string{"org.freedesktop.systemd1"}}, fingerprintNoService},
		{"no reader", fakeFprintBus{activatable: present,
			deviceErr: dbus.Error{Name: fprintName + ".Error.NoSuchDevice"}}, fingerprintNoReader},
		{"no prints error", fakeFprintBus{activatable: present,
			fingersErr: dbus.Error{Name: fprintName + ".Error.NoEnrolledPrints"}}, fingerprintNoPrints},
		{"no prints empty", fakeFprintBus{activatable: present}, fingerprintNoPrints},
		{"not root", fakeFprintBus{activatable: present, uid: 1000,
			fingers: []string{"right-index-finger"}}, fingerprintUntrusted},
		{"other error", fakeFprintBus{activatable: present,
			fingersErr: errors.New("timeout")}, fingerprintProbeFailed},
		// Plane 287: prints on record, but fprintd cannot open the reader.
		{"reader will not open", fakeFprintBus{activatable: present, fingers: []string{"right-index-finger"},
			claimErr: dbus.Error{Name: fprintName + ".Error.Internal"}}, fingerprintUnavailable},
		{"reader in use", fakeFprintBus{activatable: present, fingers: []string{"right-index-finger"},
			claimErr: dbus.Error{Name: fprintName + ".Error.AlreadyInUse"}}, fingerprintUnavailable},
		{"other session", fakeFprintBus{activatable: present, fingers: []string{"right-index-finger"},
			claimErr: dbus.Error{Name: fprintName + ".Error.PermissionDenied"}}, fingerprintUnavailable},
		{"release fails", fakeFprintBus{activatable: present, fingers: []string{"right-index-finger"},
			releaseErr: dbus.Error{Name: fprintName + ".Error.Internal"}}, fingerprintUnavailable},
	}
	for _, c := range cases {
		got := probeFingerprintOn(context.Background(), c.bus)
		if got.State != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got.State, c.want)
		}
		if c.want != fingerprintReady && got.Message() == "" {
			t.Errorf("%s: no message", c.name)
		}
	}
}

// The probe holds the reader only between Claim and Release, and never claims
// before it knows there is a print to scan.
func TestProbeFingerprintClaimsLast(t *testing.T) {
	present := []string{fprintName}
	var calls []string
	got := probeFingerprintOn(context.Background(), fakeFprintBus{activatable: present,
		fingers: []string{"right-index-finger"}, calls: &calls})
	if got.State != fingerprintReady || len(calls) != 2 || calls[0] != "claim" || calls[1] != "release" {
		t.Fatalf("state %d, calls %v", got.State, calls)
	}
	calls = nil
	probeFingerprintOn(context.Background(), fakeFprintBus{activatable: present, calls: &calls})
	if len(calls) != 0 {
		t.Fatalf("claimed without prints: %v", calls)
	}
}

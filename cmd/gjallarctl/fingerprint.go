package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
)

type fingerprintState int

const (
	fingerprintReady fingerprintState = iota
	fingerprintNoService
	fingerprintNoReader
	fingerprintNoPrints
	fingerprintUntrusted
	fingerprintProbeFailed
	fingerprintUnavailable
)

// fingerprintProbe tells whether the calling user can use the fingerprint
// phase, and if not, why. The fingerprint-only PAM service fails at once
// without a print, and sudo counts each failure as a wrong password: three
// "Sorry, try again" before the password prompt (real HP, 2026-10-09).
//
// Prints on record are not enough: the reader must also open. fprintd keeps
// a reader it can no longer open when the device was taken away and given
// back under it (the USB trust loop of Plane 278 did this thousands of
// times on fw13, 2026-11-01). pam_fprintd then fails at once, every try.
// Claiming the reader the same way pam_fprintd does tells that apart.
//
// The answer only decides whether to offer the scan. The scan itself runs in
// pam_fprintd inside sudo; nothing here can make it succeed. Every state but
// fingerprintReady goes to the password.
type fingerprintProbe struct {
	State  fingerprintState
	Detail string
}

func (p fingerprintProbe) Message() string {
	switch p.State {
	case fingerprintNoService:
		return "No fingerprint service on this device; using password."
	case fingerprintNoReader:
		return "No fingerprint reader found; using password."
	case fingerprintNoPrints:
		return "No fingerprint enrolled for this account; using password. Enroll one with fprintd-enroll."
	case fingerprintUntrusted:
		return "Fingerprint service is not run by root (" + p.Detail + "); ignoring it and using password."
	case fingerprintProbeFailed:
		return "Fingerprint reader did not answer (" + p.Detail + "); using password."
	case fingerprintUnavailable:
		return "Fingerprint reader is busy or not working right now; using password."
	}
	return ""
}

const (
	fprintName   = "net.reactivated.Fprint"
	fprintDevice = "net.reactivated.Fprint.Device"
)

// fprintBus is the part of the system bus the probe uses, so tests can
// answer without fprintd.
type fprintBus interface {
	Activatable(ctx context.Context) ([]string, error)
	OwnerUID(ctx context.Context, name string) (uint32, error)
	DefaultDevice(ctx context.Context) (dbus.ObjectPath, error)
	EnrolledFingers(ctx context.Context, device dbus.ObjectPath) ([]string, error)
	Claim(ctx context.Context, device dbus.ObjectPath) error
	Release(ctx context.Context, device dbus.ObjectPath) error
}

type systemFprintBus struct{ conn *dbus.Conn }

func (b systemFprintBus) Activatable(ctx context.Context) ([]string, error) {
	var names []string
	err := b.conn.BusObject().CallWithContext(ctx,
		"org.freedesktop.DBus.ListActivatableNames", 0).Store(&names)
	return names, err
}

func (b systemFprintBus) OwnerUID(ctx context.Context, name string) (uint32, error) {
	var uid uint32
	err := b.conn.BusObject().CallWithContext(ctx,
		"org.freedesktop.DBus.GetConnectionUnixUser", 0, name).Store(&uid)
	return uid, err
}

func (b systemFprintBus) DefaultDevice(ctx context.Context) (dbus.ObjectPath, error) {
	var path dbus.ObjectPath
	err := b.conn.Object(fprintName, "/net/reactivated/Fprint/Manager").CallWithContext(ctx,
		"net.reactivated.Fprint.Manager.GetDefaultDevice", 0).Store(&path)
	return path, err
}

func (b systemFprintBus) EnrolledFingers(ctx context.Context, device dbus.ObjectPath) ([]string, error) {
	var fingers []string
	// An empty user name means the caller; asking for any other user needs
	// admin rights in polkit and would open a password dialog.
	err := b.conn.Object(fprintName, device).CallWithContext(ctx,
		fprintDevice+".ListEnrolledFingers", 0, "").Store(&fingers)
	return fingers, err
}

// Claim opens the reader for the caller, as pam_fprintd does before a scan.
// It needs the verify action, which polkit grants the active session without
// a dialog and refuses every other session outright.
func (b systemFprintBus) Claim(ctx context.Context, device dbus.ObjectPath) error {
	return b.conn.Object(fprintName, device).CallWithContext(ctx,
		fprintDevice+".Claim", 0, "").Err
}

func (b systemFprintBus) Release(ctx context.Context, device dbus.ObjectPath) error {
	return b.conn.Object(fprintName, device).CallWithContext(ctx,
		fprintDevice+".Release", 0).Err
}

func probeFingerprint(ctx context.Context) fingerprintProbe {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return fingerprintProbe{fingerprintProbeFailed, err.Error()}
	}
	defer conn.Close()
	return probeFingerprintOn(ctx, systemFprintBus{conn})
}

func probeFingerprintOn(ctx context.Context, bus fprintBus) fingerprintProbe {
	// Systems without fprintd have no D-Bus activation file for it.
	names, err := bus.Activatable(ctx)
	if err != nil {
		return fingerprintProbe{fingerprintProbeFailed, err.Error()}
	}
	found := false
	for _, name := range names {
		found = found || name == fprintName
	}
	if !found {
		return fingerprintProbe{State: fingerprintNoService}
	}

	device, err := bus.DefaultDevice(ctx)
	if err != nil {
		if dbusErrorName(err) == fprintName+".Error.NoSuchDevice" {
			return fingerprintProbe{State: fingerprintNoReader}
		}
		return fingerprintProbe{fingerprintProbeFailed, err.Error()}
	}
	if !device.IsValid() {
		return fingerprintProbe{fingerprintProbeFailed, "invalid device path"}
	}

	// The bus policy lets only root own this name. Check it anyway: the
	// answer below decides which PAM path runs. GetDefaultDevice has
	// activated fprintd, so the name has an owner now.
	uid, err := bus.OwnerUID(ctx, fprintName)
	if err != nil {
		return fingerprintProbe{fingerprintProbeFailed, err.Error()}
	}
	if uid != 0 {
		return fingerprintProbe{fingerprintUntrusted, "uid " + strconv.FormatUint(uint64(uid), 10)}
	}

	fingers, err := bus.EnrolledFingers(ctx, device)
	if err != nil {
		if dbusErrorName(err) == fprintName+".Error.NoEnrolledPrints" {
			return fingerprintProbe{State: fingerprintNoPrints}
		}
		return fingerprintProbe{fingerprintProbeFailed, err.Error()}
	}
	if len(fingers) == 0 {
		return fingerprintProbe{State: fingerprintNoPrints}
	}

	// fprintd releases a claim when the claiming connection closes, so a
	// failed Release cannot leave the reader held: probeFingerprint closes
	// its connection on return.
	if err := bus.Claim(ctx, device); err != nil {
		return fingerprintProbe{fingerprintUnavailable, dbusErrorName(err)}
	}
	if err := bus.Release(ctx, device); err != nil {
		return fingerprintProbe{fingerprintUnavailable, dbusErrorName(err)}
	}
	return fingerprintProbe{State: fingerprintReady}
}

func dbusErrorName(err error) string {
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		return dbusErr.Name
	}
	var dbusErrPtr *dbus.Error
	if errors.As(err, &dbusErrPtr) {
		return dbusErrPtr.Name
	}
	return ""
}

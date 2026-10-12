package readmodel

import (
	"context"
	"fmt"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/oddcsource"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/roothub"
)

// ResolvedSource returns the already-resolved ODDC machine view.
//
// It must not make policy decisions. NixOS supplies its fully resolved view;
// standalone tools may resolve an explicitly selected catalog model.
type ResolvedSource interface {
	Resolved(context.Context) (map[string]any, error)
}

// ObservationSource returns current enforcement-relevant USB devices.
//
// Host controllers and other non-device infrastructure should be filtered
// by the concrete observation source, not by this composition layer.
type ObservationSource interface {
	Observed(context.Context) ([]usbtrust.ObservedDevice, error)
}

// TrustSource returns verified authoritative machine-local trust state.
//
// A nil document with nil error means this machine has not enrolled USB
// trust state yet.
type TrustSource interface {
	Trusted(context.Context) (*usbtrust.Document, error)
}

// RootHubSource names the USB root hubs, so observations can carry the
// root port they sit on.
type RootHubSource interface {
	RootHubs(context.Context) ([]roothub.RootHub, error)
}

type Reader struct {
	Resolved     ResolvedSource
	Observations ObservationSource
	Trust        TrustSource
	// Devices and RootHubs are optional. With both, parts ODDC lists with a
	// usbPort are judged by their root port.
	Devices  DevicesSource
	RootHubs RootHubSource
}

// Expected returns the USB expectations for this machine. Parts with a
// usbPort replace the resolved view's list; a model without any keeps it.
func (r Reader) Expected(ctx context.Context) ([]usbtrust.ExpectedDevice, error) {
	if r.Devices != nil {
		devices, err := r.Devices.Devices(ctx)
		if err != nil {
			return nil, fmt.Errorf("read ODDC devices: %w", err)
		}
		if len(devices.Expected) > 0 {
			return devices.Expected, nil
		}
	}

	resolved, err := r.Resolved.Resolved(ctx)
	if err != nil {
		return nil, fmt.Errorf(
			"read resolved ODDC state: %w",
			err,
		)
	}

	expected, err := oddcsource.Expected(resolved)
	if err != nil {
		return nil, fmt.Errorf(
			"derive USB expectations from ODDC: %w",
			err,
		)
	}

	return expected, nil
}

// Observed returns the live USB devices with their root port attached.
func (r Reader) Observed(ctx context.Context) ([]usbtrust.ObservedDevice, error) {
	observed, err := r.Observations.Observed(ctx)
	if err != nil {
		return nil, fmt.Errorf(
			"observe live USB state: %w",
			err,
		)
	}

	if r.RootHubs != nil {
		hubs, err := r.RootHubs.RootHubs(ctx)
		if err != nil {
			return nil, fmt.Errorf("read USB root hubs: %w", err)
		}
		roothub.AttachWiring(observed, hubs)
	}

	return observed, nil
}

type snapshot struct {
	expected []usbtrust.ExpectedDevice
	observed []usbtrust.ObservedDevice
	trusted  *usbtrust.Document
}

func (r Reader) Status(
	ctx context.Context,
) (broker.Status, error) {
	state, err := r.snapshot(ctx)
	if err != nil {
		return broker.Status{}, err
	}

	status := broker.Status{
		StatePresent:    state.trusted != nil,
		ExpectedDevices: len(state.expected),
		ObservedDevices: len(state.observed),
	}

	if state.trusted != nil {
		status.Revision = state.trusted.Revision
		status.TrustedDevices = len(state.trusted.Devices)
	}

	return status, nil
}

func (r Reader) Audit(
	ctx context.Context,
) (usbtrust.AuditResult, uint64, error) {
	state, err := r.snapshot(ctx)
	if err != nil {
		return usbtrust.AuditResult{}, 0, err
	}

	result, err := usbtrust.Audit(usbtrust.AuditInput{
		Expected: state.expected,
		Trusted:  state.trusted,
		Observed: state.observed,
	})
	if err != nil {
		return usbtrust.AuditResult{}, 0, fmt.Errorf(
			"audit USB trust snapshot: %w",
			err,
		)
	}

	var revision uint64

	if state.trusted != nil {
		revision = state.trusted.Revision
	}

	return result, revision, nil
}

func (r Reader) snapshot(
	ctx context.Context,
) (snapshot, error) {
	if r.Resolved == nil {
		return snapshot{}, fmt.Errorf(
			"USB trust resolved ODDC source is unavailable",
		)
	}

	if r.Observations == nil {
		return snapshot{}, fmt.Errorf(
			"USB trust observation source is unavailable",
		)
	}

	if r.Trust == nil {
		return snapshot{}, fmt.Errorf(
			"USB trust authoritative state source is unavailable",
		)
	}

	expected, err := r.Expected(ctx)
	if err != nil {
		return snapshot{}, err
	}

	observed, err := r.Observed(ctx)
	if err != nil {
		return snapshot{}, err
	}

	trusted, err := r.Trust.Trusted(ctx)
	if err != nil {
		return snapshot{}, fmt.Errorf(
			"read verified USB trust state: %w",
			err,
		)
	}

	if trusted != nil {
		if err := trusted.Validate(); err != nil {
			return snapshot{}, fmt.Errorf(
				"verified USB trust state is invalid: %w",
				err,
			)
		}
	}

	return snapshot{
		expected: expected,
		observed: observed,
		trusted:  trusted,
	}, nil
}

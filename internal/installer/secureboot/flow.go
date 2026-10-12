package secureboot

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

// The installer and `gjallarctl secure-boot setup` take the same steps:
// Prepare before the rebuild that turns on lanzaboote, Continue after it.

// Prepare creates the GjallarOS keys on a machine still owned by its
// factory keys and records the pending enrollment. A pending or finished
// GjallarOS enrollment is reused; every other state is refused.
func Prepare(ctx context.Context, out io.Writer) error {
	inspection, err := Inspect(ctx)
	if err != nil {
		return fmt.Errorf("inspect existing Secure Boot ownership: %w", err)
	}
	fmt.Fprintf(out, "Secure Boot state: %s -- %s\n", inspection.State, inspection.Description)

	switch inspection.State {
	case StateGjallarManaged:
		fmt.Fprintln(out, "Existing GjallarOS Secure Boot ownership detected; reusing the current PK/KEK/db key set.")
		if !inspection.Recorded {
			if err := RecordOwnership(ctx, "enrolled"); err != nil {
				return err
			}
			fmt.Fprintln(out, "Recorded legacy GjallarOS/sbctl Secure Boot ownership without rotating keys.")
		}
		return nil

	case StatePendingEnrollment:
		fmt.Fprintln(out, "Existing GjallarOS Secure Boot provisioning is pending; reusing the prepared key set.")
		return nil

	case StateOEMFactoryDerived:
		if inspection.SecureBoot {
			return errors.New("factory Secure Boot is currently enforced; disable Secure Boot before transferring ownership to GjallarOS")
		}

		fmt.Fprintln(out, "OEM/factory-derived Secure Boot root detected; preparing machine-specific GjallarOS ownership.")

		// Provision() creates a root-only temporary recovery checkpoint so
		// an interrupted run can resume with the same recovery
		// archive/passphrase.
		recovery, err := Provision(ctx)
		if err != nil {
			return err
		}
		recovery.Passphrase = ""

		return RecordOwnership(ctx, "pending-enrollment")

	case StateSetupModeUnknown:
		return errors.New("firmware is already in Secure Boot Setup Mode but GjallarOS cannot prove that it initiated the transition; refusing automatic enrollment. Restore the factory Secure Boot keys in firmware setup, turn Secure Boot off, and run this again")

	case StateForeignManaged:
		return errors.New("an existing non-GjallarOS Secure Boot owner configuration was detected; refusing to replace it automatically")

	case StateInconsistent:
		return errors.New("GjallarOS Secure Boot ownership metadata does not match the current key material; refusing to continue")

	default:
		return fmt.Errorf("unsupported Secure Boot ownership state %q", inspection.State)
	}
}

// UI is what Continue asks of the user.
type UI interface {
	ShowSecureBootRecovery(ctx context.Context, archivePath, passphrase string) error
	Confirm(ctx context.Context, message string, defaultYes bool) (bool, error)
	SecureBootEnableHandoff(ctx context.Context, firmwareName string, lockSteps []string) error
	SecureBootFirmwareHandoff(ctx context.Context, firmwareName string, policyInstructions, lockSteps []string) error
}

// Continue runs after the rebuild: it makes sure the recovery material was
// saved, verifies the signed boot chain, arms the enrollment and hands the
// firmware steps to the user. The returned continuation says whether a
// reboot into firmware setup is due.
func Continue(
	ctx context.Context,
	ui UI,
	out io.Writer,
	modelID string,
	firmware oddc.EffectiveSecureBootFirmwarePolicy,
	firmwarePasswordLock bool,
) (Continuation, error) {
	inspection, err := Inspect(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Secure Boot state before continuation: %w", err)
	}

	if inspection.State == StateOEMFactoryDerived ||
		inspection.State == StatePendingEnrollment ||
		inspection.State == StateGjallarManaged {
		if err := recoveryGate(ctx, ui, out); err != nil {
			return "", err
		}
	}

	firmwarePolicy, err := SnapshotFirmwarePolicy(modelID, firmware)
	if err != nil {
		return "", fmt.Errorf("prepare detected Secure Boot firmware policy: %w", err)
	}

	next, err := VerifyAndArmEnrollment(ctx, firmwarePolicy)
	if err != nil {
		return "", err
	}

	switch next {
	case ContinuationNone:
		fmt.Fprintln(out, "Secure Boot is active and the GjallarOS boot chain is verified.")

	case ContinuationEnable:
		fmt.Fprintln(out, "GjallarOS Secure Boot ownership is already enrolled.")
		fmt.Fprintln(out, "Only Secure Boot enforcement still needs to be enabled in firmware.")
		if err := ui.SecureBootEnableHandoff(
			ctx,
			firmwarePolicy.FirmwareName,
			FirmwareLockSteps(firmwarePasswordLock),
		); err != nil {
			return "", fmt.Errorf("Secure Boot enable handoff: %w", err)
		}

	case ContinuationEnroll:
		fmt.Fprintln(out, "Secure Boot ownership transfer armed and boot artifacts verified.")
		if err := ui.SecureBootFirmwareHandoff(
			ctx,
			firmwarePolicy.FirmwareName,
			firmwarePolicy.Instructions,
			FirmwareLockSteps(firmwarePasswordLock),
		); err != nil {
			return "", fmt.Errorf("Secure Boot firmware handoff: %w", err)
		}

	default:
		return "", fmt.Errorf("unknown Secure Boot continuation state %q", next)
	}
	return next, nil
}

// NeedsFirmwareReboot reports whether next leaves a step for firmware setup.
func NeedsFirmwareReboot(next Continuation) bool {
	return next == ContinuationEnroll || next == ContinuationEnable
}

// recoveryGate shows the recovery archive and passphrase until the user
// confirms twice that both are saved. A confirmed gate is not shown again.
func recoveryGate(ctx context.Context, ui UI, out io.Writer) error {
	confirmed, err := RecoveryConfirmed(ctx)
	if err != nil {
		return err
	}
	if confirmed {
		fmt.Fprintln(out, "Secure Boot recovery material was already confirmed saved; resuming provisioning.")
		return nil
	}

	recovery, err := Provision(ctx)
	if err != nil {
		return fmt.Errorf("prepare Secure Boot recovery material: %w", err)
	}
	defer func() { recovery.Passphrase = "" }()

	for {
		if err := ui.ShowSecureBootRecovery(ctx, recovery.ArchivePath, recovery.Passphrase); err != nil {
			return fmt.Errorf("display Secure Boot recovery material: %w", err)
		}

		saved, err := ui.Confirm(ctx, "Have you saved BOTH the Secure Boot recovery archive and passphrase?", false)
		if err != nil {
			return err
		}
		if !saved {
			fmt.Fprintln(out, "Recovery material not confirmed saved; showing it again.")
			continue
		}

		savedAgain, err := ui.Confirm(ctx, "Are you absolutely sure the Secure Boot recovery material is saved offline?", false)
		if err != nil {
			return err
		}
		if !savedAgain {
			fmt.Fprintln(out, "Second confirmation declined; showing the recovery material again.")
			continue
		}
		break
	}

	if err := MarkRecoveryConfirmed(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "Secure Boot recovery checkpoint completed; future interrupted runs resume after this gate.")
	return nil
}

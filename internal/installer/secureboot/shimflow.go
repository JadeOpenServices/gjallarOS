package secureboot

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// The shim path (Plane 293 step 4) turns Secure Boot on without touching
// the firmware's keys:
//
//  1. PrepareShim makes the GjallarOS keys and records "pending-mok"; the
//     caller then turns on secureBootEnable and secureBootShim and rebuilds,
//     which puts shim first in the firmware's boot order.
//  2. ContinueShim, on the next boot (through shim, Secure Boot still off),
//     queues our db certificate for MokManager. The user confirms it there
//     at the next start.
//  3. ContinueShim again, once shim lists the key: the user turns on Secure
//     Boot in the firmware setup, nothing else.
//  4. The first Secure Boot boot records "enrolled" (RecordShimEnrollment)
//     and the usual verification runs.

// StagePendingMOK is the ownership stage of a shim switch that waits for
// MokManager or for Secure Boot to be turned on.
const StagePendingMOK = "pending-mok"

// ContinuationReboot asks for a plain restart: into shim after the rebuild,
// or into MokManager once the key is queued.
const ContinuationReboot Continuation = "reboot"

// errShimPending keeps the own-keys flow away from a pending shim switch:
// both would hand out the same key set for different firmware changes.
var errShimPending = errors.New("a switch to shim is pending; finish it with gjallarctl secure-boot setup")

// PrepareShim creates the GjallarOS keys on a machine with its factory keys
// and records the pending shim switch. A pending or finished switch is
// reused; every other state is refused.
func PrepareShim(ctx context.Context, out io.Writer) error {
	inspection, err := Inspect(ctx)
	if err != nil {
		return fmt.Errorf("inspect existing Secure Boot ownership: %w", err)
	}
	fmt.Fprintf(out, "Secure Boot state: %s -- %s\n", inspection.State, inspection.Description)

	switch inspection.State {
	case StateGjallarShim:
		if inspection.Stage == StagePendingMOK {
			fmt.Fprintln(out, "shim already lists the GjallarOS key; reusing the prepared key set.")
			return nil
		}
		fmt.Fprintln(out, "This system already boots through shim with the GjallarOS key.")
		return nil

	case StatePendingEnrollment:
		if inspection.Stage != StagePendingMOK {
			return errors.New("a switch to GjallarOS firmware keys is pending; finish it with gjallarctl secure-boot setup --own-keys")
		}
		fmt.Fprintln(out, "A switch to shim is pending; reusing the prepared key set.")
		return nil

	case StateOEMFactoryDerived:
		if inspection.SecureBoot {
			// Locked firmware (Plane 293): another system's shim starts
			// this one, so the GjallarOS key can go into the same MOK list
			// before the boot chain changes. Without shim there is no way
			// in with Secure Boot on.
			started, err := ShimStarted()
			if err != nil {
				return err
			}
			if !started {
				return errors.New("Secure Boot is on, but this system does not boot through shim yet; turn Secure Boot off in the firmware setup and run this again")
			}
		}
		fmt.Fprintln(out, "The firmware keeps its maker's keys; preparing the GjallarOS key for shim.")
		recovery, err := Provision(ctx)
		if err != nil {
			return err
		}
		recovery.Passphrase = ""
		return RecordOwnership(ctx, StagePendingMOK)

	case StateGjallarManaged:
		return errors.New("this system already uses GjallarOS firmware keys; it does not need shim")

	case StateSetupModeUnknown:
		return errors.New("the firmware is in Setup Mode; set up GjallarOS firmware keys with gjallarctl secure-boot setup --own-keys")

	case StateForeignManaged:
		return errors.New("the firmware holds keys that are neither its factory keys nor GjallarOS keys; refusing to change Secure Boot")

	case StateInconsistent:
		return errors.New("GjallarOS Secure Boot ownership metadata does not match the current key material; refusing to continue")

	default:
		return fmt.Errorf("unsupported Secure Boot ownership state %q", inspection.State)
	}
}

// ContinueShim runs after the rebuild that turned on shim, once per boot
// until Secure Boot is on. The returned continuation says what restart is
// due: ContinuationReboot (into shim or MokManager), ContinuationEnable
// (into the firmware setup) or ContinuationNone (done).
func ContinueShim(
	ctx context.Context,
	ui UI,
	out io.Writer,
	firmwareName string,
	firmwarePasswordLock bool,
) (Continuation, error) {
	inspection, err := Inspect(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Secure Boot state before continuation: %w", err)
	}
	switch {
	case inspection.State == StateGjallarShim && inspection.Stage == "enrolled":
		fmt.Fprintln(out, "Secure Boot is on; shim checks this system against the GjallarOS key.")
		return ContinuationNone, nil
	case inspection.State == StateGjallarShim:
		return "", errors.New("Secure Boot is on through shim, but the first Secure Boot start was not recorded; restart once and look at: journalctl -b -u secure-boot-verification")
	case inspection.State != StatePendingEnrollment || inspection.Stage != StagePendingMOK:
		return "", fmt.Errorf("no shim switch is pending (state %q): %s", inspection.State, inspection.Description)
	}

	if _, present, err := liveFirmware.shimVar("MokListRT"); err != nil {
		return "", err
	} else if !present {
		fmt.Fprintln(out, "This boot did not start through shim. Restart once so the firmware starts shim, then run gjallarctl secure-boot setup again.")
		return ContinuationReboot, nil
	}

	if err := run(ctx, "sudo", privilegedTool("gjallar-verify-secure-boot-artifacts")); err != nil {
		return "", fmt.Errorf("verify signed boot files: %w", err)
	}
	if err := recoveryGate(ctx, ui, out); err != nil {
		return "", err
	}

	keys, ok, err := readLocalKeys(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("the GjallarOS Secure Boot keys are incomplete")
	}
	trust, err := ReadFirmwareTrust()
	if err != nil {
		return "", fmt.Errorf("read firmware trust: %w", err)
	}
	facts, err := LoadShimFacts(ShimFactsPath)
	if err != nil {
		return "", err
	}
	choice, err := ChoosePath(trust, facts, keys.dbDER)
	if err != nil {
		return "", err
	}
	if choice.Path != PathShim {
		return "", fmt.Errorf("this firmware would not start shim with the GjallarOS key: %s", choice.Reason)
	}

	enrolled, err := mokHolds(liveFirmware, "MokListRT", keys.dbDER)
	if err != nil {
		return "", err
	}
	if enrolled {
		if trust.ShimValidationOff {
			return "", errors.New("shim's own check is switched off in MokManager; turn it back on there before Secure Boot is turned on")
		}
		if err := run(ctx, "sudo", "install", "-m", "0600", "/dev/null", FinalMarkerPath); err != nil {
			return "", fmt.Errorf("arm final Secure Boot verification: %w", err)
		}
		fmt.Fprintln(out, "shim holds the GjallarOS key. Only Secure Boot itself still needs to be turned on in the firmware setup.")
		if err := ui.SecureBootEnableHandoff(ctx, firmwareName, FirmwareLockSteps(firmwarePasswordLock)); err != nil {
			return "", fmt.Errorf("Secure Boot enable handoff: %w", err)
		}
		return ContinuationEnable, nil
	}

	queued, err := mokHolds(liveFirmware, "MokNew", keys.dbDER)
	if err != nil {
		return "", err
	}
	if !queued {
		if err := queueMOK(ctx, keys.dbDER, out); err != nil {
			return "", err
		}
	} else {
		fmt.Fprintln(out, "The GjallarOS key already waits for MokManager.")
	}
	fmt.Fprint(out, mokManagerSteps(keys.dbDER, queued, "gjallarctl secure-boot setup"))
	return ContinuationReboot, nil
}

// ShimStarted tells whether shim started this boot: shim leaves MokListRT
// for the running system, and nothing else sets it.
func ShimStarted() (bool, error) {
	_, present, err := liveFirmware.shimVar("MokListRT")
	return present, err
}

// QueueShimKey runs before an install rebuilds a system that shim already
// starts with Secure Boot on (locked firmware, Plane 293). The rebuild puts
// the GjallarOS boot chain first, which shim starts only once its MOK list
// holds the GjallarOS key; before that, the old boot chain must stay. It
// reports whether shim lists the key. If not, it queues the key for
// MokManager and prints the steps; the caller stops, the user restarts,
// confirms the key and runs again ("again" names what to run).
func QueueShimKey(ctx context.Context, out io.Writer, facts ShimFacts, again string) (bool, error) {
	keys, ok, err := readLocalKeys(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("the GjallarOS Secure Boot keys are incomplete")
	}
	trust, err := ReadFirmwareTrust()
	if err != nil {
		return false, fmt.Errorf("read firmware trust: %w", err)
	}
	if trust.ShimValidationOff {
		return false, errors.New("shim's own check is switched off in MokManager; turn it back on there first")
	}
	choice, err := ChoosePath(trust, facts, keys.dbDER)
	if err != nil {
		return false, err
	}
	if choice.Path != PathShim {
		return false, fmt.Errorf("this firmware would not start shim with the GjallarOS key: %s", choice.Reason)
	}
	enrolled, err := mokHolds(liveFirmware, "MokListRT", keys.dbDER)
	if err != nil || enrolled {
		return enrolled, err
	}
	queued, err := mokHolds(liveFirmware, "MokNew", keys.dbDER)
	if err != nil {
		return false, err
	}
	if !queued {
		if err := queueMOK(ctx, keys.dbDER, out); err != nil {
			return false, err
		}
	} else {
		fmt.Fprintln(out, "The GjallarOS key already waits for MokManager.")
	}
	fmt.Fprint(out, mokManagerSteps(keys.dbDER, queued, again))
	return false, nil
}

// ContinueShimInstall runs after the install rebuilt a system that shim
// already starts with Secure Boot on. Secure Boot stays on, so a plain
// restart is the last step: the first start through the new boot chain
// records the enrollment (secure-boot-verification) and the installer's
// final checks run. verifier is the new generation's
// gjallar-verify-secure-boot-artifacts; the running system has none.
func ContinueShimInstall(ctx context.Context, ui UI, out io.Writer, verifier string) (Continuation, error) {
	inspection, err := Inspect(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Secure Boot state before continuation: %w", err)
	}
	switch {
	case inspection.State == StateGjallarShim && inspection.Stage == "enrolled":
		fmt.Fprintln(out, "Secure Boot is on; shim checks this system against the GjallarOS key.")
		return ContinuationNone, nil
	case inspection.State != StateGjallarShim || inspection.Stage != StagePendingMOK:
		return "", fmt.Errorf("shim does not list the GjallarOS key (state %q): %s", inspection.State, inspection.Description)
	}
	trust, err := ReadFirmwareTrust()
	if err != nil {
		return "", fmt.Errorf("read firmware trust: %w", err)
	}
	if trust.ShimValidationOff {
		return "", errors.New("shim's own check is switched off in MokManager; turn it back on there first")
	}
	if err := run(ctx, "sudo", verifier); err != nil {
		return "", fmt.Errorf("verify signed boot files: %w", err)
	}
	if err := recoveryGate(ctx, ui, out); err != nil {
		return "", err
	}
	if err := run(ctx, "sudo", "install", "-m", "0600", "/dev/null", FinalMarkerPath); err != nil {
		return "", fmt.Errorf("arm final Secure Boot verification: %w", err)
	}
	return ContinuationReboot, nil
}

// mokManagerSteps is the walk through MokManager. MokManager opens on
// "Continue boot", and names the key only by its certificate subject, so the
// steps carry the SHA-1 fingerprint that MokManager's "View key" shows.
func mokManagerSteps(der []byte, reused bool, again string) string {
	password := "Type the password you just chose"
	if reused {
		password = "Type the password chosen when the key was first queued"
	}
	return fmt.Sprintf(`
At the next start a blue screen (MokManager) asks to confirm the key.
It waits 10 seconds; press the down arrow before it runs out:
  1. Enroll MOK (the menu starts on Continue boot; press down, then Enter)
  2. View key 0; under [Fingerprint] it must read
     %s
     Press Enter to go back. If it differs, choose Continue boot instead.
  3. Continue
  4. Yes
  5. %s
  6. Reboot
If the 10 seconds ran out, run %[3]s again.
After the restart, run %[3]s again for the next step.
`, sha1Fingerprint(der), password, again)
}

// sha1Fingerprint formats der the way MokManager's "View key" prints it
// under [Fingerprint]: SHA-1 of the DER, ten bytes per line, space-separated
// (MokManager.c show_x509_info, shim 16.1).
func sha1Fingerprint(der []byte) string {
	sum := sha1.Sum(der)
	line := func(b []byte) string {
		parts := make([]string, len(b))
		for i, v := range b {
			parts[i] = fmt.Sprintf("%02x", v)
		}
		return strings.Join(parts, " ")
	}
	return line(sum[:10]) + "\n     " + line(sum[10:])
}

// queueMOK asks shim, through mokutil, to show der in MokManager at the next
// start. mokutil asks for a one-time password that MokManager asks again.
func queueMOK(ctx context.Context, der []byte, out io.Writer) error {
	if err := efiSpaceFor(liveFirmware.efivars, len(der)); err != nil {
		return err
	}
	file, err := os.CreateTemp("", "gjallar-mok-*.der")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(der); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Choose a one-time password for MokManager. MokManager reads the keyboard as US layout; letters and digits are safe.")
	if err := run(ctx, "sudo", privilegedTool("mokutil"), "--import", path); err != nil {
		return fmt.Errorf("queue the GjallarOS key for MokManager: %w", err)
	}
	queued, err := mokHolds(liveFirmware, "MokNew", der)
	if err != nil {
		return err
	}
	if !queued {
		return errors.New("mokutil returned, but the firmware holds no request for the GjallarOS key")
	}
	return nil
}

// mokHolds tells whether the shim variable name lists der.
func mokHolds(f firmwareFiles, name string, der []byte) (bool, error) {
	var raw []byte
	var present bool
	var err error
	if name == "MokNew" {
		// A request is an EFI variable only; shim never mirrors it.
		// mokutil writes it root-only (mode 0600, mokutil 0.7.2,
		// e2e-fw13 2026-11-03), so a user process reads it through root.
		raw, present, err = f.efivar(name, shimLockGUID)
		if errors.Is(err, fs.ErrPermission) {
			raw, present, err = f.rootEfivar(name, shimLockGUID)
		}
	} else {
		raw, present, err = f.shimVar(name)
	}
	if err != nil || !present {
		return false, err
	}
	list := parseSignatureList(raw, true)
	if list.Malformed {
		return false, fmt.Errorf("%s does not parse", name)
	}
	for _, c := range list.Certs {
		if bytes.Equal(c.der, der) {
			return true, nil
		}
	}
	return false, nil
}

// efiSpaceFor refuses a write the firmware storage may not hold: a full
// variable store has left machines unable to start.
func efiSpaceFor(efivars string, size int) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(efivars, &st); err != nil {
		return fmt.Errorf("read free firmware storage: %w", err)
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if st.Blocks == 0 {
		return errors.New("the kernel does not report free firmware storage; refusing to write a firmware variable")
	}
	// The request, its password hash and room for the firmware's own writes.
	if need := uint64(2*size + 8192); free < need {
		return fmt.Errorf("the firmware has %d bytes of variable storage free, the key request needs %d", free, need)
	}
	return nil
}

// RecordShimEnrollment runs on the first Secure Boot start through shim:
// it moves a pending shim switch to "enrolled". A recorded enrollment is
// left alone; any other state is an error.
func RecordShimEnrollment(ctx context.Context, out io.Writer) error {
	inspection, err := Inspect(ctx)
	if err != nil {
		return err
	}
	if inspection.State != StateGjallarShim {
		return fmt.Errorf("this start did not come through shim with the GjallarOS key (state %q): %s", inspection.State, inspection.Description)
	}
	switch inspection.Stage {
	case "enrolled":
		return nil
	case StagePendingMOK:
		if err := RecordOwnership(ctx, "enrolled"); err != nil {
			return err
		}
		fmt.Fprintln(out, "Recorded the first Secure Boot start through shim.")
		return nil
	default:
		return fmt.Errorf("unexpected Secure Boot ownership stage %q", inspection.Stage)
	}
}

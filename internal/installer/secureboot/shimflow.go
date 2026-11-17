package secureboot

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"syscall"
)

// The shim path (Plane 293 step 4) turns Secure Boot on without touching
// the firmware's keys:
//
//  1. PrepareShim makes the gjallarOS keys and records "pending-mok"; the
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

// PrepareShim creates the gjallarOS keys on a machine with its factory keys
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
			fmt.Fprintln(out, "shim already lists the gjallarOS key; reusing the prepared key set.")
			return nil
		}
		fmt.Fprintln(out, "This system already boots through shim with the gjallarOS key.")
		return nil

	case StatePendingEnrollment:
		if inspection.Stage != StagePendingMOK {
			return errors.New("a switch to gjallarOS firmware keys is pending; finish it with gjallarctl secure-boot setup --own-keys")
		}
		fmt.Fprintln(out, "A switch to shim is pending; reusing the prepared key set.")
		return nil

	case StateOEMFactoryDerived:
		if inspection.SecureBoot {
			// Locked firmware (Plane 293): another system's shim starts
			// this one, so the gjallarOS key can go into the same MOK list
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
		fmt.Fprintln(out, "The firmware keeps its maker's keys; preparing the gjallarOS key for shim.")
		recovery, err := Provision(ctx)
		if err != nil {
			return err
		}
		recovery.Passphrase = ""
		return RecordOwnership(ctx, StagePendingMOK)

	case StateGjallarManaged:
		return errors.New("this system already uses gjallarOS firmware keys; it does not need shim")

	case StateSetupModeUnknown:
		return errors.New("the firmware is in Setup Mode; set up gjallarOS firmware keys with gjallarctl secure-boot setup --own-keys")

	case StateForeignManaged:
		return errors.New("the firmware holds keys that are neither its factory keys nor gjallarOS keys; refusing to change Secure Boot")

	case StateInconsistent:
		return errors.New("gjallarOS Secure Boot ownership metadata does not match the current key material; refusing to continue")

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
		fmt.Fprintln(out, "Secure Boot is on; shim checks this system against the gjallarOS key.")
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
		return "", errors.New("the gjallarOS Secure Boot keys are incomplete")
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
		return "", fmt.Errorf("this firmware would not start shim with the gjallarOS key: %s", choice.Reason)
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
		fmt.Fprintln(out, "shim holds the gjallarOS key. Only Secure Boot itself still needs to be turned on in the firmware setup.")
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
		fmt.Fprintln(out, "The gjallarOS key already waits for MokManager.")
	}
	if err := holdMokManager(ctx, liveFirmware); err != nil {
		return "", err
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
// the gjallarOS boot chain first, which shim starts only once its MOK list
// holds the gjallarOS key; before that, the old boot chain must stay. It
// reports whether shim lists the key. If not, it queues the key for
// MokManager and prints the steps; the caller stops, the user restarts,
// confirms the key and runs again ("again" names what to run).
func QueueShimKey(ctx context.Context, ui UI, out io.Writer, facts ShimFacts, again string) (bool, error) {
	keys, err := shimKeyFits(ctx, facts)
	if err != nil {
		return false, err
	}
	if err := clearForeignMokNew(ctx, ui, out, liveFirmware, keys.dbDER); err != nil {
		return false, err
	}
	enrolled, err := mokHolds(liveFirmware, "MokListRT", keys.dbDER)
	if err != nil {
		return false, err
	}
	if enrolled {
		fmt.Fprintln(out, "shim lists the gjallarOS key; building gjallarOS with shim.")
		return true, nil
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
		fmt.Fprintln(out, "The gjallarOS key already waits for MokManager.")
	}
	if err := mokNewOnly(ctx, liveFirmware, keys.dbDER); err != nil {
		return false, err
	}
	if err := holdMokManager(ctx, liveFirmware); err != nil {
		return false, err
	}
	fmt.Fprint(out, mokManagerSteps(keys.dbDER, queued, again))
	return false, nil
}

// shimKeyFits reads the gjallarOS keys and checks that this firmware starts
// shim and that shim would start gjallarOS once it lists the key.
func shimKeyFits(ctx context.Context, facts ShimFacts) (localKeys, error) {
	keys, ok, err := readLocalKeys(ctx)
	if err != nil {
		return localKeys{}, err
	}
	if !ok {
		return localKeys{}, errors.New("the gjallarOS Secure Boot keys are incomplete")
	}
	trust, err := ReadFirmwareTrust()
	if err != nil {
		return localKeys{}, fmt.Errorf("read firmware trust: %w", err)
	}
	if trust.ShimValidationOff {
		return localKeys{}, errors.New("shim's own check is switched off in MokManager; turn it back on there first")
	}
	choice, err := ChoosePath(trust, facts, keys.dbDER)
	if err != nil {
		return localKeys{}, err
	}
	if choice.Path != PathShim {
		return localKeys{}, fmt.Errorf("this firmware would not start shim with the gjallarOS key: %s", choice.Reason)
	}
	return keys, nil
}

// CheckFreshShim runs before a fresh install from the installer stick with
// Secure Boot on. Nothing waits for MokManager yet: the live system keeps
// no answers across a restart, so the key is queued after the install
// (FinishFreshShim). Here only what would stop it then is checked, and
// requests that gjallarOS did not make (L97) are settled before the disk
// changes; FinishFreshShim checks them once more.
func CheckFreshShim(ctx context.Context, ui UI, out io.Writer, facts ShimFacts) error {
	keys, err := shimKeyFits(ctx, facts)
	if err != nil {
		return err
	}
	if _, _, err := StickHash(); err != nil {
		return err
	}
	if err := clearForeignMokNew(ctx, ui, out, liveFirmware, keys.dbDER); err != nil {
		return err
	}
	return clearForeignMokDel(ctx, ui, out, liveFirmware, liveStickCopy, keys.dbDER)
}

// FinishFreshShim runs after a fresh install from the installer stick with
// Secure Boot on, once nixos-install wrote the target. It checks the
// target's boot files (verify runs the target's
// gjallar-verify-secure-boot-artifacts), shows the recovery gate, queues the
// gjallarOS key and the deletion of the stick's own MOK entry for one
// MokManager visit, and arms the checks of the first start. The caller
// restarts.
func FinishFreshShim(ctx context.Context, ui UI, out io.Writer, facts ShimFacts, verify []string) (Continuation, error) {
	record, recorded, err := readOwnership(ctx)
	if err != nil {
		return "", fmt.Errorf("read gjallarOS Secure Boot ownership metadata: %w", err)
	}
	if !recorded {
		return "", errors.New("gjallarOS Secure Boot ownership metadata is missing")
	}
	keys, err := shimKeyFits(ctx, facts)
	if err != nil {
		return "", err
	}
	if err := verifyOwnershipRecord(record, keys, StagePendingMOK); err != nil {
		return "", err
	}
	if len(verify) == 0 {
		return "", errors.New("no boot file check given")
	}
	if err := run(ctx, "sudo", verify...); err != nil {
		return "", fmt.Errorf("verify signed boot files: %w", err)
	}
	if err := recoveryGate(ctx, ui, out); err != nil {
		return "", err
	}

	visit, err := queueVisit(ctx, ui, out, keys.dbDER)
	if err != nil {
		return "", err
	}
	if err := run(ctx, "sudo", "install", "-m", "0600", "/dev/null", FinalMarkerPath); err != nil {
		return "", fmt.Errorf("arm final Secure Boot verification: %w", err)
	}
	fmt.Fprint(out, visit.steps())
	return ContinuationReboot, nil
}

// mokVisit is what one MokManager visit after a fresh install confirms.
type mokVisit struct {
	key       []byte
	keyReused bool
	// del is MokDel in MokManager's order: one "View key N" per list.
	del       []SignatureList
	delReused bool
	// retry says what to do when the visit was missed; it ends the steps.
	retry string
}

// steps walks through MokManager. After each confirmed request MokManager
// returns to its menu, which then starts on Reboot.
func (v mokVisit) steps() string {
	if v.key == nil && len(v.del) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nAt the next start a blue screen (MokManager) asks to confirm the changes.\n")
	b.WriteString("Finish within 5 minutes: after that the computer restarts on its own and the changes are gone.\n")
	b.WriteString("The menu starts on Continue boot, which also drops the changes.\n")
	n := 0
	step := func(format string, args ...any) {
		n++
		fmt.Fprintf(&b, "  %d. "+format+"\n", append([]any{n}, args...)...)
	}
	password := func(reused bool) string {
		if reused {
			return "Type the password chosen when the request was made"
		}
		return "Type the password you just chose"
	}
	if v.key != nil {
		step("Enroll MOK (press down to it, then Enter)")
		step("View key 0; under [Fingerprint] it must read\n     %s\n     Press Enter to go back. If it differs, choose Continue boot instead.", sha1Fingerprint(v.key))
		step("Continue")
		step("Yes (the screen starts on No; press down, then Enter)")
		step("%s", password(v.keyReused))
	}
	if len(v.del) > 0 {
		step("Delete MOK (press down to it, then Enter)")
		for i, l := range v.del {
			view := "View key 0"
			if i > 0 {
				view = fmt.Sprintf("Press down to View key %d", i)
			}
			switch {
			case len(l.Certs) == 1 && l.Hashes == 0 && l.Other == 0:
				step("%s; under [Fingerprint] it must read\n     %s\n     (%s). Press Enter to go back.", view, sha1Fingerprint(l.Certs[0].der), strings.Trim(entryName(l.Certs[0].der), `"`))
			case len(l.FileSums) == 1 && len(l.Certs) == 0 && l.Hashes == 1 && l.Other == 0:
				step("%s; the SHA256 hash must start with\n     %s\n     (the installer stick). Press Enter to go back.", view, spacedHex(l.FileSums[0][:16]))
			default:
				step("%s; it holds more than one entry. Press Enter to go back.", view)
			}
		}
		step("Continue")
		step("Yes (the screen starts on No; press down, then Enter)")
		step("%s", password(v.delReused))
	}
	step("Reboot")
	retry := v.retry
	if retry == "" {
		retry = "start the installer stick again and rerun the installer."
	}
	b.WriteString("If it went on without the changes, " + retry + "\n")
	return b.String()
}

// spacedHex groups hex digits in pairs.
func spacedHex(s string) string {
	parts := make([]string, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		parts = append(parts, s[i:i+2])
	}
	return strings.Join(parts, " ")
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
		fmt.Fprintln(out, "Secure Boot is on; shim checks this system against the gjallarOS key.")
		return ContinuationNone, nil
	case inspection.State != StateGjallarShim || inspection.Stage != StagePendingMOK:
		return "", fmt.Errorf("shim does not list the gjallarOS key (state %q): %s", inspection.State, inspection.Description)
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
Finish within 5 minutes: after that the computer restarts on its own and the key
is gone. The menu starts on Continue boot, which also drops the key:
  1. Enroll MOK (the menu starts on Continue boot; press down, then Enter)
  2. View key 0; under [Fingerprint] it must read
     %s
     Press Enter to go back. If it differs, choose Continue boot instead.
  3. Continue
  4. Yes (the screen starts on No; press down, then Enter)
  5. %s
  6. Reboot
If it went on without the key, run %[3]s again.
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

// holdMokManager makes the next MokManager wait for a key press instead of
// counting down 10 seconds. shim deletes MokNew and MokDel before it shows
// the menu, so a countdown that runs out drops every queued request, and
// after a fresh install that meant installing again. shim reads MokTimeout
// once and deletes it; a negative value skips the countdown (MokManager.c
// draw_countdown, shim 16.1). mokutil gets no input from the user session.
// The menu still has only 5 minutes: shim 16.1 never resets the firmware
// watchdog that the boot manager arms (fixed upstream 58843bc13a, not in a
// release yet), so the guidance names the limit.
func holdMokManager(ctx context.Context, f firmwareFiles) error {
	if err := run(ctx, "sudo", privilegedTool("mokutil"), "--timeout", "-1"); err != nil {
		return fmt.Errorf("make MokManager wait for a key press: %w", err)
	}
	raw, present, err := f.efivar("MokTimeout", shimLockGUID)
	if errors.Is(err, fs.ErrPermission) {
		raw, present, err = f.rootEfivar("MokTimeout", shimLockGUID)
	}
	if err != nil {
		return fmt.Errorf("read MokTimeout back: %w", err)
	}
	// mokutil 0.7.2 set_timeout writes a C int (-1 = ff ff ff ff); shim 16.1
	// reads MokTimeoutvar.Timeout as INT32.
	if !present || !bytes.Equal(raw, []byte{0xff, 0xff, 0xff, 0xff}) {
		return errors.New("mokutil returned, but the firmware holds no MokManager wait")
	}
	return nil
}

// mokImportPath is where root keeps the key mokutil imports. /run belongs
// to root.
const mokImportPath = "/run/gjallar-mok-import.der"

// queueMOK asks shim, through mokutil, to show der in MokManager at the next
// start. mokutil asks for a one-time password that MokManager asks again.
func queueMOK(ctx context.Context, der []byte, out io.Writer) error {
	if err := efiSpaceFor(liveFirmware.efivars, len(der)); err != nil {
		return err
	}
	// Root mokutil reads the key from a root-owned file written from
	// memory. A file the user session made could be swapped for another
	// key, which MokManager would then offer to trust (L54).
	path := mokImportPath
	if err := writeRootFile(ctx, der, "0600", path); err != nil {
		return fmt.Errorf("stage the gjallarOS key for mokutil: %w", err)
	}
	defer func() { _ = run(context.WithoutCancel(ctx), "sudo", "rm", "-f", "--", path) }()
	fmt.Fprintln(out, "Choose a one-time password for MokManager. MokManager reads the keyboard as US layout; letters and digits are safe.")
	if err := run(ctx, "sudo", privilegedTool("mokutil"), "--import", path); err != nil {
		return fmt.Errorf("queue the gjallarOS key for MokManager: %w", err)
	}
	queued, err := mokHolds(liveFirmware, "MokNew", der)
	if err != nil {
		return err
	}
	if !queued {
		return errors.New("mokutil returned, but the firmware holds no request for the gjallarOS key")
	}
	return nil
}

// mokHolds tells whether the shim variable name lists der.
func mokHolds(f firmwareFiles, name string, der []byte) (bool, error) {
	raw, present, err := f.shimRequest(name)
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

// shimRequest reads a shim list: a pending request (MokNew, MokDel) from
// its EFI variable, anything else through shimVar. shim never mirrors a
// request, and mokutil writes it root-only (mode 0600, mokutil 0.7.2,
// e2e-fw13 2026-11-03), so a user process reads it through root.
func (f firmwareFiles) shimRequest(name string) ([]byte, bool, error) {
	if name != "MokNew" && name != "MokDel" {
		return f.shimVar(name)
	}
	raw, present, err := f.efivar(name, shimLockGUID)
	if errors.Is(err, fs.ErrPermission) {
		raw, present, err = f.rootEfivar(name, shimLockGUID)
	}
	return raw, present, err
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
		return fmt.Errorf("this start did not come through shim with the gjallarOS key (state %q): %s", inspection.State, inspection.Description)
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

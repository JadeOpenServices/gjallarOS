package secureboot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type EnrollmentResult string

const (
	EnrollmentWaitingForSetupMode EnrollmentResult = "waiting-for-setup-mode"
	EnrollmentCompleted           EnrollmentResult = "completed"
)

type enrollmentOps struct {
	present         func(string) (bool, error)
	payload         func(string) ([]byte, bool, error)
	inspect         func(context.Context) (Inspection, error)
	command         func(context.Context, string, ...string) error
	verifyArtifacts func(context.Context) error
	recordOwnership func(context.Context, string) error
	glob            func(string) ([]string, error)
	armed           func() (bool, error)
	writeDBX        func(context.Context, []byte) error
}

func EnrollFirmware(ctx context.Context) (EnrollmentResult, error) {
	if os.Geteuid() != 0 {
		return "", fmt.Errorf("Secure Boot firmware enrollment requires root")
	}

	snapshot, err := LoadFirmwarePolicySnapshot(FirmwarePolicyPath)
	if err != nil {
		return "", fmt.Errorf("load trusted Secure Boot firmware policy: %w", err)
	}

	ops := enrollmentOps{
		present: func(name string) (bool, error) {
			_, present, err := efivarPayload(name)
			return present, err
		},
		payload:         efivarPayload,
		inspect:         Inspect,
		command:         run,
		verifyArtifacts: verifyBootArtifacts,
		recordOwnership: RecordOwnership,
		glob:            filepath.Glob,
		armed: func() (bool, error) {
			_, err := os.Stat(EnrollmentMarkerPath)
			if os.IsNotExist(err) {
				return false, nil
			}
			return err == nil, err
		},
		writeDBX: writeSignedDBX,
	}

	return enrollFirmware(ctx, snapshot, ops)
}

func enrollFirmware(
	ctx context.Context,
	snapshot FirmwarePolicySnapshot,
	ops enrollmentOps,
) (EnrollmentResult, error) {
	if err := ValidateFirmwarePolicySnapshot(snapshot); err != nil {
		return "", fmt.Errorf("invalid trusted Secure Boot firmware policy: %w", err)
	}

	inspection, err := ops.inspect(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Secure Boot ownership: %w", err)
	}

	if !inspection.SetupMode {
		// sbctl enrolled the GjallarOS keys, which ends Setup Mode, but a
		// later step of this transaction failed (e2e-target, 2026-09-29:
		// sbctl verify found no lsblk). Every rerun then waited for a PK
		// removal the firmware no longer needs; finish the transaction.
		if inspection.State == StateGjallarManaged {
			armed, err := ops.armed()
			if err != nil {
				return "", fmt.Errorf("inspect Secure Boot enrollment marker: %w", err)
			}
			if armed {
				return finishEnrollment(ctx, ops)
			}
		}
		return EnrollmentWaitingForSetupMode, nil
	}

	if inspection.State != StatePendingEnrollment {
		return "", fmt.Errorf(
			"refusing Secure Boot enrollment in Setup Mode from ownership state %q: %s",
			inspection.State,
			inspection.Description,
		)
	}

	if err := restoreDefaultDBX(ctx, snapshot, ops); err != nil {
		return "", err
	}

	for _, name := range snapshot.RequiredPresent {
		present, err := ops.present(name)
		if err != nil {
			return "", fmt.Errorf("inspect required EFI variable %s: %w", name, err)
		}
		if !present {
			return "", fmt.Errorf(
				"required EFI variable %s is missing; refusing Secure Boot enrollment",
				name,
			)
		}
	}

	for _, name := range snapshot.RequiredAbsent {
		present, err := ops.present(name)
		if err != nil {
			return "", fmt.Errorf("inspect forbidden EFI variable %s: %w", name, err)
		}
		if present {
			return "", fmt.Errorf(
				"EFI variable %s must be absent before Secure Boot enrollment",
				name,
			)
		}
	}

	untouchedBefore := make(map[string][]byte, len(snapshot.Untouched))
	for _, name := range snapshot.Untouched {
		payload, present, err := ops.payload(name)
		if err != nil {
			return "", fmt.Errorf(
				"read untouched EFI variable %s before enrollment: %w",
				name,
				err,
			)
		}
		if !present {
			return "", fmt.Errorf(
				"untouched EFI variable %s is missing before enrollment",
				name,
			)
		}

		untouchedBefore[name] = append([]byte(nil), payload...)
	}

	for _, name := range snapshot.PreserveFirmwareBuiltin {
		matches, err := ops.glob("/sys/firmware/efi/efivars/" + name + "-*")
		if err != nil {
			return "", fmt.Errorf("resolve EFI variable %s: %w", name, err)
		}
		if len(matches) != 1 {
			return "", fmt.Errorf(
				"expected exactly one %s EFI variable before enrollment, found %d",
				name,
				len(matches),
			)
		}
		if err := ops.command(ctx, "chattr", "-i", matches[0]); err != nil {
			return "", fmt.Errorf(
				"clear immutable protection on preserved EFI variable %s: %w",
				name,
				err,
			)
		}
	}

	if err := ops.command(ctx, "sbctl", enrollKeysArgs(snapshot)...); err != nil {
		return "", fmt.Errorf("enroll Secure Boot keys: %w", err)
	}

	for _, name := range snapshot.Untouched {
		payload, present, err := ops.payload(name)
		if err != nil {
			return "", fmt.Errorf(
				"read untouched EFI variable %s after enrollment: %w",
				name,
				err,
			)
		}
		if !present {
			return "", fmt.Errorf(
				"untouched EFI variable %s disappeared during enrollment",
				name,
			)
		}
		if !bytes.Equal(untouchedBefore[name], payload) {
			return "", fmt.Errorf(
				"untouched EFI variable %s changed during enrollment",
				name,
			)
		}
	}

	return finishEnrollment(ctx, ops)
}

// restoreDefaultDBX puts the firmware's default revocation list back when
// the policy wants dbx present but it is empty. Some firmware clears dbx
// together with the Platform Key (Setup Mode by "erase all keys"), and an
// empty dbx trusts every revoked boot loader again. The default comes from
// the firmware itself (dbxDefault), is written signed with the GjallarOS
// KEK and must read back byte for byte, or enrollment stops before the PK is
// written.
func restoreDefaultDBX(
	ctx context.Context,
	snapshot FirmwarePolicySnapshot,
	ops enrollmentOps,
) error {
	if !slices.Contains(snapshot.RequiredPresent, "dbx") {
		return nil
	}
	current, present, err := ops.payload("dbx")
	if err != nil {
		return fmt.Errorf("read EFI variable dbx: %w", err)
	}
	if present && len(current) != 0 {
		return nil
	}

	defaults, present, err := ops.payload("dbxDefault")
	if err != nil {
		return fmt.Errorf("read EFI variable dbxDefault: %w", err)
	}
	if !present || len(defaults) == 0 {
		return fmt.Errorf(
			"dbx is empty and the firmware has no default dbx to restore; refusing Secure Boot enrollment",
		)
	}

	if err := ops.writeDBX(ctx, defaults); err != nil {
		return fmt.Errorf("restore default dbx: %w", err)
	}

	written, present, err := ops.payload("dbx")
	if err != nil {
		return fmt.Errorf("read restored dbx: %w", err)
	}
	if !present || !bytes.Equal(written, defaults) {
		return fmt.Errorf(
			"restored dbx does not match the firmware default dbx; refusing Secure Boot enrollment",
		)
	}
	return nil
}

// writeSignedDBX writes esl, an EFI signature list, to dbx as an
// authenticated update signed with the GjallarOS KEK (efitools, as in
// https://wiki.archlinux.org/title/Unified_Extensible_Firmware_Interface/Secure_Boot).
func writeSignedDBX(ctx context.Context, esl []byte) error {
	dir, err := os.MkdirTemp("/run", "gjallar-dbx-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	eslPath := filepath.Join(dir, "dbx.esl")
	authPath := filepath.Join(dir, "dbx.auth")
	if err := os.WriteFile(eslPath, esl, 0o600); err != nil {
		return err
	}
	if err := run(
		ctx,
		"sign-efi-sig-list",
		"-k", "/var/lib/sbctl/keys/KEK/KEK.key",
		"-c", "/var/lib/sbctl/keys/KEK/KEK.pem",
		"dbx", eslPath, authPath,
	); err != nil {
		return fmt.Errorf("sign dbx update: %w", err)
	}
	if err := run(ctx, "efi-updatevar", "-f", authPath, "dbx"); err != nil {
		return fmt.Errorf("write dbx: %w", err)
	}
	return nil
}

// finishEnrollment verifies and records GjallarOS ownership once its keys are
// in firmware, then hands over to final Secure Boot verification.
func finishEnrollment(ctx context.Context, ops enrollmentOps) (EnrollmentResult, error) {
	if err := ops.verifyArtifacts(ctx); err != nil {
		return "", fmt.Errorf("verify Secure Boot artifacts after enrollment: %w", err)
	}

	after, err := ops.inspect(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Secure Boot ownership after enrollment: %w", err)
	}
	if after.SetupMode {
		return "", fmt.Errorf("Secure Boot enrollment completed without leaving Setup Mode")
	}
	if after.State != StateGjallarManaged {
		return "", fmt.Errorf(
			"Secure Boot enrollment did not establish GjallarOS ownership: %s",
			after.Description,
		)
	}

	if err := ops.recordOwnership(ctx, "enrolled"); err != nil {
		return "", fmt.Errorf("record enrolled Secure Boot ownership: %w", err)
	}

	if err := ops.command(
		ctx,
		"install",
		"-m", "0600",
		"/dev/null",
		FinalMarkerPath,
	); err != nil {
		return "", fmt.Errorf("arm final Secure Boot verification: %w", err)
	}

	if err := ops.command(
		ctx,
		"rm", "-f", "--",
		EnrollmentMarkerPath,
		RecoveryConfirmedMarkerPath,
		RecoveryPassphrasePath,
	); err != nil {
		return "", fmt.Errorf("complete Secure Boot enrollment transaction: %w", err)
	}

	return EnrollmentCompleted, nil
}

func enrollKeysArgs(snapshot FirmwarePolicySnapshot) []string {
	args := []string{"enroll-keys"}
	if len(snapshot.PreserveFirmwareBuiltin) != 0 {
		args = append(
			args,
			"--firmware-builtin="+strings.Join(snapshot.PreserveFirmwareBuiltin, ","),
		)
	}
	// sbctl refuses when the TPM event log lists option ROMs, unless told the
	// new db still trusts their signer; it ignores --firmware-builtin for
	// that check. A preserved built-in db keeps the vendor CAs that sign the
	// ROMs, so the refusal only blocked enrollment (e2e-target, 2026-09-29:
	// virtio-net iPXE ROM). In sbctl this flag skips only that check.
	if slices.Contains(snapshot.PreserveFirmwareBuiltin, "db") {
		args = append(args, "--yes-this-might-brick-my-machine")
	}
	return args
}

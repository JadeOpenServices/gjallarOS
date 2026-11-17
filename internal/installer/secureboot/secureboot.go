package secureboot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/rootfile"
)

const ArchivePath = "/var/lib/gjallarOS/recovery/secure-boot-keys.tar.enc"
const EnrollmentMarkerPath = "/var/lib/gjallarOS/secure-boot-enrollment-armed"
const FinalMarkerPath = "/var/lib/gjallarOS/secure-boot-enable-required"
const RecoveryPassphrasePath = "/var/lib/gjallarOS/secure-boot/recovery-passphrase.pending"
const RecoveryConfirmedMarkerPath = "/var/lib/gjallarOS/secure-boot-recovery-confirmed"

type Continuation string

const (
	ContinuationNone   Continuation = "none"
	ContinuationEnroll Continuation = "enroll"
	ContinuationEnable Continuation = "enable"
)

type Recovery struct {
	ArchivePath string
	Passphrase  string
}

func Provision(ctx context.Context) (Recovery, error) {
	// An interrupted installer may leave a root-only passphrase checkpoint.
	// Reuse it with the existing archive rather than silently replacing the
	// user's recovery material.
	if recovery, ok, err := pendingRecovery(ctx); err != nil {
		return Recovery{}, err
	} else if ok {
		return recovery, nil
	}

	passphrase, err := generatePassphrase()
	if err != nil {
		return Recovery{}, err
	}

	keyExists, err := sudoTest(
		ctx,
		"-f",
		"/var/lib/sbctl/keys/db/db.key",
	)
	if err != nil {
		return Recovery{}, fmt.Errorf(
			"check existing Secure Boot keys: %w",
			err,
		)
	}

	if !keyExists {
		// The key names carry the install date and minute, so MokManager
		// and a later install tell this key from older ones, also from one
		// made earlier the same day (R47). sbctl reads its
		// configuration from the pipe before it locks itself down.
		conf, err := sbctlConfig(time.Now().UTC().Format("2006-01-02 15:04 UTC"))
		if err != nil {
			return Recovery{}, err
		}
		if err := runWithInput(ctx, conf, "sudo", privilegedTool("sbctl"), "--config", "/dev/stdin", "create-keys"); err != nil {
			return Recovery{}, fmt.Errorf(
				"create Secure Boot keys: %w",
				err,
			)
		}
	}

	if err := run(
		ctx,
		"sudo", "install",
		"-d", "-m", "0700",
		"/var/lib/gjallarOS/recovery",
	); err != nil {
		return Recovery{}, err
	}

	if err := run(
		ctx,
		"sudo", "install",
		"-d", "-m", "0700",
		"/var/lib/gjallarOS/secure-boot",
	); err != nil {
		return Recovery{}, err
	}

	raw := "/var/lib/gjallarOS/recovery/.secure-boot-keys.tar"

	defer func() {
		_ = run(
			context.Background(),
			"sudo", "rm", "-f", "--", raw,
		)
	}()

	if err := run(
		ctx,
		"sudo", "tar",
		"-C", "/var/lib/sbctl",
		"-cf", raw,
		".",
	); err != nil {
		return Recovery{}, fmt.Errorf(
			"archive Secure Boot keys: %w",
			err,
		)
	}

	// openssl reads the passphrase from a pipe, never from a file the
	// user session could swap (L54).
	if err := runWithInput(
		ctx,
		[]byte(passphrase+"\n"),
		"sudo", privilegedTool("openssl"),
		"enc",
		"-aes-256-cbc",
		"-pbkdf2",
		"-salt",
		"-in", raw,
		"-out", ArchivePath,
		"-pass", "stdin",
	); err != nil {
		_ = run(
			ctx,
			"sudo", "rm", "-f", "--", ArchivePath,
		)
		return Recovery{}, fmt.Errorf(
			"encrypt Secure Boot recovery archive: %w",
			err,
		)
	}

	if err := run(
		ctx,
		"sudo", "chmod", "0600",
		ArchivePath,
	); err != nil {
		return Recovery{}, err
	}

	// This is intentionally temporary. It exists only so Ctrl+C before the
	// recovery confirmation can resume with the exact same passphrase.
	if err := writeRootFile(ctx, []byte(passphrase), "0600", RecoveryPassphrasePath); err != nil {
		return Recovery{}, fmt.Errorf(
			"save temporary Secure Boot recovery checkpoint: %w",
			err,
		)
	}

	// A newly generated archive means any old confirmation is stale.
	if err := run(
		ctx,
		"sudo", "rm", "-f", "--",
		RecoveryConfirmedMarkerPath,
	); err != nil {
		return Recovery{}, fmt.Errorf(
			"clear stale recovery confirmation: %w",
			err,
		)
	}

	return Recovery{
		ArchivePath: ArchivePath,
		Passphrase:  passphrase,
	}, nil
}

func pendingRecovery(ctx context.Context) (Recovery, bool, error) {
	archiveExists, err := sudoTest(ctx, "-s", ArchivePath)
	if err != nil {
		return Recovery{}, false, fmt.Errorf(
			"check Secure Boot recovery archive: %w",
			err,
		)
	}

	passphraseExists, err := sudoTest(
		ctx,
		"-s",
		RecoveryPassphrasePath,
	)
	if err != nil {
		return Recovery{}, false, fmt.Errorf(
			"check Secure Boot recovery passphrase checkpoint: %w",
			err,
		)
	}

	if !archiveExists || !passphraseExists {
		return Recovery{}, false, nil
	}

	cmd := commandContext(
		ctx,
		"sudo", "cat",
		RecoveryPassphrasePath,
	)

	out, err := cmd.Output()
	if err != nil {
		return Recovery{}, false, fmt.Errorf(
			"read Secure Boot recovery passphrase checkpoint: %w",
			err,
		)
	}

	passphrase := strings.TrimSpace(string(out))
	if passphrase == "" {
		return Recovery{}, false, fmt.Errorf(
			"Secure Boot recovery passphrase checkpoint is empty",
		)
	}

	return Recovery{
		ArchivePath: ArchivePath,
		Passphrase:  passphrase,
	}, true, nil
}

func RecoveryConfirmed(ctx context.Context) (bool, error) {
	ok, err := sudoTest(
		ctx,
		"-f",
		RecoveryConfirmedMarkerPath,
	)
	if err != nil {
		return false, fmt.Errorf(
			"check Secure Boot recovery confirmation: %w",
			err,
		)
	}

	return ok, nil
}

func MarkRecoveryConfirmed(ctx context.Context) error {
	if err := run(
		ctx,
		"sudo", "install",
		"-d", "-m", "0700",
		"/var/lib/gjallarOS",
	); err != nil {
		return fmt.Errorf(
			"create Secure Boot state directory: %w",
			err,
		)
	}

	// Write the completed checkpoint first. If power is lost immediately
	// afterwards, the next installer run knows this gate was completed.
	if err := run(
		ctx,
		"sudo", "install",
		"-m", "0600",
		"/dev/null",
		RecoveryConfirmedMarkerPath,
	); err != nil {
		return fmt.Errorf(
			"record Secure Boot recovery confirmation: %w",
			err,
		)
	}

	// The plaintext passphrase must not survive beyond confirmation.
	if err := run(
		ctx,
		"sudo", "rm", "-f", "--",
		RecoveryPassphrasePath,
	); err != nil {
		return fmt.Errorf(
			"destroy temporary Secure Boot recovery passphrase checkpoint: %w",
			err,
		)
	}

	return nil
}

var sudoTest = func(ctx context.Context, args ...string) (bool, error) {
	cmdArgs := append([]string{"test"}, args...)
	cmd := commandContext(ctx, "sudo", cmdArgs...)

	err := cmd.Run()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	return false, err
}

// VerifyAndArmEnrollment validates the installed boot chain and determines the
// next Secure Boot transition.
//
// Lanzaboote intentionally keeps the external /boot/EFI/nixos/kernel-*.efi
// byte-identical to the Nix-store kernel. The signed generation stub records
// and verifies that kernel hash. Individually Authenticode-signing that loose
// kernel changes its bytes and causes Lanzaboote to abort with
// SECURITY_VIOLATION / "Kernel hash does not match".
func VerifyAndArmEnrollment(
	ctx context.Context,
	firmwarePolicy FirmwarePolicySnapshot,
) (Continuation, error) {
	if err := verifyBootArtifacts(ctx); err != nil {
		return ContinuationNone, err
	}

	inspection, err := Inspect(ctx)
	if err != nil {
		return ContinuationNone, fmt.Errorf(
			"inspect Secure Boot ownership before enrollment: %w", err,
		)
	}

	if err := run(
		ctx,
		"sudo", "install", "-d", "-m", "0700",
		"/var/lib/gjallarOS",
	); err != nil {
		return ContinuationNone, fmt.Errorf(
			"create Secure Boot state directory: %w", err,
		)
	}

	switch inspection.State {
	case StateGjallarManaged:
		if err := RecordOwnership(ctx, "enrolled"); err != nil {
			return ContinuationNone, err
		}

		if err := run(
			ctx,
			"sudo", "rm", "-f", "--",
			EnrollmentMarkerPath,
		); err != nil {
			return ContinuationNone, fmt.Errorf(
				"clear stale enrollment marker: %w", err,
			)
		}

		if inspection.SecureBoot {
			_ = run(
				ctx,
				"sudo", "rm", "-f", "--",
				FinalMarkerPath,
			)
			return ContinuationNone, nil
		}

		if err := run(
			ctx,
			"sudo", "install", "-m", "0600",
			"/dev/null", FinalMarkerPath,
		); err != nil {
			return ContinuationNone, fmt.Errorf(
				"arm final Secure Boot verification: %w", err,
			)
		}

		return ContinuationEnable, nil

	case StateOEMFactoryDerived:
		if err := RecordOwnership(ctx, "pending-enrollment"); err != nil {
			return ContinuationNone, err
		}

	case StatePendingEnrollment:
		if err := RecordOwnership(ctx, "pending-enrollment"); err != nil {
			return ContinuationNone, err
		}

	default:
		return ContinuationNone, fmt.Errorf(
			"refusing to arm Secure Boot enrollment from state %q: %s",
			inspection.State,
			inspection.Description,
		)
	}

	if err := EnsureFirmwarePolicySnapshot(
		ctx,
		firmwarePolicy,
	); err != nil {
		return ContinuationNone, fmt.Errorf(
			"prepare trusted Secure Boot firmware policy transaction: %w",
			err,
		)
	}

	if err := run(
		ctx,
		"sudo", "rm", "-f", "--",
		FinalMarkerPath,
	); err != nil {
		return ContinuationNone, fmt.Errorf(
			"clear stale final Secure Boot marker: %w", err,
		)
	}

	if err := run(
		ctx,
		"sudo", "install", "-m", "0600",
		"/dev/null", EnrollmentMarkerPath,
	); err != nil {
		return ContinuationNone, fmt.Errorf(
			"arm firmware enrollment: %w", err,
		)
	}

	return ContinuationEnroll, nil
}

func verifyBootArtifacts(ctx context.Context) error {
	cmd := commandContext(ctx, "sudo", privilegedTool("sbctl"), "verify")
	cmd.Stdin = os.Stdin

	out, err := cmd.CombinedOutput()

	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}

	if err == nil {
		return nil
	}

	allowedUnsignedKernel := false

	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, "is not signed") {
			continue
		}

		path := ""
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "/boot/") {
				path = field
				break
			}
		}

		if strings.HasPrefix(path, "/boot/EFI/nixos/kernel-") &&
			strings.HasSuffix(path, ".efi") {
			allowedUnsignedKernel = true
			continue
		}

		return fmt.Errorf(
			"unexpected unsigned Secure Boot artifact: %s",
			line,
		)
	}

	if allowedUnsignedKernel {
		return nil
	}

	return fmt.Errorf("verify signed Secure Boot artifacts: %w", err)
}

func generatePassphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// privilegedTool returns the binary this process resolves for name. sudo
// replaces PATH with its secure_path, which on live media lacks the tools
// install.sh adds through `nix shell` (sbctl, openssl); an absolute path
// reaches them.
func privilegedTool(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// writeRootFile writes a root-only record from memory (L54). Tests
// replace it.
var writeRootFile = rootfile.Write

// runWithInput is run with input on stdin instead of the terminal.
var runWithInput = func(ctx context.Context, input []byte, name string, args ...string) error {
	cmd := commandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var run = func(ctx context.Context, name string, args ...string) error {
	cmd := commandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var geteuid = os.Geteuid

// commandContext is exec.CommandContext, except that a leading sudo is dropped
// when the process already runs as root: boot-time units such as
// secure-boot-enrollment have no sudo on PATH (e2e-target, 2026-09-29:
// `exec: "sudo": executable file not found in $PATH`).
func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if name == "sudo" && len(args) > 0 && geteuid() == 0 {
		name, args = args[0], args[1:]
	}
	return exec.CommandContext(ctx, name, args...)
}

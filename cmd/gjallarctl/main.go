// gjallarctl is the safe, non-interactive control tool for GjallarOS.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/JadeOpenServices/gjallarOS/internal/binarycache"
	"github.com/JadeOpenServices/gjallarOS/internal/hardware/deviceprobe"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/repojson"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	osuser "os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/JadeOpenServices/gjallarOS/internal/ai/agentexec"
	"github.com/JadeOpenServices/gjallarOS/internal/ai/aitoken"
	"github.com/JadeOpenServices/gjallarOS/internal/ai/modelbroker"
	aipolicy "github.com/JadeOpenServices/gjallarOS/internal/ai/policy"
	"github.com/JadeOpenServices/gjallarOS/internal/ai/profile"
	"github.com/JadeOpenServices/gjallarOS/internal/ai/research"
	"github.com/JadeOpenServices/gjallarOS/internal/input/xkb"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/bootstrap"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/checkoutowner"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deploy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deviceprofile"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/firmware"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/hardwareconfig"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/localgit"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/nixrender"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/policy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/release"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secrets"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
	"github.com/JadeOpenServices/gjallarOS/internal/oddccli"
	"github.com/JadeOpenServices/gjallarOS/internal/preset"
)

const (
	rebuildUIEnter = "\x1b[?1049h\x1b[?25l"
	rebuildUILeave = "\x1b[?25h\x1b[?1049l"
)

func main() {
	status := run(os.Args[1:], os.Stdout, os.Stderr)
	endPrivilegeSession()
	os.Exit(status)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "usb":
		return runUSB(args[1:], stdout, stderr)
	case "thunderbolt":
		return runThunderbolt(args[1:], stdout, stderr)
	case "installer":
		return runInstaller(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "preflight":
		return runPreflight(args[1:], stdout, stderr)
	case "oddc":
		return oddccli.Run(args[1:], stdout, stderr)
	case "device-probe":
		return runDeviceProbe(args[1:], stdout, stderr)
	case "fan":
		return runFan(args[1:], stdout, stderr)
	case "hyprland-rotate":
		return runHyprlandRotate(args[1:], stdout, stderr)
	case "ai":
		return runAI(args[1:], stdout, stderr)
	case "normalize":
		return runNormalize(args[1:], stdout, stderr)
	case "preset":
		return runPreset(args[1:], stdout, stderr)
	case "auth":
		return runAuth(args[1:], stderr)
	case "rebuild":
		return runRebuild(args[1:], stdout, stderr)
	case "cleanup":
		return runCleanup(args[1:], stdout, stderr)
	case "cleanup-old-generations":
		return runCleanupOld(args[1:], stdout, stderr)
	case "thermal-status":
		return runThermalStatus(args[1:], stdout, stderr)
	case "thermal-test":
		return runThermalTest(args[1:], stdout, stderr)
	case "helpme":
		return runHelpme(args[1:], stdout, stderr)
	case "tpm2":
		return runTPM2Reenroll(args[1:], stderr)
	case "luks":
		return runLUKSLifecycle(args[1:], stdout, stderr)
	case "vpn":
		return runVPN(args[1:], stdout, stderr)
	case "secure-boot":
		return runSecureBoot(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ERROR: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runInstaller(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl installer {check-secrets|deploy|firmware|generate-hardware|policy|protect-local|release|local-password}")
		return 2
	}
	if args[0] == "protect-local" {
		return runProtectLocal(args[1:], stdout, stderr)
	}
	if args[0] == "generate-hardware" {
		return runGenerateHardware(args[1:], stdout, stderr)
	}
	if args[0] == "deploy" {
		return runDeploy(args[1:], stdout, stderr)
	}
	if args[0] == "release" {
		return runRelease(args[1:], stdout, stderr)
	}
	if args[0] == "check-secrets" {
		return runCheckSecrets(args[1:], stdout, stderr)
	}
	if args[0] == "firmware" {
		return runFirmware(args[1:], stdout, stderr)
	}
	if args[0] == "secure-boot-enroll" {
		return runSecureBootEnroll(args[1:], stdout, stderr)
	}
	if args[0] == "secure-boot-verify-ownership" {
		return runSecureBootVerifyOwnership(args[1:], stdout, stderr)
	}
	if args[0] == "secure-boot-firmware-instructions" {
		return runSecureBootFirmwareInstructions(args[1:], stdout, stderr)
	}
	if args[0] == "tpm2-metadata-token-id" {
		return runTPM2MetadataTokenID(args[1:], stdout, stderr)
	}
	if args[0] == "tpm2-check-pcrlock-policy" {
		return runTPM2CheckPCRLockPolicy(args[1:], stdout, stderr)
	}
	if args[0] == "tpm2-write-keyslot-record" {
		return runTPM2WriteKeyslotRecord(args[1:], stdout, stderr)
	}
	if args[0] == "local-password" {
		return runLocalPassword(args[1:], stdout, stderr)
	}
	if args[0] == "tpm2" {
		return runTPM2(args[1:], stdout, stderr)
	}
	if args[0] == "bootstrap" {
		return runBootstrap(args[1:], stdout, stderr)
	}
	if args[0] != "policy" {
		fmt.Fprintln(stderr, "Usage: gjallarctl installer {check-secrets|deploy|firmware|generate-hardware|policy|protect-local|release|local-password}")
		return 2
	}
	flags := flag.NewFlagSet("gjallarctl installer policy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "user.config.json", "user configuration path")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	user, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	features := policy.FromUser(user)
	fmt.Fprintf(stdout, "ai_enable=%t\nauto_reboot=%t\ndebug_functions=%t\ndocker_enable=%t\nclamshell_enable=%t\nusbguard_enable=%t\nusb_trust_enforce=%t\nusb_trust_tpm_handle=%s\nnemu_enable=%t\ntouchpad_workspace_swipe=%t\n",
		features.AIEnable,
		features.AutoReboot,
		features.DebugFunctions,
		features.ContainersEnable,
		features.ClamshellEnable,
		features.USBGuardEnable,
		features.USBTrustEnforce,
		features.USBTrustTPMHandle,
		features.NemuEnable,
		features.TouchpadWorkspaceSwipe,
	)
	return 0
}

func runBootstrap(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer bootstrap", flag.ContinueOnError)
	f.SetOutput(stderr)
	configPath := f.String("config", "/etc/nixos/configuration.nix", "NixOS configuration")
	presetMode := f.Bool("preset", false, "omit graphical prompt dependency")
	apply := f.Bool("apply", false, "apply and rebuild")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	plan := bootstrap.Build(data, *presetMode)
	if len(plan.Missing) == 0 {
		fmt.Fprintln(stdout, "Bootstrap prerequisites already present.")
		if err := firmware.EnsureService(context.Background()); err != nil {
			fmt.Fprintf(stderr, "WARN: %v\n", err)
		}
		return 0
	}
	fmt.Fprintf(stdout, "Missing helpers/options: %s\nPackages: %s\n--- proposed %s ---\n%s\n--- end proposed ---\n", strings.Join(plan.Missing, ", "), strings.Join(plan.Packages, " "), *configPath, plan.Updated)
	if !*apply {
		return 3
	}
	backup, err := bootstrap.Apply(context.Background(), *configPath, plan.Updated, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		if backup != "" {
			fmt.Fprintf(stderr, "Backup remains at %s\n", backup)
		}
		return 1
	}
	if err := firmware.EnsureService(context.Background()); err != nil {
		fmt.Fprintf(stderr, "WARN: %v\n", err)
	}
	fmt.Fprintln(stdout, "Bootstrap rebuild succeeded; previous configuration backup removed.")
	return 0
}

func ttyConfirm(tty *os.File, reader *bufio.Reader, prompt string) (bool, bool, error) {
	fmt.Fprintf(tty, "%s [y/N/q] ", prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, false, nil
	case "q", "quit":
		return false, true, nil
	default:
		return false, false, nil
	}
}

func runTPM2(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer tpm2", flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "repository path")
	hardware := f.String("hardware", "", "hardware configuration")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	resolved, err := hardwareconfig.ValidateTarget(*repo, *hardware)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}
	mappingInfo, err := diskcrypto.MappingDetails(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if mappingInfo.Name == "" {
		fmt.Fprintln(stdout, "luks_tpm2_enable=false\n[NOTE] No LUKS mapping found for TPM2 enrollment.")
		return 0
	}
	if !diskcrypto.TPMAvailable() {
		fmt.Fprintln(stdout, "luks_tpm2_enable=false\n[NOTE] No TPM2 device detected; keeping passphrase unlock.")
		return 0
	}
	device, err := diskcrypto.DetectDevice(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: refusing TPM2 enrollment: %v\n", err)
		return 1
	}
	if device == "" {
		fmt.Fprintln(stdout, "luks_tpm2_enable=false\n[NOTE] No active crypto_LUKS device found; skipping TPM2 enrollment.")
		return 0
	}
	if err := diskcrypto.VerifyDeviceIdentity(mappingInfo.Device, device); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: TPM2 enrollment requires a terminal: %v\n", err)
		return 1
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)
	yes, _, err := ttyConfirm(tty, reader, "Prepare TPM2 automatic unlock after GjallarOS Secure Boot and measured boot are verified? The passphrase remains as recovery.")
	if err != nil || !yes {
		fmt.Fprintln(stdout, "luks_tpm2_enable=false")
		return 0
	}
	if err := diskcrypto.EnableTPMConfig(resolved); err != nil {
		fmt.Fprintf(stderr, "ERROR: TPM2 preparation failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "PLAN: after Secure Boot verification, request the disk passphrase and enroll this TPM against PCRLock on %s\n", device)
	fmt.Fprintln(stdout, "luks_tpm2_enable=true")
	return 0
}

func runLocalPassword(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer local-password", flag.ContinueOnError)
	f.SetOutput(stderr)
	username := f.String("username", "", "local username")
	apply := f.Bool("apply", false, "prompt and store a missing password hash")
	applyAccount := f.Bool("apply-account", false, "also set a newly stored hash on the existing account")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	target, err := credential.Path(*username)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "PLAN: reuse %s when present; otherwise prompt on /dev/tty, hash, and install mode 0600\n", target)
	if !*apply {
		return 0
	}
	if credential.Exists(context.Background(), target) {
		fmt.Fprintf(stdout, "Reusing stored password hash for %s.\n", *username)
		// A hash left by an earlier, interrupted run must still reach a
		// locked account; a password the account already has stays.
		if *applyAccount {
			if err := applyStoredHash(context.Background(), *username, target); err != nil {
				fmt.Fprintf(stderr, "ERROR: %v\n", err)
				return 1
			}
		}
		fmt.Fprintln(stdout, target)
		return 0
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: local password requires a terminal: %v\n", err)
		return 1
	}
	defer tty.Close()
	fmt.Fprintf(tty, "Set a password for %s (terminal only).\n", *username)
	password, err := credential.ReadConfirmedPassword(tty, tty, *username)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	hash, err := credential.Hash(context.Background(), password)
	password = ""
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if err := credential.Store(context.Background(), target, hash); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if *applyAccount {
		if err := credential.Apply(context.Background(), *username, hash); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Applied password to existing account %s.\n", *username)
	}
	hash = ""
	fmt.Fprintf(stdout, "Stored password hash for %s.\n%s\n", *username, target)
	return 0
}

func applyStoredHash(ctx context.Context, username, target string) error {
	_, usable, err := credential.AccountStatus(ctx, username)
	if err != nil || usable {
		return err
	}
	hash, err := credential.Load(ctx, target)
	if err != nil {
		return err
	}
	return credential.Apply(ctx, username, hash)
}

func runFirmware(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer firmware", flag.ContinueOnError)
	f.SetOutput(stderr)
	apply := f.Bool("apply", false, "refresh metadata and apply updates")
	ensure := f.Bool("ensure-service", false, "start fwupd service")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || (*apply && *ensure) {
		return 2
	}
	if !firmware.Available() {
		fmt.Fprintln(stdout, "[NOTE] fwupdmgr is unavailable; skipping firmware operations.")
		return 0
	}
	if *ensure {
		if err := firmware.EnsureService(context.Background()); err != nil {
			fmt.Fprintf(stderr, "WARN: %v\n", err)
		}
		return 0
	}
	fmt.Fprintln(stdout, "PLAN: sudo fwupdmgr refresh --force\nPLAN: sudo fwupdmgr update")
	if !*apply {
		return 0
	}
	result, err := firmware.Update(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "WARN: %v\n", err)
		return 0
	}
	if result == "none" {
		fmt.Fprintln(stdout, "No firmware updates available; continuing.")
	} else {
		fmt.Fprintln(stdout, "Firmware update process completed. A reboot may be required.")
	}
	return 0
}

func runSecureBootEnroll(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl installer secure-boot-enroll")
		return 2
	}

	result, err := secureboot.EnrollFirmware(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: Secure Boot firmware enrollment refused: %v\n", err)
		return 1
	}

	switch result {
	case secureboot.EnrollmentWaitingForSetupMode:
		fmt.Fprintln(
			stdout,
			"Secure Boot enrollment remains armed; firmware has not entered the required Setup Mode.",
		)
		return 0

	case secureboot.EnrollmentCompleted:
		fmt.Fprintln(
			stdout,
			"Secure Boot firmware enrollment completed from the trusted ODDC policy snapshot.",
		)
		return 0

	default:
		fmt.Fprintf(
			stderr,
			"ERROR: unexpected Secure Boot enrollment result %q\n",
			result,
		)
		return 1
	}
}

func runTPM2MetadataTokenID(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage: gjallarctl installer tpm2-metadata-token-id <metadata-json>")
		return 2
	}

	id, err := diskcrypto.ReadTPM2TokenID(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: TPM2 metadata validation failed: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, id)
	return 0
}

func runTPM2CheckPCRLockPolicy(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "Usage: gjallarctl installer tpm2-check-pcrlock-policy <policy> <pcr>...")
		return 2
	}

	pcrs := make([]int, 0, len(args)-1)
	for _, arg := range args[1:] {
		pcr, err := strconv.Atoi(arg)
		if err != nil || pcr < 0 || pcr > 23 {
			fmt.Fprintf(stderr, "ERROR: invalid PCR %q\n", arg)
			return 2
		}
		pcrs = append(pcrs, pcr)
	}

	if err := diskcrypto.CheckPCRLockPolicy(args[0], pcrs); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "pcrlock policy locks every requested PCR")
	return 0
}

func runTPM2WriteKeyslotRecord(args []string, stdout, stderr io.Writer) int {
	if len(args) != 4 {
		fmt.Fprintln(
			stderr,
			"Usage: gjallarctl installer tpm2-write-keyslot-record <metadata-json> <target-json> <device> <policy>",
		)
		return 2
	}

	if err := diskcrypto.WriteTPM2KeyslotRecord(
		args[0],
		args[1],
		args[2],
		args[3],
	); err != nil {
		fmt.Fprintf(stderr, "ERROR: TPM2 keyslot record failed: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "TPM2 keyslot record written")
	return 0
}

func runSecureBootFirmwareInstructions(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(
			stderr,
			"Usage: gjallarctl installer secure-boot-firmware-instructions",
		)
		return 2
	}

	instructions, err := secureboot.FirmwareInstructions(
		secureboot.FirmwarePolicyPath,
	)
	if err != nil {
		fmt.Fprintf(
			stderr,
			"ERROR: Secure Boot firmware instructions unavailable: %v\n",
			err,
		)
		return 1
	}

	for _, instruction := range instructions {
		fmt.Fprintln(stdout, instruction)
	}

	return 0
}

func runSecureBootVerifyOwnership(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(
			stderr,
			"Usage: gjallarctl installer secure-boot-verify-ownership [expected-stage]",
		)
		return 2
	}

	expectedStage := ""
	if len(args) == 1 {
		expectedStage = args[0]
	}

	if err := secureboot.VerifyOwnership(
		context.Background(),
		expectedStage,
	); err != nil {
		fmt.Fprintf(
			stderr,
			"ERROR: Secure Boot ownership verification failed: %v\n",
			err,
		)
		return 1
	}

	fmt.Fprintln(
		stdout,
		"PASS: active GjallarOS PK/KEK/db ownership verified.",
	)
	return 0
}

func runCheckSecrets(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer check-secrets", flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "repository path")
	username := f.String("username", "", "Linux username")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	status, err := secrets.Check(context.Background(), *repo, *username)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if !status.SecretsExist {
		fmt.Fprintln(stdout, "SOPS: no secrets/default.yaml found; continuing without secrets.")
		return 0
	}
	if status.KeyExists {
		fmt.Fprintf(stdout, "SOPS: age key found at %s\n", status.KeyPath)
	} else {
		fmt.Fprintf(stdout, "SOPS: age key missing at %s\nCreate it before rebuilding with age-keygen.\n", status.KeyPath)
	}
	if status.Decrypts {
		fmt.Fprintln(stdout, "SOPS: secrets/default.yaml decrypts successfully.")
	} else if status.SOPSAvailable && status.KeyExists {
		fmt.Fprintln(stdout, "SOPS: secrets/default.yaml cannot be decrypted with this user's key.\nRe-encrypt it with your age recipient before applying the configuration.")
	} else {
		fmt.Fprintln(stdout, "SOPS: install sops and age, then verify secrets/default.yaml before rebuilding.")
	}
	return 0
}

func runRelease(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer release", flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "repository path")
	osRelease := f.String("os-release", "/etc/os-release", "OS release file")
	apply := f.Bool("apply", false, "align a mismatched release")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	expected, actual, err := release.Inspect(*repo, *osRelease)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if actual == expected {
		fmt.Fprintf(stdout, "NixOS release verified: %s\n", actual)
		return 0
	}
	fmt.Fprintf(stdout, "NixOS %s detected; this checkout targets NixOS %s.\n", actual, expected)
	fmt.Fprintf(stdout, "PLAN: sudo nix-channel --add https://channels.nixos.org/nixos-%s nixos\nPLAN: sudo nix-channel --update nixos\nPLAN: sudo nixos-rebuild boot --upgrade\n", expected)
	if !*apply {
		return 3
	}

	const nixosConfig = "/etc/nixos/configuration.nix"
	if _, err := os.Stat(nixosConfig); err != nil {
		fmt.Fprintf(
			stderr,
			"ERROR: NixOS configuration unavailable at %s: %v\n",
			nixosConfig,
			err,
		)
		return 1
	}

	if err := release.Align(
		context.Background(),
		expected,
		nixosConfig,
	); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	fmt.Fprintf(
		stdout,
		"NixOS %s staged for the next boot. Reboot to activate it.\n",
		expected,
	)
	return 0
}

func runDeploy(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer deploy", flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "absolute repository path")
	hostname := f.String("hostname", "", "flake configuration name")
	apply := f.Bool("apply", false, "install the boot generation")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	target, err := deploy.Target(*repo, *hostname)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "PLAN: sudo nixos-rebuild dry-build --flake %s --show-trace\nPLAN: sudo nixos-rebuild boot --flake %s\n", target, target)
	if !*apply {
		return 0
	}
	if err := deploy.Apply(context.Background(), *repo, *hostname); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "NixOS boot generation installed.")
	return 0
}

func runGenerateHardware(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer generate-hardware", flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "absolute repository path")
	target := f.String("target", "", "absolute hardware configuration path")
	apply := f.Bool("apply", false, "generate and replace the target")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	resolved, err := hardwareconfig.ValidateTarget(*repo, *target)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "PLAN: run nixos-generate-config --show-hardware-config\nPLAN: atomically replace %s\n", resolved)
	if !*apply {
		return 0
	}
	if err := checkoutowner.Ensure(context.Background(), *repo, func(ctx context.Context, args ...string) error {
		if status := runPrivilegedCommand(ctx, stdout, stderr, args...); status != 0 {
			return fmt.Errorf("%s exited %d", args[0], status)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	backup, err := hardwareconfig.Generate(context.Background(), *repo, *target, time.Now())
	if repairErr := checkoutowner.Repair(*repo); repairErr != nil {
		fmt.Fprintf(stderr, "WARNING: %v\n", repairErr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if backup != "" {
		fmt.Fprintf(stdout, "Backup: %s\n", backup)
	}
	fmt.Fprintf(stdout, "Generated hardware configuration: %s\n", resolved)
	return 0
}

func runProtectLocal(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl installer protect-local", flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "absolute repository path")
	hardware := f.String("hardware", "", "optional hardware configuration path")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	if !filepath.IsAbs(*repo) {
		fmt.Fprintln(stderr, "ERROR: --repo must be an absolute path")
		return 2
	}
	protected, err := localgit.Protect(context.Background(), *repo, *hardware)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	for _, path := range protected {
		fmt.Fprintf(stdout, "Protected local config: %s\n", path)
	}
	return 0
}

func runPreset(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "validate" && args[0] != "get" && args[0] != "list" && args[0] != "bool") {
		fmt.Fprintln(stderr, "Usage: gjallarctl preset {validate|get|list|bool} --config PATH [--key NAME]")
		return 2
	}
	flags := flag.NewFlagSet("gjallarctl preset "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("config", "user.config.json", "preset JSON path")
	key := flags.String("key", "", "preset field name")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || (args[0] != "validate" && *key == "") {
		return 2
	}
	document, err := preset.Load(*config)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	switch args[0] {
	case "validate":
		return 0
	case "get":
		value, err := document.String(*key)
		if err == nil {
			fmt.Fprintln(stdout, value)
		}
		return presetResult(err, stderr)
	case "list":
		values, err := document.Strings(*key)
		if err == nil {
			for _, value := range values {
				fmt.Fprintln(stdout, value)
			}
		}
		return presetResult(err, stderr)
	case "bool":
		value, err := document.Bool(*key)
		if err == nil {
			fmt.Fprintln(stdout, value)
		}
		return presetResult(err, stderr)
	default:
		return 2
	}
}

func presetResult(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "ERROR: %v\n", err)
	return 1
}

func runNormalize(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "keyboard" {
		fmt.Fprintln(stderr, "Usage: gjallarctl normalize keyboard --layout VALUE")
		return 2
	}
	flags := flag.NewFlagSet("gjallarctl normalize keyboard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layout := flags.String("layout", "", "XKB keyboard layout")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	result, err := xkb.Normalize(*layout)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "layout=%s\nvariant=%s\n", result.Name, result.Variant)
	return 0
}

func runAIModelServe(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(
		"gjallarctl ai model-serve",
		flag.ContinueOnError,
	)

	f.SetOutput(stderr)

	socket := f.String(
		"socket",
		"/run/gjallar-ai-model/model.sock",
		"Unix socket",
	)

	upstream := f.String(
		"upstream",
		"http://127.0.0.1:11434",
		"Ollama upstream",
	)

	model := f.String(
		"model",
		"gjallaros-caveman-ai",
		"only exposed model",
	)

	upstreamModel := f.String(
		"upstream-model",
		"",
		"upstream name of the exposed model (default: --model)",
	)

	tokenFile := f.String(
		"upstream-token-file",
		"",
		"file with the bearer token of a central server",
	)

	username := f.String(
		"user",
		"",
		"only authorized desktop user",
	)

	cgroupPrefix := f.String(
		"cgroup-prefix",
		"/system.slice/ai-session@",
		"required system-service cgroup prefix",
	)

	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}

	if *username == "" {
		fmt.Fprintln(
			stderr,
			"ERROR: --user is required",
		)
		return 2
	}

	account, err := osuser.Lookup(*username)
	if err != nil {
		fmt.Fprintf(
			stderr,
			"ERROR: resolve agent user: %v\n",
			err,
		)
		return 1
	}

	uid64, err := strconv.ParseUint(
		account.Uid,
		10,
		32,
	)

	if err != nil {
		fmt.Fprintf(
			stderr,
			"ERROR: invalid agent uid: %v\n",
			err,
		)
		return 1
	}

	token := ""
	if *tokenFile != "" {
		token, err = aitoken.Read(*tokenFile)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
	}

	err = modelbroker.Serve(
		context.Background(),
		modelbroker.Config{
			SocketPath:           *socket,
			Upstream:             *upstream,
			Model:                *model,
			UpstreamModel:        *upstreamModel,
			UpstreamToken:        token,
			AllowedUID:           uint32(uid64),
			RequiredCgroupPrefix: *cgroupPrefix,
			MaxRequestBytes:      16 << 20,
			Timeout:              15 * time.Minute,
		},
	)

	if err != nil {
		fmt.Fprintf(
			stderr,
			"ERROR: model broker: %v\n",
			err,
		)
		return 1
	}

	return 0
}

// runAISetToken stores the central AI server token root-only for
// ai-endpoint-token-seal, which encrypts it, then restarts the AI services.
func runAISetToken(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl ai set-token")
		return 2
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: open terminal: %v\n", err)
		return 1
	}
	defer tty.Close()
	token, err := credential.ReadSecret(tty, bufio.NewReader(tty), stdout, "AI server token: ")
	if err == nil && os.Geteuid() != 0 {
		// Store runs sudo several times; one authentication covers them and
		// the restart below.
		if status := runPrivilegeAuthentication(context.Background(), stderr); status != 0 {
			return status
		}
	}
	if err == nil {
		err = aitoken.Store(context.Background(), token, aitoken.Pending)
	}
	if errors.Is(err, credential.ErrInterrupted) {
		fmt.Fprintln(stderr, "Stopped; the stored token is unchanged.")
		return 130
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if runPrivilegedCommand(context.Background(), stdout, stderr, "systemctl", "restart",
		"ai-endpoint-token-seal.service", "ai-model-broker.service", "ollama-model-provision.service") != 0 {
		fmt.Fprintln(stderr, "ERROR: token stored but AI services did not restart")
		return 1
	}
	fmt.Fprintln(stdout, "AI server token sealed; AI services restarted.")
	return 0
}

func runAI(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "model-serve" {
		return runAIModelServe(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "set-token" {
		return runAISetToken(args[1:], stdout, stderr)
	}

	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl ai {profile|tool|approve|research-serve|set-token}")
		return 2
	}
	if args[0] == "tool" {
		return runAITool(args[1:], stdout, stderr)
	}
	if args[0] == "approve" {
		return runAIApprove(args[1:], stdout, stderr)
	}
	if args[0] == "research-serve" {
		return runAIResearchServe(args[1:], stdout, stderr)
	}
	if args[0] != "profile" {
		return 2
	}
	flags := flag.NewFlagSet("gjallarctl ai profile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("config", "user.config.json", "user configuration path")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	result, err := profile.Detect(context.Background(), *config)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: AI profile detection failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "profile=%s\nmodel=%s\ncontext_tokens=%d\nvram_mb=%d\nram_gb=%d\ncpu_cores=%d\narchitecture=%s\ngpu_vendor=%s\ngpu_type=%s\n",
		result.Profile, result.Model, result.ContextTokens, result.VRAMMB, result.RAMGB, result.CPUCores, result.Architecture, result.GPUVendor, result.GPUType)
	return 0
}

func runAITool(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl ai tool", flag.ContinueOnError)
	f.SetOutput(stderr)
	workspace := f.String("workspace", ".", "active workspace")
	mode := f.String("mode", "workspace", "policy mode")
	approval := f.String("approval", "", "one-use approval file")
	audit := f.String("audit", "", "JSONL audit file")
	timeout := f.Duration("timeout", 10*time.Minute, "finite execution timeout")
	if err := f.Parse(args); err != nil || f.NArg() < 1 {
		return 2
	}
	if err := agentexec.Run(context.Background(), agentexec.Request{Workspace: *workspace, Tool: f.Arg(0), Args: f.Args()[1:], Mode: *mode, ApprovalPath: *approval, AuditPath: *audit, Timeout: *timeout}, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

func runAIApprove(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl ai approve", flag.ContinueOnError)
	f.SetOutput(stderr)
	workspace := f.String("workspace", ".", "active workspace")
	grant := f.String("grant", "", "approval file")
	if err := f.Parse(args); err != nil || f.NArg() < 1 || *grant == "" {
		return 2
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(stderr, "ERROR: approval requires a trusted interactive terminal")
		return 1
	}
	fmt.Fprintf(stderr, "Approve exact action %q with arguments %q? [y/N] ", f.Arg(0), f.Args()[1:])
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		fmt.Fprintln(stderr, "Denied.")
		return 1
	}
	if err := aipolicy.WriteGrant(*grant, *workspace, f.Arg(0), f.Args()[1:], time.Now()); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Exact one-use approval created; expires in five minutes.")
	return 0
}

func runAIResearchServe(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("gjallarctl ai research-serve", flag.ContinueOnError)
	f.SetOutput(stderr)
	socket := f.String("socket", "/run/gjallar-ai/research.sock", "Unix socket")
	maxBytes := f.Int64("max-bytes", 2<<20, "maximum response")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return 2
	}
	_ = os.Remove(*socket)
	if err := os.MkdirAll(filepath.Dir(*socket), 0750); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer listener.Close()
	_ = os.Chmod(*socket, 0600)
	b := research.Broker{AllowedHosts: []string{"nixos.org", "github.com", "docs.ollama.com", "opencode.ai"}, MaxBytes: *maxBytes, Timeout: 15 * time.Second}
	server := &http.Server{Handler: research.Handler(b), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func runDeviceProbe(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "refresh" {
		fmt.Fprintln(stderr, "Usage: gjallarctl device-probe refresh [--output PATH]")
		return 2
	}

	flags := flag.NewFlagSet("gjallarctl device-probe refresh", flag.ContinueOnError)
	flags.SetOutput(stderr)

	output := flags.String(
		"output",
		"/run/gjallarOS/device-probe.json",
		"device probe snapshot path",
	)

	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "ERROR: device-probe refresh accepts no positional arguments")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	snapshot, err := deviceprobe.Collect(ctx, "/sys")
	if err != nil {
		fmt.Fprintf(stderr, "FAIL: device probe collection: %v\n", err)
		return 1
	}

	if err := deviceprobe.WriteSnapshot(*output, snapshot); err != nil {
		fmt.Fprintf(stderr, "FAIL: device probe snapshot: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "PASS: device probe snapshot written to %s\n", *output)
	return 0
}

type fanSystemPolicy struct {
	QuietStrategy           string `json:"quietStrategy"`
	QuietEnterC             int    `json:"quietEnterC"`
	QuietExitC              int    `json:"quietExitC"`
	PerformanceStrategy     string `json:"performanceStrategy"`
	ThermalOverrideStrategy string `json:"thermalOverrideStrategy"`
	ThermalEnterC           int    `json:"thermalEnterC"`
	ThermalExitC            int    `json:"thermalExitC"`
}

type fanRuntimeConfig struct {
	Schema                int             `json:"schema"`
	Enabled               bool            `json:"enabled"`
	Backend               string          `json:"backend"`
	Profile               string          `json:"profile"`
	DefaultStrategy       string          `json:"defaultStrategy"`
	StrategyOnDischarging *string         `json:"strategyOnDischarging"`
	Strategies            []string        `json:"strategies"`
	SystemPolicy          fanSystemPolicy `json:"systemPolicy"`
}

type fwFanCurrent struct {
	Status   string `json:"status"`
	Strategy string `json:"strategy"`
	Default  bool   `json:"default"`
}

type fwFanActive struct {
	Status string `json:"status"`
	Active bool   `json:"active"`
}

type fwFanSpeed struct {
	Status string `json:"status"`
	Speed  string `json:"speed"`
}

type fanStatus struct {
	Enabled               bool     `json:"enabled"`
	Backend               string   `json:"backend"`
	Profile               string   `json:"profile,omitempty"`
	Current               string   `json:"current"`
	DefaultStrategy       string   `json:"defaultStrategy"`
	StrategyOnDischarging *string  `json:"strategyOnDischarging"`
	Strategies            []string `json:"strategies"`
	Active                bool     `json:"active"`
	SpeedPercent          int      `json:"speedPercent"`
	RequestedMode         string   `json:"requestedMode"`
	EffectiveStrategy     string   `json:"effectiveStrategy"`
}

func loadFanRuntimeConfig(path string) (fanRuntimeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fanRuntimeConfig{}, fmt.Errorf("read fan runtime config: %w", err)
	}

	var cfg fanRuntimeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fanRuntimeConfig{}, fmt.Errorf("decode fan runtime config: %w", err)
	}

	if cfg.Schema != 1 {
		return fanRuntimeConfig{}, fmt.Errorf("unsupported fan runtime schema %d", cfg.Schema)
	}
	if !cfg.Enabled {
		return fanRuntimeConfig{}, errors.New("ODDC fan control is not enabled")
	}
	if strings.TrimSpace(cfg.Backend) == "" {
		return fanRuntimeConfig{}, errors.New("ODDC fan control has no backend")
	}
	if len(cfg.Strategies) == 0 {
		return fanRuntimeConfig{}, errors.New("ODDC fan control has no strategies")
	}

	return cfg, nil
}

func readPlatformProfile() string {
	path := os.Getenv("GJALLAR_PLATFORM_PROFILE")
	if path == "" {
		path = "/sys/firmware/acpi/platform_profile"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readFanTemperatureC() (int, error) {
	root := os.Getenv("GJALLAR_FAN_HWMON_ROOT")
	if root == "" {
		root = "/sys/class/hwmon"
	}

	preferred := []string{
		"tctl",
		"tdie",
		"package",
		"cpu",
	}

	type candidate struct {
		priority int
		tempC    int
	}

	var best *candidate

	hwmons, err := filepath.Glob(filepath.Join(root, "hwmon*"))
	if err != nil {
		return 0, err
	}

	for _, hwmon := range hwmons {
		inputs, err := filepath.Glob(filepath.Join(hwmon, "temp*_input"))
		if err != nil {
			continue
		}

		for _, input := range inputs {
			raw, err := os.ReadFile(input)
			if err != nil {
				continue
			}

			milliC, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				continue
			}

			base := strings.TrimSuffix(input, "_input")
			labelData, _ := os.ReadFile(base + "_label")
			label := strings.ToLower(strings.TrimSpace(string(labelData)))

			priority := len(preferred) + 1
			for i, token := range preferred {
				if strings.Contains(label, token) {
					priority = i
					break
				}
			}

			if priority > len(preferred) {
				continue
			}

			tempC := milliC / 1000
			if best == nil ||
				priority < best.priority ||
				(priority == best.priority && tempC > best.tempC) {
				best = &candidate{
					priority: priority,
					tempC:    tempC,
				}
			}
		}
	}

	if best == nil {
		return 0, fmt.Errorf("no CPU/package hwmon temperature found")
	}

	return best.tempC, nil
}

func chooseFanStrategy(
	cfg fanRuntimeConfig,
	currentStrategy string,
	temperatureC int,
	powerProfile string,
) (string, string) {
	policy := cfg.SystemPolicy

	if policy.ThermalOverrideStrategy != "" {
		if temperatureC >= policy.ThermalEnterC {
			return policy.ThermalOverrideStrategy, "thermal"
		}
		if currentStrategy == policy.ThermalOverrideStrategy &&
			temperatureC > policy.ThermalExitC {
			return policy.ThermalOverrideStrategy, "thermal"
		}
	}

	if policy.QuietStrategy != "" {
		if temperatureC <= policy.QuietEnterC {
			return policy.QuietStrategy, "temperature"
		}

		if currentStrategy == policy.QuietStrategy &&
			temperatureC < policy.QuietExitC {
			return policy.QuietStrategy, "temperature"
		}
	}

	if powerProfile == "performance" && policy.PerformanceStrategy != "" {
		return policy.PerformanceStrategy, "power-profile"
	}

	if cfg.DefaultStrategy != "" {
		return cfg.DefaultStrategy, "system"
	}

	return currentStrategy, "system"
}

func fanStrategyAllowed(cfg fanRuntimeConfig, strategy string) bool {
	for _, candidate := range cfg.Strategies {
		if strategy == candidate {
			return true
		}
	}
	return false
}

func fwFanCtrlBinary() string {
	if path := os.Getenv("ODDC_FW_FANCTRL_BIN"); path != "" {
		return path
	}
	return "fw-fanctrl"
}

func runFanJSONCommand(target any, args ...string) error {
	command := exec.Command(fwFanCtrlBinary(), args...)
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf(
				"fw-fanctrl %s: %s",
				strings.Join(args, " "),
				strings.TrimSpace(string(exitErr.Stderr)),
			)
		}
		return fmt.Errorf("fw-fanctrl %s: %w", strings.Join(args, " "), err)
	}

	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf(
			"decode fw-fanctrl %s output: %w",
			strings.Join(args, " "),
			err,
		)
	}

	return nil
}

func reconcileFanOnce(cfg fanRuntimeConfig, stdout, stderr io.Writer) int {
	temperatureC, err := readFanTemperatureC()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: read fan temperature: %v\n", err)
		return 1
	}

	powerProfile := readPlatformProfile()

	switch cfg.Backend {
	case "fw-fanctrl":
		var current fwFanCurrent
		if err := runFanJSONCommand(
			&current,
			"--output-format", "JSON",
			"print", "current",
		); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}

		effective, reason := chooseFanStrategy(
			cfg,
			current.Strategy,
			temperatureC,
			powerProfile,
		)

		if !fanStrategyAllowed(cfg, effective) {
			fmt.Fprintf(
				stderr,
				"ERROR: policy selected unknown fan strategy %q\n",
				effective,
			)
			return 1
		}

		changed := effective != current.Strategy
		if changed {
			command := exec.Command(fwFanCtrlBinary(), "use", effective)
			if output, err := command.CombinedOutput(); err != nil {
				fmt.Fprintf(
					stderr,
					"ERROR: switch fan strategy: %v: %s\n",
					err,
					strings.TrimSpace(string(output)),
				)
				return 1
			}
		}

		if changed {
			result := map[string]any{
				"requestedMode":     "system",
				"effectiveStrategy": effective,
				"previousStrategy":  current.Strategy,
				"temperatureC":      temperatureC,
				"powerProfile":      powerProfile,
				"reason":            reason,
				"changed":           true,
			}

			if err := json.NewEncoder(stdout).Encode(result); err != nil {
				fmt.Fprintf(stderr, "ERROR: encode fan reconcile result: %v\n", err)
				return 1
			}
		}

		return 0

	default:
		fmt.Fprintf(stderr, "ERROR: unsupported fan backend %q\n", cfg.Backend)
		return 1
	}
}

func runFan(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl fan {status|list|reconcile|controller}")
		return 2
	}

	configPath := os.Getenv("ODDC_FAN_CONFIG")
	if configPath == "" {
		configPath = "/etc/oddc/fan-control.json"
	}

	cfg, err := loadFanRuntimeConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	switch args[0] {
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "Usage: gjallarctl fan list")
			return 2
		}

		for _, strategy := range cfg.Strategies {
			fmt.Fprintln(stdout, strategy)
		}
		return 0

	case "reconcile":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "Usage: gjallarctl fan reconcile")
			return 2
		}
		return reconcileFanOnce(cfg, stdout, stderr)

	case "controller":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "Usage: gjallarctl fan controller")
			return 2
		}

		for {
			if code := reconcileFanOnce(cfg, stdout, stderr); code != 0 {
				return code
			}
			time.Sleep(3 * time.Second)
		}

	case "status":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "Usage: gjallarctl fan status")
			return 2
		}

		switch cfg.Backend {
		case "fw-fanctrl":
			var current fwFanCurrent
			var active fwFanActive
			var speed fwFanSpeed

			if err := runFanJSONCommand(
				&current,
				"--output-format", "JSON",
				"print", "current",
			); err != nil {
				fmt.Fprintf(stderr, "ERROR: %v\n", err)
				return 1
			}

			if err := runFanJSONCommand(
				&active,
				"--output-format", "JSON",
				"print", "active",
			); err != nil {
				fmt.Fprintf(stderr, "ERROR: %v\n", err)
				return 1
			}

			if err := runFanJSONCommand(
				&speed,
				"--output-format", "JSON",
				"print", "speed",
			); err != nil {
				fmt.Fprintf(stderr, "ERROR: %v\n", err)
				return 1
			}

			speedPercent, err := strconv.Atoi(speed.Speed)
			if err != nil {
				fmt.Fprintf(stderr, "ERROR: invalid fw-fanctrl speed %q\n", speed.Speed)
				return 1
			}

			status := fanStatus{
				Enabled:               true,
				Backend:               cfg.Backend,
				Profile:               cfg.Profile,
				Current:               current.Strategy,
				DefaultStrategy:       cfg.DefaultStrategy,
				StrategyOnDischarging: cfg.StrategyOnDischarging,
				Strategies:            cfg.Strategies,
				Active:                active.Active,
				SpeedPercent:          speedPercent,
				RequestedMode:         "system",
				EffectiveStrategy:     current.Strategy,
			}

			encoder := json.NewEncoder(stdout)
			if err := encoder.Encode(status); err != nil {
				fmt.Fprintf(stderr, "ERROR: encode fan status: %v\n", err)
				return 1
			}
			return 0

		default:
			fmt.Fprintf(stderr, "ERROR: unsupported fan backend %q\n", cfg.Backend)
			return 1
		}

	default:
		fmt.Fprintln(stderr, "Usage: gjallarctl fan {status|list|reconcile|controller}")
		return 2
	}
}

func runPreflight(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gjallarctl preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "GjallarOS repository root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "ERROR: preflight accepts no positional arguments")
		return 2
	}

	root, err := installercheck.ResolveRepository(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}

	return reportPreflight(root, stdout, stderr)
}

func reportPreflight(root string, stdout, stderr io.Writer) int {
	report := installercheck.Preflight(root)
	fmt.Fprintln(stdout, "[GjallarOS] Fast preflight")
	for _, finding := range report.Findings {
		fmt.Fprintf(stdout, "%s: %s\n", finding.Level, finding.Message)
	}
	if report.Failed() {
		fmt.Fprintln(stderr, "FAIL: GjallarOS preflight failed")
		return 1
	}
	return 0
}

func reportRebuildPreflight(
	root string,
	stdout,
	stderr io.Writer,
) int {
	report := installercheck.Preflight(root)

	return reportRebuildPreflightResult(
		report.Findings,
		report.Failed(),
		stdout,
		stderr,
	)
}

func reportRebuildPreflightResult(
	findings []installercheck.Finding,
	failed bool,
	stdout,
	stderr io.Writer,
) int {
	if failed {
		for _, finding := range findings {
			if finding.Level == "PASS" {
				continue
			}

			fmt.Fprintf(
				stdout,
				"%s: %s\n",
				finding.Level,
				finding.Message,
			)
		}

		fmt.Fprintln(
			stderr,
			"FAIL: GjallarOS preflight failed",
		)

		return 1
	}

	fmt.Fprintln(stdout, "[GjallarOS] Preflight ✓")

	for _, finding := range findings {
		if finding.Level == installercheck.Warn {
			fmt.Fprintf(
				stdout,
				"WARN: %s\n",
				finding.Message,
			)
		}
	}

	return 0
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gjallarctl check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "GjallarOS repository root")
	timeout := flags.Duration("timeout", 5*time.Second, "maximum time for each safe external check")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "ERROR: check accepts no positional arguments")
		return 2
	}
	if *timeout <= 0 || *timeout > 30*time.Second {
		fmt.Fprintln(stderr, "ERROR: --timeout must be greater than zero and no more than 30s")
		return 2
	}

	root, err := installercheck.ResolveRepository(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report := installercheck.Check(ctx, root)
	for _, finding := range report.Findings {
		fmt.Fprintf(stdout, "%s: %s\n", finding.Level, finding.Message)
	}
	if report.Failed() {
		fmt.Fprintln(stderr, "ERROR: GjallarOS validation failed")
		return 1
	}
	return 0
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: gjallarctl usb {status|audit|policy|review|provision-key|allow-once|trust-permanent|keep-blocked|enroll-internal|accept-replacement|forget} [OPTIONS]")
	fmt.Fprintln(out, "       gjallarctl thunderbolt {status|review|allow-once|always-allow|forget} [--uid UID]")
	fmt.Fprintln(out, "Usage: gjallarctl check [--repo PATH] [--timeout DURATION]")
	fmt.Fprintln(out, "       gjallarctl preflight [--repo PATH]")
	oddccli.Usage(out)
	fmt.Fprintln(out, "       gjallarctl auth")
	fmt.Fprintln(out, "       gjallarctl rebuild [--repo PATH] [--host HOST] [-d|--debug] [-n|--no-cleanup] [--update] [--update-inputs] [--hardware-update [--stage main|staging] [--switch 0|1]] [NIXOS-REBUILD-ARGS...]")
	fmt.Fprintln(out, "       gjallarctl device-probe refresh [--output PATH]")
	fmt.Fprintln(out, "       gjallarctl fan {status|list|reconcile}")
	fmt.Fprintln(out, "       gjallarctl ai profile [--config PATH]")
	fmt.Fprintln(out, "       gjallarctl normalize keyboard --layout VALUE")
	fmt.Fprintln(out, "       gjallarctl preset {validate|get|list|bool} --config PATH [--key NAME]")
	fmt.Fprintln(out, "       gjallarctl tpm2 reenroll")
	fmt.Fprintln(out, "       gjallarctl luks {status|recovery-key|setup|remind [--open|--refresh]}")
	fmt.Fprintln(out, "       gjallarctl vpn status")
	fmt.Fprintln(out, "       gjallarctl secure-boot {status|setup|update-dbx} [--repo PATH]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Safe GjallarOS maintenance commands. Rebuild invokes sudo explicitly.")
}

func normalizeRebuildUserIntent(user *config.User, repo string) (bool, error) {
	if user == nil {
		return false, errors.New("user configuration is nil")
	}

	repo = strings.TrimSpace(repo)
	if repo == "" || !filepath.IsAbs(repo) {
		return false, fmt.Errorf("repository path must be absolute: %q", repo)
	}

	if strings.TrimSpace(user.DotfilesDir) != "" {
		return false, nil
	}

	user.DotfilesDir = filepath.Clean(repo)
	return true, nil
}

func runRebuild(args []string, stdout, stderr io.Writer) int {
	repo, host := "", ""
	debug, cleanup := false, true
	hardwareUpdate, stage, switchNow := false, "", ""
	update, updateInputs := false, false
	var rebuildArgs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printRebuildHelp(stdout)
			return 0
		case "--update":
			update = true
		case "--update-inputs":
			updateInputs = true
		case "--hardware-update":
			hardwareUpdate = true
		case "--stage":
			if i+1 >= len(args) || (args[i+1] != "main" && args[i+1] != "staging") {
				fmt.Fprintln(stderr, "ERROR: --stage requires main or staging")
				return 2
			}
			i++
			stage = args[i]
		case "--switch":
			if i+1 >= len(args) || (args[i+1] != "0" && args[i+1] != "1") {
				fmt.Fprintln(stderr, "ERROR: --switch requires 0 or 1")
				return 2
			}
			i++
			switchNow = args[i]
		case "--repo", "--host":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "ERROR: %s requires a value\n", args[i])
				return 2
			}
			i++
			if args[i-1] == "--repo" {
				repo = args[i]
			} else {
				host = args[i]
			}
		case "-d", "--debug":
			debug = true
		case "-n", "--no-cleanup":
			cleanup = false
		case "--":
			rebuildArgs = append(rebuildArgs, args[i+1:]...)
			i = len(args)
		default:
			rebuildArgs = append(rebuildArgs, args[i])
		}
	}
	resolvedRepo, err := installercheck.DiscoverRepository(repo)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: resolve GjallarOS repository: %v\n", err)
		return 2
	}
	repo = resolvedRepo

	// A root rebuild or installer run leaves root-owned files in the
	// checkout that this user's flake staging cannot read; hand them back.
	if err := checkoutowner.Ensure(context.Background(), repo, func(ctx context.Context, args ...string) error {
		if status := runPrivilegedCommand(ctx, stdout, stderr, args...); status != 0 {
			return fmt.Errorf("%s exited %d", args[0], status)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: %v\n", err)
		return 1
	}
	defer func() {
		if err := checkoutowner.Repair(repo); err != nil {
			fmt.Fprintf(stderr, "[GjallarOS] Warning: %v\n", err)
		}
	}()

	if update {
		if code := pullCheckout(repo, stdout, stderr); code != 0 {
			return code
		}
	}
	// Moves every flake input past what upstream tested; for development
	// checkouts, not endpoints.
	if updateInputs {
		if code := runCommand(context.Background(), stdout, stderr, "nix", "flake", "update", "--flake", repo); code != 0 {
			fmt.Fprintf(stderr, "[GjallarOS] Error: nix flake update failed (exit %d); nothing rebuilt\n", code)
			return code
		}
	}

	if (stage != "" || switchNow != "") && !hardwareUpdate {
		fmt.Fprintln(stderr, "ERROR: --stage and --switch require --hardware-update")
		return 2
	}
	// The oddc pin moves first, so the rebuild below already uses it.
	// --switch 0 stops after the pin; the next rebuild applies it.
	if hardwareUpdate {
		if code := oddccli.HardwareUpdate(repo, stage, stdout, stderr); code != 0 {
			fmt.Fprintf(stderr, "[GjallarOS] Error: oddc update failed (exit %d); nothing rebuilt\n", code)
			return code
		}
		if switchNow == "0" {
			fmt.Fprintln(stdout, "[GjallarOS] oddc pin updated, not switched (--switch 0). Apply with: rebuild")
			return 0
		}
	}

	_, err = repojson.CanonicalizeChangedTracked(
		context.Background(),
		repo,
	)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: canonicalize changed JSON: %v\n", err)
		return 1
	}

	// Rebuild consumes the same typed user intent as the installer and only
	// refreshes direct user-owned generated-state assignments. Hardware, AI
	// discovery and installer-owned facts remain untouched.
	configPath := filepath.Join(repo, "user.config.json")
	userConfig, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: load user configuration: %v\n", err)
		return 1
	}

	intentChanged, err := normalizeRebuildUserIntent(&userConfig, repo)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: normalize user configuration: %v\n", err)
		return 1
	}
	if intentChanged {
		if err := config.WriteAtomic(configPath, userConfig); err != nil {
			fmt.Fprintf(stderr, "[GjallarOS] Error: persist normalized user configuration: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "[GjallarOS] Migrated missing dotfilesDir to the discovered repository path.")
	}

	if host == "" {
		host = strings.TrimSpace(userConfig.Hostname)
		if host == "" {
			fmt.Fprintln(
				stderr,
				"[GjallarOS] Error: user configuration has no hostname",
			)
			return 1
		}
	}

	messagesPath := filepath.Join(
		repo,
		"system/tools/commands/rebuild-messages.json",
	)
	messages, err := loadRebuildMessages(messagesPath, host)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: %v\n", err)
		return 1
	}

	settingsPath := filepath.Join(repo, "generated", "state.nix")
	if err := nixrender.SyncUserIntent(settingsPath, userConfig); err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: sync routed user intent: %v\n", err)
		return 1
	}

	// The device's ODDC answer follows the commit flake.lock pins, so
	// `nix flake update oddc` and a rebuild take a new catalog revision.
	// A failed fetch stops the rebuild: the old answer no longer matches the
	// pinned catalogue, and ODDC's revision check would reject it anyway.
	overrideRev, err := oddcOverrideRevision(rebuildArgs)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: %v\n", err)
		return 2
	}
	var before, pinned string
	if overrideRev != "" {
		before, pinned, err = deviceprofile.RefreshAnswerAt(repo, overrideRev)
	} else {
		before, pinned, err = deviceprofile.RefreshAnswer(repo)
	}
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: refresh ODDC answer: %v\n", err)
		return 1
	}
	if before != pinned && before != "" {
		fmt.Fprintf(stdout, "[GjallarOS] ODDC answer updated: %s -> %s\n", before, pinned)
	}

	if status := reportRebuildPreflight(
		repo,
		stdout,
		stderr,
	); status != 0 {
		return status
	}

	// Managed JODS endpoints deliberately remove the desktop user from
	// wheel. Rebuilds on those systems therefore cross the explicit root
	// authentication boundary instead of attempting sudo.
	managedDevice := userConfig.EndpointManagedDevice

	// JODS moves the pin on managed endpoints; elsewhere the user decides.
	if !hardwareUpdate && !managedDevice {
		oddccli.CheckUpdate(repo, stdout)
	}

	if managedDevice && os.Geteuid() != 0 {
		executable, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "[GjallarOS] Error: resolve gjallarctl executable: %v\n", err)
			return 1
		}

		home, _ := os.UserHomeDir()

		rootArgs := []string{
			"env",
			"GJALLAR_REBUILD_CALLER_HOME=" + home,
			executable,
			"rebuild",
			"--repo",
			repo,
			"--host",
			host,
		}

		if debug {
			rootArgs = append(rootArgs, "--debug")
		}
		if !cleanup {
			rootArgs = append(rootArgs, "--no-cleanup")
		}

		rootArgs = append(rootArgs, rebuildArgs...)

		fmt.Fprintln(
			stdout,
			"[GjallarOS] Managed endpoint: authenticating as root for rebuild.",
		)

		return runCommand(
			context.Background(),
			stdout,
			stderr,
			"su",
			"-c",
			shellJoin(rootArgs),
			"root",
		)
	}
	if os.Geteuid() != 0 {
		if status := runPrivilegeAuthentication(
			context.Background(),
			stderr,
		); status != 0 {
			return status
		}
	}

	started := time.Now()
	rebuildHome := os.Getenv("GJALLAR_REBUILD_CALLER_HOME")
	if rebuildHome == "" {
		rebuildHome, _ = os.UserHomeDir()
	}
	// Evaluate a staged copy: path: on the checkout copies .git (gigabytes of
	// history) and ignored scratch such as .vm into the store on every rebuild.
	source, err := flakesource.Stage(repo, "")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	defer source.Close()
	plan := newRebuildPlan(source.Dir, host, rebuildHome, debug, rebuildArgs)

	var status int
	if debug {
		fmt.Fprintf(stdout, "Rebuilding NixOS for %s\n[GjallarOS] Flake: %s#%s\n\n", host, repo, host)
		status = plan.run(context.Background(), stdout, stderr)
	} else {
		status = runRebuildQuiet(stdout, stderr, host, messages, started, plan)
	}
	if status == 0 {
		if err := installercheck.RememberRepository(repo); err != nil {
			fmt.Fprintf(
				stderr,
				"WARN: remember GjallarOS repository: %v\n",
				err,
			)
		}
	}

	if status == 0 && cleanup {
		if cleanupStatus := runCleanupOld(nil, stdout, stderr); cleanupStatus != 0 {
			fmt.Fprintln(
				stderr,
				"WARN: rebuild switched successfully, but old-generation cleanup failed; run cleanup-old-generations later.",
			)
		}
	}
	elapsed := int(time.Since(started).Round(time.Second) / time.Second)
	if status == 130 {
		fmt.Fprintf(stderr, "\n⏱ %02d:%02d  ✗ Rebuild cancelled.\n", elapsed/60, elapsed%60)
	} else if status == 0 {
		fmt.Fprintf(stdout, "\n⏱ %02d:%02d  ✓ Rebuild completed successfully.\n", elapsed/60, elapsed%60)
	} else {
		fmt.Fprintf(stderr, "\n⏱ %02d:%02d  ✗ Rebuild failed.\n", elapsed/60, elapsed%60)
	}
	return status
}

// oddcOverrideRevision returns the commit a `--override-input oddc <ref>`
// in the nixos-rebuild arguments points at, or "" without such an override.
// The ODDC answer is fetched at that commit, so a ref that names no commit
// (a branch, a local path) is refused instead of building code and answer
// from different revisions.
func oddcOverrideRevision(rebuildArgs []string) (string, error) {
	rev := ""
	for i := 0; i+2 < len(rebuildArgs); i++ {
		if rebuildArgs[i] != "--override-input" || rebuildArgs[i+1] != "oddc" {
			continue
		}
		ref := rebuildArgs[i+2]
		candidate := ref
		if base, query, ok := strings.Cut(ref, "?"); ok {
			candidate = base
			for _, field := range strings.Split(query, "&") {
				if value, ok := strings.CutPrefix(field, "rev="); ok {
					candidate = value
				}
			}
		}
		candidate = candidate[strings.LastIndexAny(candidate, "/:")+1:]
		if len(candidate) != 40 || strings.Trim(candidate, "0123456789abcdef") != "" {
			return "", fmt.Errorf("--override-input oddc %s names no commit; pin it to one (github:JadeOpenServices/oddc/<40-hex commit>) so the ODDC answer matches", ref)
		}
		rev = candidate
	}
	return rev, nil
}

// rebuildPlan is a rebuild in three privileged steps. `nixos-rebuild switch
// --flake` evaluates and builds in one nix process, which keeps the
// evaluator's heap (about 4 GiB for this configuration) until the last
// compile ends; with a local C++ build on a 4 GiB machine that ran out of
// memory and took the terminal with it (book12 VM, 2026-10-10). Here the
// evaluation exits before anything builds, and nixos-rebuild only switches
// to the built path.
type rebuildPlan struct {
	eval     []string // prints the system's .drv
	build    []string // the .drv's output is appended
	activate []string // --store-path and the built system are appended
}

// rebuildSwitchFlags are the nixos-rebuild arguments that nix itself does
// not know, with the number of values each takes. Everything else is a nix
// option and goes to the evaluation and to the build.
var rebuildSwitchFlags = map[string]int{
	"--install-bootloader": 0,
	"--install-grub":       0,
	"--profile-name":       1,
	"-p":                   1,
	"--specialisation":     1,
	"-c":                   1,
	"--sudo":               0,
	"--use-remote-sudo":    0,
	"--ask-sudo-password":  0,
	"--no-reexec":          0,
	"--fast":               0,
	"--no-ssh-tty":         0,
	"--target-host":        1,
	"--build-host":         1,
	"--no-build-nix":       0,
	"--diff":               0,
	"--json":               0,
}

func newRebuildPlan(
	repo, host, rebuildHome string,
	debug bool,
	rebuildArgs []string,
) rebuildPlan {
	var nixArgs, switchArgs []string
	for i := 0; i < len(rebuildArgs); i++ {
		values, ok := rebuildSwitchFlags[rebuildArgs[i]]
		if !ok {
			nixArgs = append(nixArgs, rebuildArgs[i])
			continue
		}
		end := min(i+1+values, len(rebuildArgs))
		switchArgs = append(switchArgs, rebuildArgs[i:end]...)
		i = end - 1
	}
	if debug {
		nixArgs = append([]string{"--show-trace"}, nixArgs...)
	}

	nix := []string{"nix", "--extra-experimental-features", "nix-command flakes"}
	plan := rebuildPlan{
		eval: slices.Concat(nix, []string{
			"eval",
			"--raw",
			"path:" + repo + "#nixosConfigurations." + strconv.Quote(host) + ".config.system.build.toplevel.drvPath",
		}, nixArgs),
		build: slices.Concat(nix, []string{"build", "--no-link", "--print-out-paths"}, binarycache.Args(repo), nixArgs),
		// Without --no-reexec, nixos-rebuild 26.05 first builds a newer copy
		// of itself from <nixpkgs/nixos> even for --store-path, which fails
		// on a flake system and has nothing to offer for a prebuilt one.
		activate: slices.Concat([]string{"nixos-rebuild", "switch", "--no-reexec"}, switchArgs),
	}

	// Pure evaluation is the default. The only current impure input is the
	// explicitly discovered Noctalia palette used to snapshot live colors into
	// build-time Stylix targets. Add --impure exactly once when that input exists.
	if rebuildHome != "" {
		palette := filepath.Join(
			rebuildHome,
			".local",
			"state",
			"noctalia",
			"stylix-override.json",
		)
		if info, err := os.Stat(palette); err == nil && info.Mode().IsRegular() {
			plan.eval = slices.Concat(
				[]string{"env", "GJALLAR_NOCTALIA_PALETTE=" + palette},
				plan.eval,
				[]string{"--impure"},
			)
		}
	}
	return plan
}

func (p rebuildPlan) run(ctx context.Context, stdout, stderr io.Writer) int {
	var drv bytes.Buffer
	if status := runPrivilegedCommand(ctx, &drv, stderr, p.eval...); status != 0 {
		return status
	}
	drvPath := strings.TrimSpace(drv.String())
	if !strings.HasPrefix(drvPath, "/nix/store/") || !strings.HasSuffix(drvPath, ".drv") {
		fmt.Fprintf(stderr, "ERROR: evaluation printed no derivation: %q\n", drvPath)
		return 1
	}

	var built bytes.Buffer
	if status := runPrivilegedCommand(ctx, &built, stderr, slices.Concat(p.build, []string{drvPath + "^out"})...); status != 0 {
		return status
	}
	system := strings.TrimSpace(built.String())
	if !strings.HasPrefix(system, "/nix/store/") || strings.ContainsAny(system, " \n") {
		fmt.Fprintf(stderr, "ERROR: build printed no single system path: %q\n", system)
		return 1
	}

	return runPrivilegedCommand(ctx, stdout, stderr, slices.Concat(p.activate, []string{"--store-path", system})...)
}

// pullCheckout brings the system's checkout up to date before the rebuild:
// every install tracks the same upstream. The pull is fast-forward only, so
// local commits or edits stop it with git's own message and nothing is
// rebuilt.
func pullCheckout(repo string, stdout, stderr io.Writer) int {
	before := gitHead(repo)
	if status := runCommand(context.Background(), stdout, stderr, "git", "-C", repo, "pull", "--ff-only"); status != 0 {
		fmt.Fprintf(stderr, "[GjallarOS] Error: git pull --ff-only in %s failed; nothing rebuilt\n", repo)
		return status
	}
	if after := gitHead(repo); before == after {
		fmt.Fprintf(stdout, "[GjallarOS] %s already at %s\n", repo, after)
	} else {
		fmt.Fprintf(stdout, "[GjallarOS] %s %s -> %s\n", repo, before, after)
	}
	return 0
}

func printRebuildHelp(out io.Writer) {
	fmt.Fprint(out, `Usage: rebuild [OPTIONS] [NIXOS-REBUILD-ARGS...]

Apply the GjallarOS configuration in the system checkout.

  --update              Pull the checkout from upstream first (fast-forward
                        only; local commits or edits stop it).
  --update-inputs       Move every flake input to its newest version first.
                        For development checkouts, not normal installs.
  --hardware-update     Move the ODDC hardware catalogue pin first.
    --stage main|staging  ODDC branch to follow.
    --switch 0|1          0 moves the pin without rebuilding.
  --repo PATH           Use this checkout instead of the system one.
  --host HOST           Build this host instead of the configured one.
  -d, --debug           Show the full nixos-rebuild output.
  -n, --no-cleanup      Keep old system generations.
  -h, --help            Show this help.

Arguments after -- go to nixos-rebuild. --override-input oddc must name a
commit (github:JadeOpenServices/oddc/<commit>); the ODDC answer for this
machine is fetched at that commit too.
`)
}

func gitHead(repo string) string {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

const (
	systemProfilePath          = "/nix/var/nix/profiles/system"
	rebuildGenerationRetention = 5
)

func systemGenerationCleanupArgs(keep int) []string {
	return []string{
		"nix-env",
		"--profile",
		systemProfilePath,
		"--delete-generations",
		fmt.Sprintf("+%d", keep),
	}
}

func parseCleanupKeep(raw string) (int, error) {
	keep, err := strconv.Atoi(raw)
	if err != nil || keep < 1 {
		return 0, fmt.Errorf("keep count must be a positive integer")
	}
	return keep, nil
}

func trimSystemGenerations(keep int, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "[cleanup] Keeping the latest %d system generations.\n", keep)
	return runPrivilegedCommand(
		context.Background(),
		stdout,
		stderr,
		systemGenerationCleanupArgs(keep)...,
	)
}

func runCleanup(args []string, stdout, stderr io.Writer) int {
	keepRaw := strconv.Itoa(rebuildGenerationRetention)
	for i := 0; i < len(args); i++ {
		if (args[i] == "-k" || args[i] == "--keep") && i+1 < len(args) {
			i++
			keepRaw = args[i]
		} else if args[i] == "-h" || args[i] == "--help" {
			fmt.Fprintln(stdout, "Usage: cleanup [--keep N]")
			return 0
		} else {
			fmt.Fprintln(stderr, "Usage: cleanup [--keep N]")
			return 2
		}
	}

	keep, err := parseCleanupKeep(keepRaw)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}

	if status := trimSystemGenerations(keep, stdout, stderr); status != 0 {
		return status
	}

	// Explicit cleanup is the place for store GC. Normal rebuilds only trim
	// rollback history; scheduled system nix.gc owns periodic collection.
	fmt.Fprintln(stdout, "[cleanup] Collecting unreachable Nix store paths.")
	return runPrivilegedCommand(
		context.Background(),
		stdout,
		stderr,
		"nix-store",
		"--gc",
	)
}

func runCleanupOld(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: cleanup-old-generations")
		return 2
	}

	return trimSystemGenerations(
		rebuildGenerationRetention,
		stdout,
		stderr,
	)
}

func runThermalStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return 2
	}
	for _, item := range []struct {
		title, name string
		args        []string
	}{{"uptime", "uptime", nil}, {"sensors", "sensors", nil}, {"power profile", "powerprofilesctl", []string{"get"}}} {
		fmt.Fprintf(stdout, "\n== %s ==\n", item.title)
		_ = runCommand(context.Background(), stdout, stderr, item.name, item.args...)
	}
	fmt.Fprintln(stdout, "\n== top CPU ==")
	data, err := exec.Command("ps", "-eo", "pid,user,comm,%cpu,%mem", "--sort=-%cpu").Output()
	if err != nil {
		fmt.Fprintf(stderr, "ps: %v\n", err)
		return 1
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for line := 0; line < 20 && scanner.Scan(); line++ {
		fmt.Fprintln(stdout, scanner.Text())
	}
	return 0
}

func runThermalTest(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "Usage: thermal-test <off|on|status> <process-pattern>")
		return 2
	}
	data, _ := exec.Command("pgrep", "-f", args[1]).Output()
	pids := strings.Fields(string(data))
	if len(pids) == 0 {
		fmt.Fprintln(stdout, "No matching processes.")
		return 0
	}
	switch args[0] {
	case "off", "on":
		sig := syscall.SIGSTOP
		if args[0] == "on" {
			sig = syscall.SIGCONT
		}
		for _, pid := range pids {
			var n int
			fmt.Sscanf(pid, "%d", &n)
			_ = syscall.Kill(n, sig)
		}
		return 0
	case "status":
		return runCommand(context.Background(), stdout, stderr, "ps", "-o", "pid,stat,comm,args=", "-p", strings.Join(pids, ","))
	default:
		fmt.Fprintf(stderr, "Unknown action: %s\n", args[0])
		return 2
	}
}

const tpm2ReenrollTool = "gjallar-tpm2-reenroll"

// runTPM2Reenroll hands the terminal to the re-enrollment tool, which asks
// for the LUKS passphrase; runCommand starts children in their own process
// group, where reading the terminal stops them.
func runTPM2Reenroll(args []string, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "reenroll" {
		fmt.Fprintln(stderr, "Usage: gjallarctl tpm2 reenroll")
		return 2
	}
	return execRootTool(tpm2ReenrollTool, "TPM2 unlock is not enabled on this system", stderr)
}

// execRootTool replaces gjallarctl with tool, run as root.
func execRootTool(tool, missing string, stderr io.Writer) int {
	if _, err := exec.LookPath(tool); err != nil {
		fmt.Fprintf(stderr, "ERROR: %s not found; %s\n", tool, missing)
		return 1
	}

	argv := rootToolArgv(tool, os.Geteuid())
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	err = syscall.Exec(path, argv, os.Environ())
	fmt.Fprintf(stderr, "ERROR: %s: %v\n", argv[0], err)
	return 1
}

func rootToolArgv(tool string, euid int) []string {
	if euid == 0 {
		return []string{tool}
	}
	// -k: ask now and leave no timestamp behind.
	return []string{"sudo", "-k", tool}
}

func runHelpme(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--text" {
		return 2
	}
	text := "GjallarOS tools\n\n  rebuild            Apply the current NixOS configuration.\n  rebuild --update   Pull the system checkout and rebuild.\n  cleanup            Remove old generations and collect garbage.\n  thermal-status     Show temperatures and power state.\n  thermal-test       Pause/resume processes for troubleshooting.\n  check-installer    Check installer configuration.\n  gjallar-preflight  Run fast repository wiring checks.\n"
	if len(args) == 1 || os.Getenv("DISPLAY")+os.Getenv("WAYLAND_DISPLAY") == "" {
		fmt.Fprint(stdout, text)
		return 0
	}
	if _, err := exec.LookPath("yad"); err != nil {
		fmt.Fprint(stdout, text)
		return 0
	}
	return runCommand(context.Background(), stdout, stderr, "yad",
		"--list", "--title=GjallarOS tools", "--width=900", "--height=520", "--center", "--button=Close:0",
		"--column=Command", "--column=Description",
		"rebuild", "Apply the current NixOS configuration.",
		"rebuild --update", "Pull the system checkout and rebuild.",
		"cleanup", "Remove old generations and collect garbage.",
		"thermal-status", "Show temperatures and power state.",
		"thermal-test", "Pause/resume processes for troubleshooting.",
		"check-installer", "Check installer configuration.",
		"gjallar-preflight", "Run fast repository wiring checks.")
}

func runRebuildQuiet(stdout, stderr io.Writer, host string, messages []string, started time.Time, plan rebuildPlan) int {
	log, err := os.CreateTemp("", "gjallar-rebuild-*.log")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: create rebuild log: %v\n", err)
		return 1
	}
	keepLog := false
	defer func() {
		_ = log.Close()
		if !keepLog {
			_ = os.Remove(log.Name())
		}
	}()

	terminal, ok := stdout.(*os.File)
	interactive := ok && isTerminal(terminal)
	stop := make(chan struct{})
	var ui sync.WaitGroup
	if interactive {
		fmt.Fprint(stdout, rebuildUIEnter)
		ui.Add(1)
		go func() {
			defer ui.Done()
			index, shownAt := rand.IntN(len(messages)), 0
			ticker := time.NewTicker(time.Second / 4)
			defer ticker.Stop()
			for {
				elapsed := int(time.Since(started) / time.Second)
				width := terminalWidth(terminal)
				if elapsed-shownAt >= rebuildMessageSeconds(messages[index], width) {
					if len(messages) > 1 {
						next := index
						for next == index {
							next = rand.IntN(len(messages))
						}
						index = next
					}
					shownAt = elapsed
				}
				fmt.Fprint(stdout, rebuildFrame(host, messages[index], elapsed, elapsed-shownAt, width))
				select {
				case <-stop:
					return
				case <-ticker.C:
				}
			}
		}()
	} else {
		fmt.Fprintf(stdout, "Rebuilding NixOS for %s\n", host)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := plan.run(ctx, log, log)
	cancelled := ctx.Err() != nil
	cancel()
	if interactive {
		close(stop)
		ui.Wait()
		fmt.Fprint(stdout, rebuildUILeave)
	}

	elapsed := time.Since(started)
	localBuilds := rebuildLocalDerivations(log)
	retriedError := rebuildNetworkError(log, true)
	networkError := rebuildNetworkError(log, false)
	if elapsed >= time.Minute {
		keepLog = true
		fmt.Fprintf(
			stdout,
			"[rebuild] Slow rebuild: %s elapsed; %d local derivation(s) observed.\n",
			elapsed.Round(time.Second),
			len(localBuilds),
		)
		if len(localBuilds) != 0 {
			fmt.Fprintf(
				stdout,
				"[rebuild] Recent local builds: %s\n",
				strings.Join(lastStrings(localBuilds, 8), ", "),
			)
		}
		fmt.Fprintf(stdout, "[rebuild] Detailed log retained: %s\n", log.Name())
		if status == 0 && retriedError != "" {
			fmt.Fprintf(stdout, "[rebuild] Downloads failed and were retried: %s\n", retriedError)
		}
	}
	if status != 0 {
		keepLog = true
		if _, err := log.Seek(0, io.SeekStart); err == nil {
			_, _ = io.Copy(stderr, log)
		}
		fmt.Fprintf(stderr, "[rebuild] Detailed log retained: %s\n", log.Name())
		if networkError != "" && !cancelled {
			fmt.Fprintf(stderr, "[rebuild] No network: %s\n", networkError)
			fmt.Fprintln(stderr, "[rebuild] Check the connection (Wi-Fi, DHCP lease, VPN, router) and run rebuild again.")
		}
	}
	return status
}

// rebuildNetworkErrorMarkers are curl and git messages for a host that
// cannot be resolved or reached. A broken DHCP lease looked like a hung or
// crashed rebuild (DDR3 box, 2026-10-09), because nix only retries quietly.
var rebuildNetworkErrorMarkers = []string{
	"Could not resolve host",
	"Couldn't resolve host",
	"Could not connect to server",
	"Couldn't connect to server",
	"Timeout was reached",
	"Network is unreachable",
	"Temporary failure in name resolution",
}

// rebuildNetworkError returns the first network failure in the log, or "".
// Nix logs a failed download it will retry as "warning:"; such a line only
// counts with retries set. Otherwise one hiccup of a substituter blamed the
// network for a build that failed or was cancelled for another reason (fw13
// VM, 2026-10-09).
func rebuildNetworkError(log io.ReadSeeker, retries bool) string {
	if _, err := log.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	scanner := bufio.NewScanner(log)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !retries && strings.HasPrefix(line, "warning:") {
			continue
		}
		for _, marker := range rebuildNetworkErrorMarkers {
			if strings.Contains(line, marker) {
				return line
			}
		}
	}
	return ""
}

func rebuildLocalDerivations(log io.ReadSeeker) []string {
	if _, err := log.Seek(0, io.SeekStart); err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	var names []string
	scanner := bufio.NewScanner(log)
	for scanner.Scan() {
		line := scanner.Text()
		marker := "building '"
		start := strings.Index(line, marker)
		if start < 0 {
			continue
		}
		value := line[start+len(marker):]
		end := strings.IndexByte(value, '\'')
		if end < 0 {
			continue
		}
		path := value[:end]
		if !strings.HasSuffix(path, ".drv") {
			continue
		}

		name := strings.TrimSuffix(filepath.Base(path), ".drv")
		if dash := strings.IndexByte(name, '-'); dash >= 0 && dash+1 < len(name) {
			name = name[dash+1:]
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func lastStrings(values []string, count int) []string {
	if count <= 0 || len(values) <= count {
		return values
	}
	return values[len(values)-count:]
}

func runPrivilegedCommand(
	ctx context.Context,
	stdout, stderr io.Writer,
	args ...string,
) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "ERROR: empty privileged command")
		return 2
	}

	if os.Geteuid() == 0 {
		return runCommand(
			ctx,
			stdout,
			stderr,
			args[0],
			args[1:]...,
		)
	}

	if status := runPrivilegeAuthentication(ctx, stderr); status != 0 {
		return status
	}

	sudoArgs := append([]string{"-n"}, args...)

	return runCommand(
		ctx,
		stdout,
		stderr,
		"sudo",
		sudoArgs...,
	)
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))

	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}

	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}

	return "'" + strings.ReplaceAll(
		value,
		"'",
		"'\"'\"'",
	) + "'"
}

func environmentWithOverride(key, value string) []string {
	prefix := key + "="
	current := os.Environ()
	env := make([]string, 0, len(current)+1)

	for _, entry := range current {
		if !strings.HasPrefix(entry, prefix) {
			env = append(env, entry)
		}
	}

	return append(env, prefix+value)
}

// systemHelperDir holds the helpers of the running system. The sudoers PAM
// split names this system's gjallar-sudo-auth, so it is used even when
// gjallarctl itself runs from a dev checkout (go run, nix build result).
var systemHelperDir = "/run/current-system/sw/bin"

// helperExecutable resolves a gjallarctl helper script: the running system's
// copy first, then the one next to this binary (installer media, systems
// without gjallarctl installed).
func helperExecutable(name string) (string, error) {
	if helper, err := resolveExecutable(filepath.Join(systemHelperDir, name)); err == nil {
		return helper, nil
	}

	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve gjallarctl executable: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve gjallarctl executable symlink: %w", err)
	}

	helper, err := resolveExecutable(filepath.Join(filepath.Dir(resolved), name))
	if err != nil {
		return "", fmt.Errorf(
			"%w (not in %s either; is gjallarctl installed on this system?)",
			err,
			systemHelperDir,
		)
	}
	return helper, nil
}

func resolveExecutable(path string) (string, error) {
	name := filepath.Base(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("%s is not executable", resolved)
	}

	return resolved, nil
}

func privilegeFingerprintCommand(
	ctx context.Context,
	tty *os.File,
	authHelper string,
) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sudo", authHelper)

	// The command-specific sudo policy selects a fingerprint-only PAM service.
	// Keep stdin detached so this phase itself never owns shell input.
	cmd.Stdin = nil
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	return cmd
}

func privilegePasswordCommand(
	ctx context.Context,
	tty *os.File,
	askpass string,
	authHelper string,
) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sudo", "-A", authHelper)

	// -A selects the helper's password-only PAM service. The password is
	// collected by systemd-ask-password and passed directly to sudo.
	cmd.Stdin = nil
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.Env = environmentWithOverride("SUDO_ASKPASS", askpass)

	// The askpass helper prompts on the terminal and turns echo off, which a
	// background process group cannot do. Make sudo the foreground job. With
	// Foreground, Ctty is a descriptor in this process, not in the child:
	// fd 1 was the pipe in `sudo bootctl status | grep` and failed with
	// ENOTTY (real HP, 2026-10-09).
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true,
		Foreground: true,
		Ctty:       int(tty.Fd()),
	}

	return cmd
}

// reclaimTerminalForeground makes this process group the terminal's
// foreground job again after a foreground child exits.
func reclaimTerminalForeground(tty *os.File) {
	pgrp := int32(syscall.Getpgrp())
	_, _, _ = syscall.Syscall(
		syscall.SYS_IOCTL,
		tty.Fd(),
		uintptr(syscall.TIOCSPGRP),
		uintptr(unsafe.Pointer(&pgrp)),
	)
}

func authenticationCommandStatus(
	ctx context.Context,
	cmd *exec.Cmd,
	stderr io.Writer,
	label string,
) int {
	err := cmd.Run()
	if err == nil {
		return 0
	}

	if ctx.Err() != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		return 130
	}

	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}

	fmt.Fprintf(stderr, "ERROR: %s: %v\n", label, err)
	return 1
}

// runAuth refreshes the sudo timestamp through the GjallarOS fingerprint-first
// flow so a following plain sudo does not start its own PAM conversation.
func runAuth(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl auth")
		return 2
	}
	if os.Geteuid() == 0 {
		return 0
	}

	cached := exec.Command("sudo", "-n", "-v")
	cached.Stdin, cached.Stdout, cached.Stderr = nil, nil, nil
	if cached.Run() == nil {
		return 0
	}

	return authenticatePrivilege(context.Background(), stderr)
}

// A gjallarctl operation always asks for authentication and leaves no sudo
// timestamp behind; plain sudo keeps the 5 minute default. The follow-up
// commands run with sudo -n on the timestamp gjallar-sudo-auth creates, so it
// is dropped on exit, not avoided. A rebuild can outlast those 5 minutes, so
// the timestamp is refreshed while the operation runs; without that, the
// generation cleanup at the end asked again.
var (
	privilegeSessionStarted    bool
	invalidateSudoTimestamp    = func() { _ = exec.Command("sudo", "-k").Run() }
	refreshSudoTimestamp       = func() bool { return exec.Command("sudo", "-n", "-v").Run() == nil }
	privilegeKeepaliveInterval = time.Minute
	stopPrivilegeKeepalive     func()
)

func beginPrivilegeSession() {
	if privilegeSessionStarted {
		return
	}
	privilegeSessionStarted = true
	invalidateSudoTimestamp()
}

func endPrivilegeSession() {
	if stopPrivilegeKeepalive != nil {
		stopPrivilegeKeepalive()
		stopPrivilegeKeepalive = nil
	}
	if privilegeSessionStarted {
		invalidateSudoTimestamp()
		privilegeSessionStarted = false
	}
}

// keepPrivilegeAlive refreshes the session timestamp until the session ends.
// sudo -n -v never prompts: once the timestamp is gone, it fails quietly.
func keepPrivilegeAlive() {
	if !privilegeSessionStarted || stopPrivilegeKeepalive != nil {
		return
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(privilegeKeepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = refreshSudoTimestamp()
			}
		}
	}()
	// Wait for a refresh in flight, so it cannot land after sudo -k.
	stopPrivilegeKeepalive = func() { close(done); <-stopped }
}

func runPrivilegeAuthentication(ctx context.Context, stderr io.Writer) int {
	beginPrivilegeSession()
	// Later steps of the same operation reuse the timestamp without a word;
	// only a lost one asks again.
	if stopPrivilegeKeepalive != nil && refreshSudoTimestamp() {
		return 0
	}
	status := authenticatePrivilege(ctx, stderr)
	if status == 0 {
		keepPrivilegeAlive()
	}
	return status
}

func authenticatePrivilege(ctx context.Context, stderr io.Writer) int {
	askpass, err := helperExecutable("gjallar-sudo-askpass")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: privilege authentication helper: %v\n", err)
		return 1
	}

	authHelper, err := helperExecutable("gjallar-sudo-auth")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: privilege authentication helper: %v\n", err)
		return 1
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(
			stderr,
			"ERROR: privilege authentication requires a controlling terminal: %v\n",
			err,
		)
		return 1
	}
	defer tty.Close()

	fmt.Fprintln(tty)
	fmt.Fprintln(
		tty,
		"[GjallarOS] Authenticate for privileged operation.",
	)

	if probe := probeFingerprint(ctx); probe.State != fingerprintReady {
		fmt.Fprintln(tty, "[GjallarOS] "+probe.Message())
	} else {
		fingerprint := privilegeFingerprintCommand(ctx, tty, authHelper)
		fingerprintStatus := authenticationCommandStatus(
			ctx,
			fingerprint,
			stderr,
			"fingerprint authentication",
		)
		if fingerprintStatus == 0 {
			return reportAuthentication(tty, 0)
		}
		if fingerprintStatus == 130 {
			return 130
		}

		fmt.Fprintln(
			tty,
			"[GjallarOS] Fingerprint did not match, timed out or was skipped; using password.",
		)
	}

	password := privilegePasswordCommand(
		ctx,
		tty,
		askpass,
		authHelper,
	)

	// Handing the terminal to sudo and taking it back both change the
	// foreground job; SIGTTOU would stop us while we are in the background.
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	defer reclaimTerminalForeground(tty)

	return reportAuthentication(tty, authenticationCommandStatus(
		ctx,
		password,
		stderr,
		"password authentication",
	))
}

// reportAuthentication closes the prompt with a result line. Without it the
// answered prompt stayed the last line on screen while a quiet rebuild ran,
// looked like it was still waiting, and a password typed into it went to the
// shell once the rebuild ended (e2e-fw13, 2026-10-11). Ctrl-C already shows
// on the terminal.
func reportAuthentication(tty io.Writer, status int) int {
	switch status {
	case 0:
		fmt.Fprintln(tty, "[GjallarOS] Authenticated.")
	case 130:
	default:
		fmt.Fprintln(tty, "[GjallarOS] Authentication failed.")
	}
	return status
}

func runCommand(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) int {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = stdout, stderr, os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if ctx.Err() != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		return 130
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	fmt.Fprintf(stderr, "ERROR: %s: %v\n", name, err)
	return 1
}

func loadRebuildMessages(path, host string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rebuild messages: %w", err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse rebuild messages: %w", err)
	}
	var messages []string
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case string:
			prefix := "Rebuilding NixOS for $runtime_host... "
			if strings.HasPrefix(value, prefix) {
				messages = append(messages, strings.ReplaceAll(strings.TrimPrefix(value, prefix), "$runtime_host", host))
			}
		case []any:
			for _, item := range value {
				visit(item)
			}
		case map[string]any:
			for _, item := range value {
				visit(item)
			}
		}
	}
	visit(document)
	if len(messages) == 0 {
		return nil, fmt.Errorf("no rebuild messages found in %s", path)
	}
	return messages, nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func terminalWidth(file *os.File) int {
	type winsize struct{ Row, Col, Xpixel, Ypixel uint16 }
	var size winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 || size.Col < 2 {
		return 80
	}
	return int(size.Col)
}

func rebuildFrame(host, message string, elapsedSeconds, messageSeconds, width int) string {
	if width < 2 {
		width = 80
	}
	prefix := fmt.Sprintf("⏱ [%02d:%02d]  ", elapsedSeconds/60, elapsedSeconds%60)
	available := width - terminalCellWidth(prefix) - 2
	if available < 1 {
		available = 1
	}
	overflow := terminalCellWidth(message) - available
	if overflow < 0 {
		overflow = 0
	}
	offset := messageSeconds * 4
	if offset > overflow {
		offset = overflow
	}
	status := prefix + terminalCellSlice(message, offset, available)
	return "\x1b[H\x1b[2J" + terminalCellSlice("Rebuilding NixOS for "+host, 0, width-1) + "\n" + status
}

func rebuildMessageSeconds(message string, width int) int {
	available := width - terminalCellWidth("⏱ [00:00]  ") - 2
	if available < 1 {
		available = 1
	}
	overflow := terminalCellWidth(message) - available
	if overflow <= 0 {
		return 5
	}
	seconds := (overflow+3)/4 + 2
	if seconds < 5 {
		return 5
	}
	return seconds
}

func terminalCellWidth(value string) int {
	width := 0
	for _, r := range value {
		width += terminalRuneWidth(r)
	}
	return width
}

func terminalCellSlice(value string, skip, limit int) string {
	if limit <= 0 {
		return ""
	}
	var out strings.Builder
	position, used := 0, 0
	for _, r := range value {
		width := terminalRuneWidth(r)
		if position+width <= skip {
			position += width
			continue
		}
		if position < skip {
			position += width
			continue
		}
		if used+width > limit {
			break
		}
		out.WriteRune(r)
		used += width
		position += width
	}
	return out.String()
}

func terminalRuneWidth(r rune) int {
	if r == utf8.RuneError || r == 0 || r == '\n' || r == '\r' || unicode.IsControl(r) {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == '\u200d' || r == '\ufe0f' {
		return 0
	}
	if r == 0x23f1 || r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd)) {
		return 2
	}
	return 1
}

type hyprMonitor struct {
	Name        string  `json:"name"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	RefreshRate float64 `json:"refreshRate"`
	X           int     `json:"x"`
	Y           int     `json:"y"`
	Scale       float64 `json:"scale"`
}

func internalDRMConnector(sysRoot string) (string, error) {
	paths, err := filepath.Glob(filepath.Join(sysRoot, "class", "drm", "card*-eDP-*"))
	if err != nil {
		return "", err
	}

	for _, path := range paths {
		status, err := os.ReadFile(filepath.Join(path, "status"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(status)) == "connected" {
			name := filepath.Base(path)
			if i := strings.Index(name, "-eDP-"); i >= 0 {
				return name[i+1:], nil
			}
		}
	}

	return "", fmt.Errorf("no connected internal eDP connector found")
}

func hyprlandMonitors() ([]hyprMonitor, error) {
	out, err := exec.Command("hyprctl", "monitors", "-j").Output()
	if err != nil {
		return nil, fmt.Errorf("hyprctl monitors: %w", err)
	}

	var monitors []hyprMonitor
	if err := json.Unmarshal(out, &monitors); err != nil {
		return nil, fmt.Errorf("decode hyprctl monitors: %w", err)
	}

	return monitors, nil
}

func orientationTransform(value string) (int, bool) {
	switch strings.TrimSpace(value) {
	case "normal":
		return 0, true
	// iio-sensor-proxy names the edge that points up. Hyprland transform 1
	// turns the picture for the left edge up (ZBook x2 G4, ODDC 2026-10-11).
	case "left-up":
		return 1, true
	case "bottom-up":
		return 2, true
	case "right-up":
		return 3, true
	default:
		return 0, false
	}
}

// Hyprland runs a scale only when it divides the mode into whole logical
// pixels, in 1/120 steps, but hyprctl prints it with two decimals: 1.875
// shows as 1.88, which Hyprland rejects when it is passed back. This finds
// the step that prints as reported.
func runningScale(width, height int, reported float64) float64 {
	shown := strconv.FormatFloat(reported, 'f', 2, 64)
	best, bestDist := reported, 1.0
	center := int(math.Round(reported * 120))
	for step := center - 1; step <= center+1; step++ {
		if step <= 0 || width*120%step != 0 || height*120%step != 0 {
			continue
		}
		scale := float64(step) / 120
		if strconv.FormatFloat(scale, 'f', 2, 64) != shown {
			continue
		}
		if dist := math.Abs(scale - reported); dist < bestDist {
			best, bestDist = scale, dist
		}
	}
	return best
}

func applyHyprlandRotation(sysRoot, orientation string) error {
	transform, ok := orientationTransform(orientation)
	if !ok {
		return nil
	}

	connector, err := internalDRMConnector(sysRoot)
	if err != nil {
		return err
	}

	monitors, err := hyprlandMonitors()
	if err != nil {
		return err
	}

	var monitor *hyprMonitor
	for i := range monitors {
		if monitors[i].Name == connector {
			monitor = &monitors[i]
			break
		}
	}
	if monitor == nil {
		return fmt.Errorf("internal connector %q not present in Hyprland", connector)
	}

	mode := fmt.Sprintf("%dx%d@%.3f", monitor.Width, monitor.Height, monitor.RefreshRate)
	position := fmt.Sprintf("%dx%d", monitor.X, monitor.Y)
	scale := strconv.FormatFloat(runningScale(monitor.Width, monitor.Height, monitor.Scale), 'f', -1, 64)
	spec := fmt.Sprintf(
		"%s,%s,%s,%s,transform,%d",
		monitor.Name,
		mode,
		position,
		scale,
		transform,
	)

	for _, args := range [][]string{
		{"keyword", "monitor", spec},
		// Touch screen and pen belong to the built-in panel. Hyprland's
		// automatic binding finds no monitor for them.
		{"keyword", "input:touchdevice:output", monitor.Name},
		{"keyword", "input:tablet:output", monitor.Name},
		{"keyword", "input:touchdevice:transform", strconv.Itoa(transform)},
		{"keyword", "input:tablet:transform", strconv.Itoa(transform)},
	} {
		cmd := exec.Command("hyprctl", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf(
				"hyprctl %s: %w: %s",
				strings.Join(args, " "),
				err,
				strings.TrimSpace(string(out)),
			)
		}
	}

	return nil
}

// sensorOrientation reads the orientation from a monitor-sensor line.
func sensorOrientation(line string) string {
	switch {
	case strings.Contains(line, "Accelerometer orientation changed:"):
		return strings.TrimSpace(
			strings.TrimPrefix(
				line[strings.Index(line, "Accelerometer orientation changed:"):],
				"Accelerometer orientation changed:",
			),
		)

	case strings.Contains(line, "Has accelerometer (orientation:"):
		start := strings.Index(line, "orientation:")
		if start >= 0 {
			rest := line[start+len("orientation:"):]
			if end := strings.IndexAny(rest, ",)"); end >= 0 {
				rest = rest[:end]
			}
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// watchHyprlandReloads signals reloads after each Hyprland config reload.
func watchHyprlandReloads(reloads chan<- struct{}, stderr io.Writer) {
	socket := filepath.Join(
		os.Getenv("XDG_RUNTIME_DIR"), "hypr",
		os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"), ".socket2.sock",
	)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		fmt.Fprintf(stderr, "WARN: Hyprland events: %v; rotation is not restored after a config reload\n", err)
		return
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "configreloaded>>") {
			select {
			case reloads <- struct{}{}:
			default:
			}
		}
	}
	fmt.Fprintf(stderr, "WARN: Hyprland events ended: %v\n", scanner.Err())
}

func runHyprlandRotate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gjallarctl hyprland-rotate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	switchName := flags.String("tablet-switch", "", "rotate only while this input device reports tablet mode")
	requireSwitch := flags.Bool("require-tablet-switch", false, "treat a missing tablet mode switch as laptop mode")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}

	cmd := exec.Command("monitor-sensor")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(stderr, "FAIL: monitor-sensor stdout: %v\n", err)
		return 1
	}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "FAIL: start monitor-sensor: %v\n", err)
		return 1
	}

	orientations := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			if orientation := sensorOrientation(scanner.Text()); orientation != "" {
				orientations <- orientation
			}
		}
		scanErr <- scanner.Err()
		close(orientations)
	}()

	// A config reload (Monique saving, for one) brings back the monitor rule
	// from the config, so the picture turns back until the sensor changes.
	reloads := make(chan struct{}, 1)
	go watchHyprlandReloads(reloads, stderr)

	// With a tablet mode switch the screen turns only in tablet mode; in
	// laptop mode it keeps the configured orientation (ODDC 35.3).
	gate := newRotationGate(*switchName != "", *requireSwitch)
	switches := make(chan tabletSwitchState)
	if *switchName != "" {
		go watchTabletSwitch("/sys", "/dev/input", *switchName, switches, stderr)
	}

	for running := true; running; {
		var action rotationAction
		select {
		case orientation, ok := <-orientations:
			if !ok {
				running = false
				continue
			}
			action = gate.orientation(orientation)
		case state := <-switches:
			action = gate.switchState(state)
		case <-reloads:
			action = gate.reloaded()
		}

		switch action {
		case rotationApply:
			// A failed apply may have changed some keywords already, so
			// laptop mode restores after any attempt.
			gate.applied()
			if err := applyHyprlandRotation("/sys", gate.current); err != nil {
				fmt.Fprintf(stderr, "WARN: rotate %s: %v\n", gate.current, err)
				continue
			}
			fmt.Fprintf(stdout, "PASS: orientation %s applied\n", gate.current)
		case rotationRestore:
			// A reload brings back the monitor and input rules from the config.
			if out, err := exec.Command("hyprctl", "reload").CombinedOutput(); err != nil {
				fmt.Fprintf(stderr, "WARN: hyprctl reload: %v: %s\n", err, strings.TrimSpace(string(out)))
				continue
			}
			gate.reloaded()
			fmt.Fprintln(stdout, "PASS: laptop mode, configured orientation restored")
		}
	}

	if err := <-scanErr; err != nil {
		fmt.Fprintf(stderr, "FAIL: read monitor-sensor: %v\n", err)
		return 1
	}

	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(stderr, "FAIL: monitor-sensor: %v\n", err)
		return 1
	}

	return 0
}

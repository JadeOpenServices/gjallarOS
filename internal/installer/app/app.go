package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/control"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/apps"
	"github.com/JadeOpenServices/gjallarOS/internal/ai/aitoken"
	"github.com/JadeOpenServices/gjallarOS/internal/ai/profile"
	"github.com/JadeOpenServices/gjallarOS/internal/hardware/graphics"
	"github.com/JadeOpenServices/gjallarOS/internal/input/xkb"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/baremetalinstall"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/bootstrap"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/checkoutowner"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deploy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deviceprofile"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deviceprofilecache"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/firefoxstore"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/firmware"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/geolocation"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/hardwareconfig"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installstate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/localgit"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/nixrender"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryprovision"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryresize"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoverytarget"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/release"
	installerresume "github.com/JadeOpenServices/gjallarOS/internal/installer/resume"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/sourcerevision"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

type options struct {
	recovery                                                 bool
	repo                                                     string
	skipHardware, refreshHardware, noRebuild, acceptExisting bool
	forceRedeploy                                            bool
	targetDisk                                               string
	recoveryDisk, recoveryPartition, recoverySigningKey      string
	recoverySigningPublicKey                                 string
}
type state struct {
	user                     config.User
	render                   nixrender.Settings
	preset, existing         bool
	control                  string
	touchscreen              bool
	chassisType              string
	penTablet                bool
	orientationSensor        bool
	recoveryDisk             string
	recoveryPartition        string
	recoverySigningKey       string
	recoverySigningPublicKey string
	secureBootFirmware       oddc.EffectiveSecureBootFirmwarePolicy
	deviceGate               string
}

const (
	installerSecureBootResumeMarker = "/var/lib/gjallarOS/installer-resume-after-secure-boot"
	installerReleaseRebootScheduled = 75
)

var detectNetworkLocation = geolocation.Detect
var runJODSCommand = attached

func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 2 && args[0] == "--resume-transaction" {
		return runResumed(ctx, args[1], in, out, errOut)
	}
	return run(ctx, args, in, out, errOut)
}

// runResumed runs the transaction installer-resume.service hands over. The
// argument is the state directory; older wrapper units pass active.json.
func runResumed(ctx context.Context, path string, in io.Reader, out, errOut io.Writer) int {
	dir := path
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		dir = filepath.Dir(path)
	}

	tx, err := installerresume.Claim(dir)
	if err != nil {
		return fail(errOut, err)
	}

	if err := validateResume(tx); err != nil {
		return fail(errOut, errors.Join(err, installerresume.Finish(dir, false, false)))
	}

	for key, value := range map[string]string{
		"GJALLAR_INSTALLER_RESUMED":      "1",
		"GJALLAR_INSTALLER_RESUME_STATE": tx.State,
		resumedDeviceGateEnv:             tx.DeviceGate,
	} {
		if err := os.Setenv(key, value); err != nil {
			return fail(
				errOut,
				fmt.Errorf(
					"mark validated installer resume transaction: %w",
					err,
				),
			)
		}
	}

	if err := trustResumeRepository(tx.Repo); err != nil {
		return fail(errOut, err)
	}

	code := run(ctx, tx.Args, in, out, errOut)

	// A shutdown that stops the service mid-run is an unplanned reboot:
	// keep the transaction so the next boot resumes it.
	if err := installerresume.Finish(dir, code == 0, ctx.Err() != nil); err != nil {
		fmt.Fprintf(errOut, "WARNING: %v\n", err)
	}

	return code
}

// trustResumeRepository lets git, run as root by installer-resume.service,
// read the repository the installing user owns; it refused that as dubious
// ownership (e2e-full, 2026-10-05). The root-owned transaction names the
// repository that user already deployed as root, so this trusts nothing new.
func trustResumeRepository(repo string) error {
	count := 0
	if value := os.Getenv("GIT_CONFIG_COUNT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return fmt.Errorf("invalid GIT_CONFIG_COUNT %q", value)
		}
		count = parsed
	}

	index := strconv.Itoa(count)
	for key, value := range map[string]string{
		"GIT_CONFIG_KEY_" + index:   "safe.directory",
		"GIT_CONFIG_VALUE_" + index: repo,
		"GIT_CONFIG_COUNT":          strconv.Itoa(count + 1),
	} {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("trust resumed GjallarOS repository: %w", err)
		}
	}

	return nil
}

func validateResume(tx installerresume.Transaction) error {
	if tx.State == installerresume.StateMaintenanceReboot {
		return nil
	}

	expected, active, err := release.Inspect(
		tx.Repo,
		"/run/current-system/etc/os-release",
	)
	if err != nil {
		return err
	}

	if expected != tx.ExpectedRelease {
		return fmt.Errorf(
			"resume expected NixOS %s but repository policy now expects %s",
			tx.ExpectedRelease,
			expected,
		)
	}

	if active != tx.ExpectedRelease {
		return fmt.Errorf(
			"staged NixOS %s did not become active; current release is %s; automatic retry disabled",
			tx.ExpectedRelease,
			active,
		)
	}

	return nil
}

// maintenanceArgs keeps the mode this run settled on for the rerun after
// the maintenance boot. By then the machine is a GjallarOS install, so the
// first run's arguments alone would ask again whether to touch it, and the
// default answer leaves it half installed.
func maintenanceArgs(args []string, forceRedeploy bool) []string {
	mode := "--accept-existing"
	if forceRedeploy {
		mode = "--force-redeploy"
	}
	out := append([]string(nil), args...)
	for _, arg := range out {
		if arg == mode {
			return out
		}
	}
	return append(out, mode)
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if os.Getenv("GJALLAR_INSTALLER_RESUMED") == "1" {
		fmt.Fprintf(
			out,
			"PASS: installer resumed (%s)\n",
			os.Getenv("GJALLAR_INSTALLER_RESUME_STATE"),
		)
	}

	originalArgs := append([]string(nil), args...)

	f := flag.NewFlagSet("gjallar-installer", flag.ContinueOnError)
	f.SetOutput(errOut)
	cwd, _ := os.Getwd()
	opt := options{}
	f.StringVar(&opt.repo, "repo", cwd, "GjallarOS repository")
	f.BoolVar(&opt.skipHardware, "skip-hardware", false, "skip hardware generation")
	f.BoolVar(&opt.refreshHardware, "refresh-hardware", false, "regenerate hardware configuration")
	f.BoolVar(&opt.noRebuild, "no-rebuild", false, "do not install a boot generation")
	f.BoolVar(&opt.acceptExisting, "accept-existing", false, "allow an existing GjallarOS installation to be updated")
	f.BoolVar(&opt.forceRedeploy, "force-redeploy", false, "force a full clean GjallarOS redeployment without repartitioning or wiping user data")
	f.BoolVar(&opt.recovery, "recovery", false, "run from the trusted GjallarOS recovery environment")
	f.StringVar(&opt.targetDisk, "target-disk", "", "whole physical disk for a destructive fresh GjallarOS installation")
	f.StringVar(&opt.recoveryDisk, "recovery-disk", "", "GPT disk with unallocated space for a recovery partition")
	f.StringVar(&opt.recoveryPartition, "recovery-partition", "", "existing dedicated recovery partition")
	f.StringVar(&opt.recoverySigningKey, "recovery-signing-key", "", "runtime path to offline Ed25519 recovery release private key")
	f.StringVar(&opt.recoverySigningPublicKey, "recovery-signing-public-key", "", "runtime path to separately pinned Ed25519 recovery release public key")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		return 2
	}
	root, err := installercheck.ResolveRepository(opt.repo)
	if err != nil {
		return fail(errOut, err)
	}
	opt.repo = root
	if err := requireNixOS("/etc/os-release"); err != nil {
		return fail(errOut, err)
	}
	// A previous run as root may have left root-owned files in the checkout
	// (generated/hardware.nix 0600 and its backups); hand them back first,
	// and hand back whatever this run writes when it runs as root itself.
	if err := checkoutowner.Ensure(ctx, root, func(ctx context.Context, args ...string) error {
		_, err := privilegedCommand(ctx, args...)
		return err
	}); err != nil {
		return fail(errOut, err)
	}
	defer func() {
		if err := checkoutowner.Repair(root); err != nil {
			fmt.Fprintf(errOut, "WARNING: %v\n", err)
		}
	}()
	ui := prompt.New(in, out)
	s := state{control: controlBinary()}
	installedRoot := "/"
	presetRoot := root
	if opt.recovery && opt.acceptExisting {
		installedRoot = "/mnt"
		presetRoot = installedRoot
	}
	presetPath := filepath.Join(presetRoot, "user.config.json")
	if _, err := os.Stat(presetPath); err == nil {
		s.user, err = config.Load(presetPath)
		if err != nil {
			return fail(errOut, err)
		}
		s.preset = true
	} else {
		// Interactive installs start where the preset template starts.
		if s.user, err = config.Defaults(); err != nil {
			return fail(errOut, err)
		}
	}
	if opt.recovery {
		if opt.acceptExisting {
			s.existing, err = detectRecoveryInstalledRoot(ctx, installedRoot)
			if err != nil {
				return fail(errOut, err)
			}
		} else {
			// The embedded repository is installation source material, not evidence
			// that the recovery environment itself is an installed GjallarOS system.
			// Recovery without --accept-existing is the explicit fresh operation.
			s.existing = false
		}
	} else {
		s.existing, err = detectExistingInstalledSystem(ctx, root)
		if err != nil {
			return fail(errOut, err)
		}
	}

	persistentInstalledHost := s.existing
	if !opt.recovery && !persistentInstalledHost {
		persistentInstalledHost, err = detectPersistentInstalledHost(ctx)
		if err != nil {
			return fail(errOut, err)
		}
	}
	if err := validateTargetDiskContext(opt.targetDisk, persistentInstalledHost); err != nil {
		return fail(errOut, err)
	}

	forceRedeploy := opt.forceRedeploy || s.user.ForceRedeploy

	if s.existing && !forceRedeploy && !opt.acceptExisting {
		choice, err := ui.Choice(
			ctx,
			"Existing GjallarOS installation detected",
			existingLeave,
			[]string{existingUpdate, existingRedeploy, existingLeave},
		)
		if err != nil {
			return fail(errOut, err)
		}
		if choice == existingLeave {
			fmt.Fprintln(out, "Existing installation left unchanged.")
			return 0
		}
		forceRedeploy = choice == existingRedeploy
	}

	opt.forceRedeploy = forceRedeploy

	if forceRedeploy && opt.noRebuild {
		return fail(
			errOut,
			errors.New("force redeployment requires a rebuild; --force-redeploy cannot be combined with --no-rebuild"),
		)
	}

	if s.existing && forceRedeploy {
		fmt.Fprintln(
			out,
			"Force clean redeployment selected; preserving disk layout, credentials, encryption keys, and user data.",
		)
	}
	if skip := liveHostPreparationSkip(
		opt,
		os.Getenv("GJALLAR_INSTALLER_RESUME_STATE"),
	); skip != "" {
		fmt.Fprintln(out, skip)
	} else if code := prepareHost(
		ctx,
		ui,
		opt,
		s,
		originalArgs,
		out,
		errOut,
	); code != 0 {
		if code == installerReleaseRebootScheduled {
			return 0
		}
		return code
	}
	sourceRevision, err := sourcerevision.Resolve(root, opt.recovery)
	if err != nil {
		return fail(errOut, fmt.Errorf("resolve GjallarOS source revision: %w", err))
	}

	hardware := detectInstallerHardware("/sys")

	var resolvedSource oddc.DeviceSource
	resolvedSource, err = deviceprofile.CurrentSource(root)
	if err != nil {
		return fail(errOut, err)
	}
	resolvedDevice, err := resolveODDCModelFromSource(
		resolvedSource,
		hardware,
	)
	if err != nil {
		return fail(errOut, err)
	}

	needsDeviceRebind := false
	if opt.recovery && opt.acceptExisting {
		capsulePath := filepath.Join(
			installedRoot,
			"var",
			"lib",
			"gjallarOS",
			"device-profile",
		)

		capsule, err := deviceprofilecache.Verify(capsulePath)
		if err != nil {
			return fail(
				errOut,
				fmt.Errorf("verify recovery device-profile capsule: %w", err),
			)
		}

		needsDeviceRebind = deviceprofilecache.NeedsRebind(
			capsule,
			discovery.ODDCIdentity(hardware),
			resolvedDevice,
		)

		if !needsDeviceRebind {
			resolvedSource = oddc.EmbeddedSource{
				Root:       filepath.Join(capsulePath, "oddc"),
				Repository: capsule.Source.ODDCRepository,
				Revision:   capsule.Source.ODDCRevision,
				Integrity:  capsule.Source.ODDCIntegrity,
			}

			resolvedDevice, err = resolveODDCModelFromSource(
				resolvedSource,
				hardware,
			)
			if err != nil {
				return fail(
					errOut,
					fmt.Errorf("resolve cached recovery device profile: %w", err),
				)
			}
		}
	}

	var activeHostOverlay *oddc.HostOverlay

	if shouldApplyODDCHostOverlay(
		persistentInstalledHost,
		needsDeviceRebind,
	) {
		resolvedDevice, activeHostOverlay, err = resolveODDCModelWithHost(
			ctx,
			installedRoot,
			resolvedSource,
			hardware,
			resolvedDevice,
		)
		if err != nil {
			return fail(errOut, err)
		}
	}

	hostOverlayChanged := false

	hardware, activeHostOverlay, hostOverlayChanged, err =
		reconcileInternalHardware(
			ctx,
			ui,
			resolvedDevice,
			hardware,
			activeHostOverlay,
			s.user.UnattendedInstall,
			out,
		)
	if err != nil {
		return fail(errOut, err)
	}

	if hostOverlayChanged {
		resolvedDevice, err = resolveODDCModelFromSourceWithHost(
			resolvedSource,
			hardware,
			activeHostOverlay,
		)
		if err != nil {
			return fail(
				errOut,
				fmt.Errorf(
					"apply staged machine-local ODDC hardware removals: %w",
					err,
				),
			)
		}
	}

	s.touchscreen = hardware.Touchscreen
	s.chassisType = hardware.ChassisType
	s.penTablet = hardware.PenTablet
	s.orientationSensor = hardware.OrientationSensor
	// ODDC owns the fact when the model states it; local IIO discovery
	// decides only for machines ODDC has no answer for.
	if present, ok := oddc.OrientationSensor(resolvedDevice); ok {
		s.orientationSensor = present
	}

	if _, err := oddc.ResolveGraphicsPolicy(resolvedDevice); err != nil {
		return fail(errOut, err)
	}

	if resolvedDevice.ModelID == "" {
		fmt.Fprintln(out, "ODDC canonical model: none (unsupported by ODDC; installing the hardware-neutral baseline)")
	} else {
		fmt.Fprintf(out, "ODDC canonical model: %s\n", resolvedDevice.ModelID)
	}

	if needsDeviceRebind {
		if s.user.UnattendedInstall || s.user.EndpointManagedDevice {
			return fail(
				errOut,
				errors.New(
					"cached recovery device profile does not match current hardware; explicit device rebind authorization is required",
				),
			)
		}

		approved, err := ui.Confirm(
			ctx,
			"Recovery device identity does not match the current hardware. Rebind recovery state to this machine and regenerate device-specific configuration?",
			false,
		)
		if err != nil {
			return fail(errOut, err)
		}
		if !approved {
			return fail(
				errOut,
				errors.New("device rebind was not authorized"),
			)
		}

		opt.refreshHardware = true
		fmt.Fprintln(out, "Authorized device rebind; hardware configuration will be regenerated.")
	}

	pinnedRelease, err := release.Expected(root)
	if err != nil {
		return fail(errOut, fmt.Errorf("resolve pinned NixOS release: %w", err))
	}

	if err := enforceDeviceValidation(
		ctx,
		ui,
		s.user,
		resolvedDevice,
		pinnedRelease,
		sourceRevision,
	); err != nil {
		return fail(errOut, err)
	}
	s.deviceGate = deviceGate(resolvedDevice, pinnedRelease, sourceRevision)

	s.secureBootFirmware, err = oddc.ResolveSecureBootFirmwarePolicy(resolvedDevice)
	if err != nil {
		return fail(errOut, fmt.Errorf(
			"resolve Secure Boot firmware policy: %w",
			err,
		))
	}

	fmt.Fprintf(out, "Touchscreen detected: %t\n", hardware.Touchscreen)
	fmt.Fprintf(out, "Pen/tablet detected: %t\n", hardware.PenTablet)
	fmt.Fprintf(out, "Orientation sensor: %t\n", s.orientationSensor)

	choices, err := discovery.Discover(root)
	if err != nil {
		return fail(errOut, err)
	}
	if s.preset {
		normalizePreset(&s.user, root)
		if s.user.USBGuardEnable && resolvedDevice.ModelID == "" {
			return fail(errOut, errors.New("usbguardEnable needs an ODDC model, and ODDC has none for this machine; set usbguardEnable to false"))
		}
		if s.user.USBGuardEnable && s.user.USBTrustTPMHandle == "" && diskcrypto.TPMAvailable() {
			s.user.USBTrustTPMHandle = config.DefaultUSBTrustTPMHandle
		}
	} else if err := collectInteractive(ctx, ui, root, !persistentInstalledHost || rootEncrypted(ctx, "/"), resolvedDevice.ModelID != "", diskcrypto.TPMAvailable(), jodsManagementBlocker(s.secureBootFirmware.Policy.Supported, diskcrypto.TPMAvailable()), hardware, choices, &s.user); err != nil {
		return fail(errOut, err)
	}
	if err := configureWeatherLocation(ctx, ui, &s.user, out); err != nil {
		return fail(errOut, err)
	}
	// Resolve managed security requirements before checking the physical TPM
	// boundary so policy normalization cannot re-enable TPM-dependent features
	// after a no-TPM decision.
	normalizeManagementSafety(&s.user)

	tpmAvailable := diskcrypto.TPMAvailable()
	securityRequested := s.user.SecureBootPrompt ||
		s.user.SecureBootEnable ||
		s.user.LUKSTPM2Enable ||
		s.user.JODSPrebootLockEnable ||
		s.user.USBTrustEnforce

	// TPM 1.2 has only SHA-1 PCRs; systemd-cryptenroll and the measured-boot
	// policy need TPM 2.0. Its owner may expect Secure Boot, so always say
	// so and ask, even when nothing asked for Secure Boot.
	tpm12 := !tpmAvailable && diskcrypto.TPMMajorVersion("/") == 1
	if tpm12 && !securityRequested && s.user.UnattendedInstall {
		fmt.Fprintln(out)
		fmt.Fprint(out, tpm12Notice)
	} else if !tpmAvailable && (securityRequested || tpm12) {
		fmt.Fprintln(out)
		if tpm12 {
			fmt.Fprint(out, tpm12Notice)
		} else {
			fmt.Fprintln(out, "TPM2 hardware was not detected.")
			fmt.Fprintln(
				out,
				"GjallarOS requires TPM2 for its Secure Boot and measured-boot security policy.",
			)
		}
		fmt.Fprintln(
			out,
			"Secure Boot, TPM2 LUKS unlock, TPM-dependent JODS preboot locking, and USB blocking must be disabled to continue.",
		)

		if s.user.UnattendedInstall {
			return fail(
				errOut,
				errors.New(
					"TPM2 is unavailable while user.config.json requests TPM-dependent security; rerun interactively to approve disabling Secure Boot",
				),
			)
		}

		continued, err := ui.Confirm(
			ctx,
			"Continue installation with Secure Boot disabled?",
			false,
		)
		if err != nil {
			return fail(errOut, err)
		}
		if !continued {
			fmt.Fprintln(
				out,
				"Installation cancelled. Secure Boot settings were not changed.",
			)
			return 0
		}

		disableTPMDependentSecurity(&s.user)

		if s.preset {
			if err := config.WriteAtomic(presetPath, s.user); err != nil {
				return fail(
					errOut,
					fmt.Errorf(
						"persist no-TPM security settings to user.config.json: %w",
						err,
					),
				)
			}

			fmt.Fprintln(
				out,
				"Updated user.config.json for this TPM-less machine: secureBootPrompt=false, secureBootEnable=false, luksTpm2Enable=false, jodsPrebootLockEnable=false, usbTrustEnforce=false, usbTrustTpmHandle=\"\".",
			)
		}
	}

	if tpmAvailable &&
		s.user.SecureBootPrompt &&
		!s.user.EndpointManagedDevice &&
		s.secureBootFirmware.Policy.Supported {
		// Taking over firmware key ownership needs informed consent: show
		// what is replaced, what stays trusted, and what the user must keep
		// before asking.
		if err := ui.SecureBootOwnershipPlan(
			ctx,
			secureboot.OwnershipPlan(
				s.secureBootFirmware.Policy,
				s.user.LUKSTPM2Enable,
			),
		); err != nil {
			return fail(errOut, err)
		}

		promptText := "Prepare Secure Boot and recovery keys?"
		if firmwareName := strings.TrimSpace(
			s.secureBootFirmware.Policy.FirmwareName,
		); firmwareName != "" {
			promptText = "Prepare Secure Boot and recovery keys for " +
				firmwareName + "?"
		}

		s.user.SecureBootEnable, err = ui.Confirm(
			ctx,
			promptText,
			false,
		)
		if err != nil {
			return fail(errOut, err)
		}

		if s.user.SecureBootEnable {
			s.user.FirmwarePasswordLock, err = ui.Confirm(
				ctx,
				"Lock firmware setup with a new supervisor password when Secure Boot gets enabled (you set it in firmware; not the disk passphrase)?",
				true,
			)
			if err != nil {
				return fail(errOut, err)
			}
			if err := collectStartupUnlock(ctx, ui, s.chassisType, s.touchscreen, &s.user); err != nil {
				return fail(errOut, err)
			}
		}
	} else if tpmAvailable &&
		s.user.SecureBootPrompt &&
		!s.user.SecureBootEnable &&
		!s.user.EndpointManagedDevice {
		fmt.Fprint(out, noSecureBootPolicyNotice(resolvedDevice.ModelID))
	} else if s.user.SecureBootEnable &&
		s.secureBootFirmware.Policy.Supported {
		// user.config.json or endpoint management already decided; still
		// tell the user before any key is generated.
		fmt.Fprint(out, secureboot.OwnershipPlan(
			s.secureBootFirmware.Policy,
			s.user.LUKSTPM2Enable,
		))
		if s.user.FirmwarePasswordLock {
			fmt.Fprintln(out, "firmwarePasswordLock=true: the final firmware step asks you to set a supervisor password.")
		} else {
			fmt.Fprintln(out, "firmwarePasswordLock=false: firmware setup stays without a supervisor password.")
		}
	}

	if err := validateSecureBootFirmwareSupport(
		s.user.SecureBootEnable,
		resolvedDevice.ModelID,
		s.secureBootFirmware,
	); err != nil {
		if mayOfferSecureBootFallback(
			s.user.SecureBootEnable,
			s.user.EndpointManagedDevice,
			s.user.UnattendedInstall,
		) {

			continued, promptErr := ui.Confirm(
				ctx,
				fmt.Sprintf(
					"%v Continue installation with Secure Boot disabled?",
					err,
				),
				false,
			)
			if promptErr != nil {
				return fail(errOut, promptErr)
			}
			if !continued {
				return fail(errOut, err)
			}

			disableTPMDependentSecurity(&s.user)

			if s.preset {
				if err := config.WriteAtomic(presetPath, s.user); err != nil {
					return fail(
						errOut,
						fmt.Errorf(
							"persist unsupported-firmware security fallback to user.config.json: %w",
							err,
						),
					)
				}

				fmt.Fprintln(
					out,
					"Updated user.config.json to keep unsupported Secure Boot and TPM2-dependent security disabled.",
				)
			}

			fmt.Fprintln(
				out,
				"Continuing installation with Secure Boot and TPM2 measured-boot unlock disabled.",
			)
		} else {
			return fail(errOut, err)
		}
	}

	// Checked once the Secure Boot decision is final, whatever led to it:
	// no TPM, no ODDC setup, a declined prompt or user.config.json. Each of
	// them installs an unsigned boot loader that enforcing firmware refuses.
	if !s.user.SecureBootEnable {
		firmwareOn, firmwareErr := secureboot.FirmwareSecureBoot()
		fmt.Fprint(out, firmwareSecureBootNotice(firmwareOn, firmwareErr))
		if firmwareOn {
			if s.user.UnattendedInstall {
				return fail(errOut, errors.New(
					"firmware enforces Secure Boot but GjallarOS Secure Boot is not set up for this install; turn Secure Boot off in firmware setup and rerun",
				))
			}
			choice, err := ui.Choice(
				ctx,
				"Secure Boot is on in firmware",
				secureBootOffReboot,
				[]string{secureBootOffReboot, secureBootOffLater, secureBootOffCancel},
			)
			if err != nil {
				return fail(errOut, err)
			}
			switch choice {
			case secureBootOffReboot:
				fmt.Fprintln(out, "Nothing has been installed yet. Turn Secure Boot off in firmware setup, then start the installer again.")
				fmt.Fprintln(out, "Rebooting directly into firmware setup...")
				if err := attached(ctx, "sudo", "systemctl", "reboot", "--firmware-setup"); err != nil {
					return fail(errOut, fmt.Errorf("reboot into firmware setup: %w", err))
				}
				return 0
			case secureBootOffCancel:
				fmt.Fprintln(out, "Installation cancelled. Nothing was changed.")
				return 0
			}
		}
	}

	if s.existing &&
		s.user.RecoveryEnable &&
		s.user.RecoveryPartitionEnable {
		continued, err := handleExistingRecoveryFilesystem(
			ctx,
			ui,
			presetPath,
			&s,
			out,
		)
		if err != nil {
			return fail(errOut, err)
		}
		if !continued {
			fmt.Fprintln(
				out,
				"Existing installation left unchanged; recovery compatibility was not accepted.",
			)
			return 0
		}
	}

	if err := configureRecoveryProvisioning(ctx, ui, opt, &s); err != nil {
		return fail(errOut, err)
	}
	if s.user.EndpointManagedDevice && s.user.JODSAllowInsecureTLS {
		if err := confirmInsecureJODS(ctx, ui); err != nil {
			return fail(errOut, err)
		}
	}
	// Management-only boot restrictions must never survive in an unmanaged
	// preset or a user declining JODS enrollment.
	if err := validateSelections(s.user, choices); err != nil {
		return fail(errOut, err)
	}
	if s.user.JODSPrebootLockEnable && (!s.user.RecoveryEnable || !s.user.SecureBootEnable) {
		return fail(errOut, errors.New("JODS preboot locking requires both the trusted recovery entry and Secure Boot"))
	}
	if err := config.Validate(s.user); err != nil {
		return fail(errOut, err)
	}
	if err := detectAndRenderState(
		ctx,
		root,
		&s,
		hardware,
		resolvedDevice,
	); err != nil {
		return fail(errOut, err)
	}
	if err := configureSecrets(ctx, root, &s, errOut); err != nil {
		return fail(errOut, err)
	}
	fmt.Fprintf(out, "\nSelected: hostname=%s user=%s shell=%s\n", s.user.Hostname, s.user.Username, s.user.Shell)
	write := s.user.WriteConfig
	if !s.preset {
		write, err = ui.Confirm(ctx, "Write configuration to "+filepath.Join(root, "generated", "state.nix")+"?", false)
		if err != nil {
			return fail(errOut, err)
		}
	}
	if !write {
		fmt.Fprintln(out, "Nothing changed.")
		return 0
	}
	compatibilityConfig := ""
	if persistentInstalledHost {
		compatibilityConfig = filepath.Join(installedRoot, "etc/nixos/configuration.nix")
	}
	if err := installstate.Ensure(ctx, root, compatibilityConfig, s.user.Username, pinnedRelease); err != nil {
		return fail(errOut, fmt.Errorf("initialize installation compatibility state: %w", err))
	}
	if !s.preset {
		if err := config.WriteAtomic(presetPath, s.user); err != nil {
			return fail(errOut, err)
		}
		fmt.Fprintln(out, "Wrote", presetPath)
	}
	// A managed fresh installation may not create local account credentials
	// before the device has completed its JODS pre-install enrollment and
	// recovery-identity attestation. The legacy post-install enrollment path
	// is intentionally insufficient for this boundary.
	if s.render.EndpointManagedDevice && !s.existing {
		return fail(
			errOut,
			errors.New(
				"managed fresh installation requires JODS pre-install device enrollment and recovery-identity attestation before password provisioning",
			),
		)
	}

	rootPasswordPath := "/var/lib/gjallarOS/passwords/root.hash"
	if s.existing && privilegedFileExists(ctx, rootPasswordPath) {
		s.render.RootPasswordFile = rootPasswordPath
		fmt.Fprintln(out, "Existing root password hash retained.")
	} else {
		// Root recovery is intentionally local even on JODS-managed endpoints.
		// Management must never remove wheel/Polkit administration before a
		// verified local recovery credential exists.
		path, err := controlOutput(ctx, s.control, errOut, rootPasswordArgs(persistentInstalledHost)...)
		if err != nil {
			return fail(errOut, err)
		}
		lines := strings.Fields(strings.TrimSpace(path))
		if len(lines) > 0 {
			s.render.RootPasswordFile = lines[len(lines)-1]
		}
		fmt.Fprint(out, path)
	}
	if err := s.ensureUserPassword(ctx, persistentInstalledHost, out, errOut); err != nil {
		return fail(errOut, err)
	}
	if s.user.HasApp("ai") && s.user.AIEndpoint != "" {
		if err := ensureAIToken(ctx, out); err != nil {
			return fail(errOut, err)
		}
	}
	settingsPath := filepath.Join(root, "generated", "state.nix")
	if err := nixrender.WriteAtomic(settingsPath, s.render); err != nil {
		return fail(errOut, err)
	}
	fmt.Fprintln(out, "Wrote", settingsPath)
	hardwarePath := hardwareconfig.Target(root)
	hardwareGenerator := hardwareconfig.Generate
	if opt.recovery && opt.acceptExisting {
		hardwareGenerator = hardwareconfig.GenerateTarget
	}

	hardwareResult, err := reconcileHardwareConfiguration(
		ctx,
		root,
		hardwarePath,
		s.existing,
		opt.skipHardware,
		opt.refreshHardware,
		time.Now(),
		hardwareGenerator,
	)
	if err != nil {
		return fail(errOut, err)
	}

	skip := hardwareResult.action != hardwareGenerate

	switch hardwareResult.action {
	case hardwareSkip:
		fmt.Fprintln(out, "Hardware generation explicitly skipped.")

	case hardwareRetain:
		fmt.Fprintln(out, "Existing hardware configuration retained:", hardwarePath)

	case hardwareGenerate:
		if s.existing && !hardwareResult.existed {
			fmt.Fprintln(
				out,
				"Existing installation is missing hardware configuration; generating it from the running machine.",
			)
		} else if opt.refreshHardware {
			fmt.Fprintln(
				out,
				"Hardware refresh requested; regenerating from the running machine.",
			)
		}

		fmt.Fprintln(out, "PLAN: generate and atomically replace", hardwarePath)
		if hardwareResult.backup != "" {
			fmt.Fprintln(out, "Backup:", hardwareResult.backup)
		}
		if !tpm2AllowedForSecureBoot(s.render.SecureBootEnable) {
			s.render.LUKSTPM2Enable = false
			s.user.LUKSTPM2Enable = false
			fmt.Fprintln(
				out,
				"GjallarOS Secure Boot is not set up on this install; TPM2 disk unlock stays off.",
			)
		} else if tpm2FollowsRequest(
			s.render.EndpointManagedDevice,
			persistentInstalledHost,
		) {
			s.render.LUKSTPM2Enable = s.user.LUKSTPM2Enable
		} else {
			tpmOut, err := controlOutput(ctx, s.control, errOut, "installer", "tpm2", "--repo", root, "--hardware", hardwarePath)
			if err != nil {
				return fail(errOut, err)
			}
			fmt.Fprint(out, tpmOut)
			s.render.LUKSTPM2Enable = strings.Contains(tpmOut, "luks_tpm2_enable=true")
		}
		if err := nixrender.WriteAtomic(settingsPath, s.render); err != nil {
			return fail(errOut, err)
		}
		if !s.render.EndpointManagedDevice && !s.existing {
			// A fresh target does not have its canonical LUKS2 root yet.
			// Running installer luks here would operate against the installer
			// environment instead of the future target.
			//
			// rootprovision creates the initial human recovery-capable keyslot.
			// TPM2 enrollment is intentionally deferred until the installed
			// GjallarOS lifecycle can act against its own verified root device.
			fmt.Fprintln(
				out,
				"Fresh-target TPM2 LUKS enrollment deferred until the installed GjallarOS lifecycle.",
			)
		} else if s.existing {
			fmt.Fprintln(out, "Existing LUKS keyslots retained; disk-key migration was not rerun.")
		}
	}
	if s.render.JODSPrebootLockEnable && !s.render.LUKSTPM2Enable {
		return fail(errOut, errors.New("JODS preboot locking requires verified TPM2 LUKS enrollment; configuration was left unactivated"))
	}

	if opt.recovery {
		// Recovery media uses an embedded writable source tree, not a Git
		// checkout. Local Git excludes are installed/development-tree hygiene
		// and are not applicable to the recovery execution environment.
		fmt.Fprintln(out, "Recovery installer source is embedded; local Git protection skipped.")
	} else {
		fmt.Fprintln(out, "PLAN: atomically update local Git excludes and protect generated machine configuration")
		if _, err := localgit.Protect(ctx, root, func() string {
			if skip {
				return ""
			}
			return hardwarePath
		}()); err != nil {
			return fail(errOut, err)
		}
	}
	if s.render.SecureBootEnable {
		if err := secureboot.Prepare(ctx, out); err != nil {
			return fail(errOut, err)
		}
	}
	hostOverlayToCommit, err := hostOverlayForCommit(
		activeHostOverlay,
		hostOverlayChanged,
		needsDeviceRebind,
		resolvedDevice.ModelID,
	)
	if err != nil {
		return fail(errOut, err)
	}

	runRebuild := s.user.RunRebuild && !opt.noRebuild
	if opt.forceRedeploy {
		runRebuild = true
	}
	if !s.preset && !opt.noRebuild && !opt.forceRedeploy {
		runRebuild, err = ui.Confirm(ctx, "Install the next NixOS boot generation now?", false)
		if err != nil {
			return fail(errOut, err)
		}
	}
	if needsDeviceRebind && !runRebuild {
		return fail(
			errOut,
			errors.New(
				"Recovery device rebind requires installing the regenerated system; rebuild cannot be skipped",
			),
		)
	}

	if runRebuild {
		if persistentInstalledHost {
			maintenanceRequired := false

			if s.user.RecoveryEnable &&
				s.user.RecoveryPartitionEnable &&
				s.recoveryPartition == "" {
				fmt.Fprintln(
					out,
					"STAGE: inspecting existing encrypted-Btrfs layout for recovery storage",
				)

				maintenanceRequired, err = prepareInstalledRecoveryStorage(
					ctx,
					ui,
					out,
					installedRoot,
					&s,
				)
				if err != nil {
					return fail(errOut, err)
				}

				if maintenanceRequired {
					s.recoveryPartition = ""
				}
			}

			if opt.recovery && opt.acceptExisting {
				fmt.Fprintln(
					out,
					"STAGE: preparing authenticated recovery target",
				)

				if err := recoverytarget.PrepareBoot(
					ctx,
					installedRoot,
				); err != nil {
					return fail(errOut, err)
				}

				fmt.Fprintln(
					out,
					"PLAN: reinstall existing system into",
					installedRoot,
				)

				if _, err := baremetalinstall.Install(
					ctx,
					baremetalinstall.Input{
						Repo:     root,
						Hostname: s.user.Hostname,
						Out:      out,
					},
				); err != nil {
					return fail(errOut, err)
				}

				if needsDeviceRebind {
					if err := materializeODDCCapsule(
						ctx,
						root,
						filepath.Join(
							installedRoot,
							"var",
							"lib",
							"gjallarOS",
							"device-profile",
						),
						hardware,
						resolvedDevice,
						true,
						"hardware-rebind",
					); err != nil {
						return fail(
							errOut,
							fmt.Errorf(
								"commit recovery device-profile capsule after hardware rebind: %w",
								err,
							),
						)
					}
					if resolvedDevice.ModelID == "" {
						stageODDCDraft(
							ctx,
							filepath.Join(installedRoot, "var", "lib", "gjallarOS", "device-profile"),
							out,
						)
					}

					fmt.Fprintln(
						out,
						"PASS: recovery device profile rebound to current hardware.",
					)
				}

				if err := saveODDCHostOverlay(
					ctx,
					installedRoot,
					hostOverlayToCommit,
				); err != nil {
					return fail(errOut, err)
				}

				if hostOverlayToCommit != nil {
					fmt.Fprintln(
						out,
						"PASS: machine-local ODDC host state committed after recovery deployment.",
					)
				}
			} else {
				target, err := deploy.Target(
					root,
					s.user.Hostname,
				)
				if err != nil {
					return fail(errOut, err)
				}

				fmt.Fprintln(
					out,
					"PLAN: validate then install",
					target,
				)

				if err := deploy.Apply(ctx, root, s.user.Hostname); err != nil {
					return fail(errOut, err)
				}
				if resolvedDevice.ModelID == "" {
					stageODDCDraft(
						ctx,
						filepath.Join(installedRoot, "var", "lib", "gjallarOS", "device-profile"),
						out,
					)
				}

				if err := saveODDCHostOverlay(
					ctx,
					installedRoot,
					hostOverlayToCommit,
				); err != nil {
					return fail(errOut, err)
				}

				if hostOverlayToCommit != nil {
					fmt.Fprintln(
						out,
						"PASS: machine-local ODDC host state committed after deployment.",
					)
				}
			}

			if maintenanceRequired {
				// The maintenance boot already ran for this transaction and
				// still left no JODS-RECOVERY; another one would loop.
				if os.Getenv("GJALLAR_INSTALLER_RESUME_STATE") == installerresume.StateMaintenanceReboot {
					return fail(
						errOut,
						errors.New("recovery-storage maintenance did not provision JODS-RECOVERY; automatic retry disabled"),
					)
				}

				fmt.Fprintln(
					out,
					"PASS: one-shot recovery-storage maintenance environment installed",
				)

				// The rerun after maintenance formats and fills JODS-RECOVERY
				// and finishes Secure Boot; it stopped here before (e2e-target,
				// 2026-10-05). It runs without a terminal, so only presets
				// continue on their own.
				if s.preset && installedRoot == "/" {
					executable, err := os.Executable()
					if err != nil {
						return fail(
							errOut,
							fmt.Errorf(
								"resolve installer executable for maintenance continuation: %w",
								err,
							),
						)
					}

					if err := installerresume.ArmMaintenance(
						ctx,
						executable,
						opt.repo,
						maintenanceArgs(originalArgs, opt.forceRedeploy),
						s.deviceGate,
					); err != nil {
						return fail(
							errOut,
							fmt.Errorf(
								"arm installer maintenance continuation: %w",
								err,
							),
						)
					}

					fmt.Fprintln(
						out,
						"PASS: installer continuation armed; installation resumes after maintenance",
					)
				} else {
					fmt.Fprintln(
						out,
						"ACTION: after maintenance, run the installer again to finish recovery setup",
					)
				}
				fmt.Fprintln(
					out,
					"STAGE: arming one-shot maintenance boot",
				)

				systemPath, err := filepath.EvalSymlinks(
					filepath.Join(installedRoot, "nix/var/nix/profiles/system"),
				)
				if err != nil {
					return fail(
						errOut,
						fmt.Errorf(
							"resolve installed system generation: %w",
							err,
						),
					)
				}

				maintenanceNext := filepath.Join(
					systemPath,
					"sw/bin/gjallar-recovery-maintenance-next",
				)

				if err := attached(
					ctx,
					"sudo",
					maintenanceNext,
					installedRoot,
				); err != nil {
					return fail(
						errOut,
						fmt.Errorf(
							"arm recovery-storage maintenance boot: %w",
							err,
						),
					)
				}

				return 0
			}
			if err := provisionRecoveryPartition(ctx, root, s); err != nil {
				return fail(errOut, err)
			}
		} else {
			targetDisk, err := selectFreshTargetDisk(
				ctx,
				ui,
				out,
				opt.targetDisk,
			)
			if err != nil {
				return fail(
					errOut,
					fmt.Errorf(
						"select fresh installation target: %w",
						err,
					),
				)
			}

			result, err := runFreshBareMetal(
				ctx,
				ui,
				root,
				targetDisk,
				s.user.Hostname,
				s.user.Username,
				s.user.DotfilesDir,
				hardware,
				resolvedDevice,
				opt.recovery,
				s.user.RecoveryEnable,
				freshPasswordFiles(s.render),
				hostOverlayToCommit,
				out,
			)
			if err != nil {
				return fail(
					errOut,
					fmt.Errorf(
						"run canonical fresh bare-metal installation: %w",
						err,
					),
				)
			}

			// The Secure Boot continuation below still works on the target.
			defer result.ReleaseTargetState()
			s.recoveryPartition = result.RecoveryPartition
		}
	}

	// Secure Boot continuation is intentionally independent of runRebuild.
	//
	// -no-rebuild means "do not install another NixOS generation"; it must
	// never mean "skip an already-started Secure Boot transaction".
	if s.render.SecureBootEnable {
		next, err := secureboot.Continue(
			ctx,
			ui,
			out,
			resolvedDevice.ModelID,
			s.secureBootFirmware,
			s.user.FirmwarePasswordLock,
		)
		if err != nil {
			return fail(errOut, err)
		}

		// A completed Secure Boot transaction needs no marker, firmware
		// handoff, or reboot. This is especially important on installer reruns.
		if secureboot.NeedsFirmwareReboot(next) {
			if err := attached(
				ctx,
				"sudo", "install",
				"-d", "-m", "0700",
				"/var/lib/gjallarOS",
			); err != nil {
				return fail(
					errOut,
					fmt.Errorf(
						"create installer continuation directory: %w",
						err,
					),
				)
			}

			if err := attached(
				ctx,
				"sudo", "install",
				"-m", "0600",
				"/dev/null",
				installerSecureBootResumeMarker,
			); err != nil {
				return fail(
					errOut,
					fmt.Errorf(
						"arm installer continuation across Secure Boot reboot: %w",
						err,
					),
				)
			}

			fmt.Fprintln(
				out,
				"Installer continuation armed across Secure Boot reboot.",
			)
			fmt.Fprintln(
				out,
				"After Secure Boot is verified, GjallarOS will automatically run the final installer checks.",
			)

			fmt.Fprintln(
				out,
				"Secure Boot firmware instructions acknowledged.",
			)
			fmt.Fprintln(
				out,
				"Rebooting directly into firmware setup...",
			)

			if err := attached(
				ctx,
				"sudo", "systemctl",
				"reboot", "--firmware-setup",
			); err != nil {
				return fail(
					errOut,
					fmt.Errorf("reboot into firmware setup: %w", err),
				)
			}

			return 0
		}
	}

	if runRebuild {
		if s.render.EndpointManagedDevice {
			if s.existing {
				if err := activateJODSEnrollment(ctx, true); err != nil {
					return fail(errOut, err)
				}
				fmt.Fprintln(out, "JODS enrollment submitted; retry timer enabled while approval is pending.")
			} else {
				fmt.Fprintln(
					out,
					"JODS enrollment deferred to the installed GjallarOS lifecycle; installer environment was not enrolled.",
				)
			}
		}
		if s.user.AutoReboot {
			_ = attached(ctx, "sudo", "systemctl", "reboot")
		} else if !s.render.EndpointManagedDevice {
			yes, _ := ui.Confirm(
				ctx,
				"Deployment complete. Reboot now?",
				false,
			)
			if yes {
				_ = attached(ctx, "sudo", "systemctl", "reboot")
			}
		}
	}
	if s.render.EndpointManagedDevice && !runRebuild {
		fmt.Fprintln(out, "JODS configured, not contacted: enrollment waits for a successful installation rebuild.")
	}

	fmt.Fprintln(out, "GjallarOS installation complete.")
	return 0
}

func confirmInsecureJODS(ctx context.Context, ui prompt.UI) error {
	confirmed, err := ui.Confirm(ctx, "SECURITY WARNING: disable TLS certificate verification for this local-development JODS endpoint? This permits machine-in-the-middle attacks.", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("insecure JODS TLS was not explicitly confirmed")
	}
	return nil
}

func activateJODSEnrollment(ctx context.Context, installationSucceeded bool) error {
	if !installationSucceeded {
		return nil
	}
	if err := runJODSCommand(ctx, "sudo", "install", "-d", "-m", "0700", "/var/lib/gjallarOS"); err != nil {
		return fmt.Errorf("prepare installer status directory: %w", err)
	}
	if err := runJODSCommand(ctx, "sudo", "install", "-m", "0600", "/dev/null", "/var/lib/gjallarOS/installation-complete"); err != nil {
		return fmt.Errorf("mark installation complete: %w", err)
	}
	if err := runJODSCommand(ctx, "sudo", "systemctl", "start", "jods-mdm-agent-enroll.service"); err != nil {
		return fmt.Errorf("submit JODS enrollment: %w", err)
	}
	if err := runJODSCommand(ctx, "sudo", "systemctl", "enable", "--now", "jods-mdm-agent-enroll.timer"); err != nil {
		return fmt.Errorf("enable JODS enrollment retry timer: %w", err)
	}
	return nil
}

// liveHostPreparationSkip reports why the running system must not be
// rebuilt, staged or rebooted before the installer operates on its target.
func liveHostPreparationSkip(opt options, resumeState string) string {
	switch {
	case resumeState == installerresume.StateMaintenanceReboot:
		// The GjallarOS generation was deployed before the maintenance boot.
		// The bootstrap /etc/nixos config lacks the flake's helpers, so it
		// rebuilt and switched to the bare base system (e2e-full, 2026-10-05).
		return "Resumed after maintenance; GjallarOS already deployed, live-host rebuild skipped."
	case opt.recovery:
		// Recovery media is a purpose-built execution environment. Never mutate
		// or rebuild the live recovery system before operating on the target.
		return "Recovery environment validated; live-host rebuild skipped."
	case opt.targetDisk != "":
		// Fresh installs run from live media (validateTargetDiskContext).
		// nixos-install builds the target; rebuilding the live system only
		// fills its RAM-backed store and was OOM-killed on 12 GiB machines.
		return "Live installation media; live-host rebuild skipped (nixos-install builds the target)."
	}
	return ""
}

func prepareHost(
	ctx context.Context,
	ui prompt.UI,
	opt options,
	s state,
	originalArgs []string,
	out,
	errOut io.Writer,
) int {
	expected, actual, err := release.Inspect(opt.repo, "/etc/os-release")
	if err != nil {
		return fail(errOut, err)
	}
	if expected != actual {
		fmt.Fprintf(out, "NixOS %s detected; target is %s.\n", actual, expected)
		fmt.Fprintf(
			out,
			"ACTION: staging pinned NixOS %s for the next boot; the live desktop will not be switched in place.\n",
			expected,
		)

		executable, err := os.Executable()
		if err != nil {
			return fail(
				errOut,
				fmt.Errorf(
					"resolve installer executable for release continuation: %w",
					err,
				),
			)
		}

		if err := installerresume.Arm(
			ctx,
			executable,
			opt.repo,
			originalArgs,
			expected,
		); err != nil {
			return fail(
				errOut,
				fmt.Errorf(
					"arm installer release continuation: %w",
					err,
				),
			)
		}

		if err := release.Align(
			ctx,
			expected,
			installerresume.WrapperPath,
		); err != nil {
			return fail(errOut, err)
		}

		fmt.Fprintf(
			out,
			"PASS: NixOS %s staged and installer continuation armed.\n",
			expected,
		)
		fmt.Fprintln(
			out,
			"Rebooting into the staged release; installation will resume automatically.",
		)

		if err := attached(
			ctx,
			"sudo",
			"systemctl",
			"reboot",
		); err != nil {
			return fail(
				errOut,
				fmt.Errorf(
					"reboot into staged NixOS %s: %w; continuation remains armed for the next manual reboot",
					expected,
					err,
				),
			)
		}

		return installerReleaseRebootScheduled
	}
	// A completed GjallarOS installation is flake-owned. Rebuilding the
	// bootstrap /etc/nixos/configuration.nix on a rerun would switch the live
	// machine back to its pre-install base generation before deploying the
	// requested flake generation.
	if s.existing && !opt.forceRedeploy {
		fmt.Fprintln(out, "Existing GjallarOS installation detected; skipping /etc/nixos bootstrap rebuild.")
	} else {
		if s.existing && opt.forceRedeploy {
			fmt.Fprintln(out, "Force redeployment requested; running prerequisite bootstrap despite existing-install detection.")
		}
		configPath := "/etc/nixos/configuration.nix"
		data, err := os.ReadFile(configPath)
		if err != nil {
			return fail(errOut, err)
		}
		plan := bootstrap.Build(data, s.preset)
		if len(plan.Missing) > 0 {
			fmt.Fprintf(out, "Missing helpers/options: %s\nProposed configuration:\n%s\n", strings.Join(plan.Missing, ", "), plan.Updated)
			if !s.user.EndpointManagedDevice && os.Getenv("GJALLAR_INSTALLER_RESUMED") != "1" {
				yes, err := ui.Confirm(ctx, "Apply prerequisite configuration and rebuild?", false)
				if err != nil {
					return fail(errOut, err)
				}
				if !yes {
					return fail(errOut, errors.New("required persistent prerequisites were not approved"))
				}
			}
			if _, err := bootstrap.Apply(ctx, configPath, plan.Updated, time.Now()); err != nil {
				return fail(errOut, err)
			}
		}
	}
	check := s.user.RunUpdateChecks
	if !s.preset && os.Getenv("GJALLAR_INSTALLER_RESUMED") != "1" {
		check, err = ui.Confirm(ctx, "Check for and install firmware updates?", false)
		if err != nil {
			return fail(errOut, err)
		}
	}
	if check && firmware.Available() {
		fmt.Fprintln(out, "PLAN: refresh and apply firmware updates")
		_, _ = firmware.Update(ctx)
	}
	return 0
}

func detectedNixSystem() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64-linux", nil
	case "arm64":
		return "aarch64-linux", nil
	default:
		return "", fmt.Errorf(
			"unsupported machine architecture %q",
			runtime.GOARCH,
		)
	}
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// partitionable is false for an in-place install on an unencrypted root,
// which recovery partitioning cannot resize.
//
// Personal values (account, name, email) are never filled in from this
// machine or the checkout; the prompts show made-up examples instead.
func collectInteractive(ctx context.Context, ui prompt.UI, root string, partitionable, modelSelected, tpm2 bool, managedBlocker string, hardware discovery.Hardware, o discovery.Options, u *config.User) error {
	var err error
	if u.Hostname, err = ui.Value(ctx, "Hostname", "gjallarOS"); err != nil {
		return err
	}
	if u.Username, err = ui.Example(ctx, "Username", "alex", true); err != nil {
		return err
	}
	if err := collectJODSManagement(ctx, ui, managedBlocker, u); err != nil {
		return err
	}
	if u.EndpointManagedDevice {
		localDevelopment, err := ui.Confirm(ctx, "Use a clearly marked local-development JODS server?", false)
		if err != nil {
			return err
		}
		endpointDefault := strings.TrimSpace(u.JODSEndpoint)
		if endpointDefault == "" && !localDevelopment {
			endpointDefault = "https://admin.oss-ad.eu:1666"
		}
		u.JODSEndpoint, err = ui.Value(ctx, "JODS HTTPS endpoint", endpointDefault)
		if err != nil {
			return err
		}
		u.JODSPolicySigningKey, err = ui.Value(ctx, "JODS policy-signing Ed25519 public key (64 hexadecimal characters)", "")
		if err != nil {
			return err
		}
		u.JODSRecoverySigningKey, err = ui.Value(ctx, "JODS recovery-command Ed25519 public key (different 64 hexadecimal characters)", "")
		if err != nil {
			return err
		}
		u.JODSEnrollmentMode, err = ui.Choice(ctx, "JODS enrollment mode", "manual", []string{"auto", "manual", "jade-registry-only"})
		if err != nil {
			return err
		}
		if localDevelopment {
			u.JODSAllowInsecureTLS, err = ui.Confirm(ctx, "Allow a self-signed JODS TLS certificate for local development?", false)
			if err != nil {
				return err
			}
		}
		deviceDefault := "pc"
		if hardware.FormFactor == "laptop" {
			deviceDefault = "laptop"
		}
		u.JODSDeviceClass, err = ui.Choice(ctx, "JODS device class", deviceDefault, []string{"pc", "vm", "laptop", "kiosk", "workstation"})
		if err != nil {
			return err
		}
		u.JODSDesktopProfile, err = ui.Value(ctx, "JODS desktop profile", "hyprland")
		if err != nil {
			return err
		}
		u.JODSDesktopProfile = strings.ToLower(strings.TrimSpace(u.JODSDesktopProfile))
	}
	if err != nil {
		return err
	}
	u.Timezone, err = ui.Value(ctx, "Timezone (IANA name)", "Europe/Berlin")
	if err != nil {
		return err
	}
	u.Locale, err = ui.Value(ctx, "Locale", "en_US.UTF-8")
	if err != nil {
		return err
	}
	layout, err := ui.Value(ctx, "Keyboard layout", "de")
	if err != nil {
		return err
	}
	normalized, err := xkb.Normalize(layout)
	if err != nil {
		return err
	}
	u.KeyboardLayout, u.KeyboardVariant = normalized.Name, normalized.Variant
	u.TouchpadWorkspaceSwipe, err = ui.Confirm(ctx, "Enable three-finger workspace swipes?", true)
	if err != nil {
		return err
	}
	u.ClamshellEnable, err = ui.Confirm(ctx, "Enable dock-aware clamshell mode?", true)
	if err != nil {
		return err
	}
	if err := collectUSBTrust(ctx, ui, modelSelected, partitionable, tpm2, u); err != nil {
		return err
	}
	u.Name, err = ui.Example(ctx, "Full name", "Alex Example", true)
	if err != nil {
		return err
	}
	u.Email, err = ui.Example(ctx, "Git email", "alex@example.com", true)
	if err != nil {
		return err
	}
	u.GitHubUsername, err = ui.Example(ctx, "GitHub username", "alex-example", false)
	if err != nil {
		return err
	}
	u.DotfilesDir, err = ui.Value(ctx, "Absolute dotfiles path", defaultDotfilesDir(u.Username, root))
	if err != nil {
		return err
	}
	u.Shell, err = ui.Choice(ctx, "Login shell", first(o.Shells), o.Shells)
	if err != nil {
		return err
	}
	u.Editors, err = ui.Multi(ctx, "Editors", []string{"vscodium"}, o.Editors)
	if err != nil {
		return err
	}
	u.Browsers, err = ui.Multi(ctx, "Browsers", []string{first(o.Browsers)}, o.Browsers)
	if err != nil {
		return err
	}
	u.PreferredEditor, err = ui.Value(ctx, "Preferred editor", u.Editors[0])
	if err != nil {
		return err
	}
	u.PreferredBrowser, err = ui.Value(ctx, "Preferred browser", u.Browsers[0])
	if err != nil {
		return err
	}
	if err := collectProjectTools(ctx, ui, u); err != nil {
		return err
	}
	// Theme selection belongs to the desktop shell after installation.
	// Keep the default theme only as the bootstrap rendering contract.
	if u.Theme == "" {
		u.Theme = first(o.Themes)
	}
	u.PrintingEnable, err = ui.Confirm(ctx, "Enable printing and scanning support?", false)
	if err != nil {
		return err
	}
	u.NetworkPrintingEnable = false
	if u.PrintingEnable {
		u.NetworkPrintingEnable, err = ui.Confirm(ctx, "Enable network printer discovery?", false)
		if err != nil {
			return err
		}
	}
	if err := collectApps(ctx, ui, u); err != nil {
		return err
	}
	if err := collectFirefoxAddons(ctx, ui, u); err != nil {
		return err
	}
	if err := collectRecovery(ctx, ui, partitionable, u); err != nil {
		return err
	}
	// Device-specific hardware policy is resolved through ODDC.
	// Do not ask users to manually select a vendor/model that hardware
	// discovery and the resolved ODDC device graph already determine.
	u.WriteConfig = true
	u.RunRebuild = true
	return nil
}

// collectRecovery asks for the recovery boot entry and, where the root can
// take one, the recovery partition.
func collectRecovery(ctx context.Context, ui prompt.UI, partitionable bool, u *config.User) error {
	var err error
	u.RecoveryEnable, err = ui.Confirm(ctx, "Install the trusted local GjallarOS recovery/JODS boot entry?", true)
	if err != nil {
		return err
	}
	u.RecoveryPartitionEnable = false
	if u.RecoveryEnable && !partitionable {
		fmt.Fprintln(ui.Out, "No recovery partition: this root is not on encrypted Btrfs, which recovery partitioning needs. The recovery boot entry is still installed.")
	} else if u.RecoveryEnable {
		u.RecoveryPartitionEnable, err = ui.Confirm(
			ctx,
			"Create and maintain a dedicated GjallarOS recovery partition (recommended):",
			true,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// collectAIServer points AI at a central Ollama server. Nothing runs or is
// downloaded locally then; the server picks hardware and holds the model.
func collectAIServer(ctx context.Context, ui prompt.UI, u *config.User) error {
	u.OverrideAISelection, u.OverrideModelWith = false, ""
	fmt.Fprintln(ui.Out, "The central AI server must use HTTPS and a bearer token (e.g. Ollama behind a TLS proxy that checks it). The token is asked for later, hidden.")
	for {
		endpoint, err := ui.Value(ctx, "AI server URL", "https://192.168.8.205")
		if err != nil {
			return err
		}
		model, err := ui.Value(ctx, "Ollama model on the server", "qwen3-coder:30b")
		if err != nil {
			return err
		}
		tokens, err := ui.Value(ctx, "Context tokens", "32768")
		if err != nil {
			return err
		}
		u.AIEndpoint, u.AIRemoteModel = strings.TrimRight(strings.TrimSpace(endpoint), "/"), strings.TrimSpace(model)
		u.AIRemoteContextTokens, err = strconv.Atoi(strings.TrimSpace(tokens))
		if err == nil {
			err = config.ValidateAIEndpoint(*u)
		}
		if err == nil {
			return nil
		}
		fmt.Fprintf(ui.Out, "%v\n", err)
	}
}

// ensureAIToken asks for the central AI server token, hidden, unless one is
// already staged or sealed. It stays root-only at aitoken.Pending until the
// installed system seals it with systemd-creds.
func ensureAIToken(ctx context.Context, out io.Writer) error {
	if privilegedFileExists(ctx, aitoken.Pending) || privilegedFileExists(ctx, aitoken.Sealed) {
		fmt.Fprintln(out, "Existing AI server token retained.")
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open terminal for AI server token (or stage it root-only at %s): %w", aitoken.Pending, err)
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)
	for {
		token, err := credential.ReadSecret(tty, reader, out, "AI server token (hidden): ")
		if err != nil {
			return fmt.Errorf("read AI server token: %w", err)
		}
		if err := aitoken.Validate(token); err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		return aitoken.Store(ctx, token, aitoken.Pending)
	}
}

// collectApps asks each catalogue app's installer question; apps without
// one take their default. Some apps ask follow-up questions.
func collectApps(ctx context.Context, ui prompt.UI, u *config.User) error {
	catalogue, err := apps.Catalogue()
	if err != nil {
		return err
	}
	followUp := map[string]func(context.Context, prompt.UI, *config.User) error{
		"ai":        collectAI,
		"tailscale": collectTailscale,
	}
	u.Apps = []string{}
	u.Tailscale = nil
	u.AIAgentMode, u.OverrideAISelection, u.OverrideModelWith = "", false, ""
	u.AIEndpoint, u.AIRemoteModel, u.AIRemoteContextTokens = "", "", 0
	for _, app := range catalogue {
		selected := app.Default
		if app.Installer != "" {
			selected, err = ui.Confirm(ctx, app.Installer, app.Default)
			if err != nil {
				return err
			}
		}
		if !selected {
			continue
		}
		u.Apps = append(u.Apps, app.ID)
		if collect := followUp[app.ID]; collect != nil {
			if err := collect(ctx, ui, u); err != nil {
				return err
			}
		}
	}
	return nil
}

// collectFirefoxAddons asks which add-on collections Firefox gets, then
// one question per ticked collection for its add-ons, so an add-on is only
// offered while its collection is on. Earlier answers are the defaults; an
// add-on that was locked stays locked while it is still ticked.
func collectFirefoxAddons(ctx context.Context, ui prompt.UI, u *config.User) error {
	previous := apps.CompleteFirefoxAddons(u.FirefoxAddons)
	previousStore := u.FirefoxStoreAddons
	u.FirefoxAddons, u.FirefoxStoreAddons = previous, []apps.FirefoxStoreAddon{}
	if !slices.Contains(u.Browsers, "firefox") {
		return nil
	}
	collections := apps.FirefoxCollections()
	items := []prompt.Item{}
	for _, c := range collections {
		items = append(items, prompt.Item{Value: c.ID, Name: c.Name, Description: c.Description, On: previous[c.ID].Enable})
	}
	chosen, err := ui.Checklist(ctx, "Firefox add-ons. Tick the collections you want to pick from.", items)
	if err != nil {
		return err
	}
	for _, c := range collections {
		choice := apps.FirefoxCollectionChoice{Addons: maps.Clone(previous[c.ID].Addons)}
		u.FirefoxAddons[c.ID] = choice
		if !slices.Contains(chosen, c.ID) {
			// Off, but the add-on choices stay for when it is turned on again.
			continue
		}
		items = []prompt.Item{}
		for _, a := range c.Addons {
			items = append(items, prompt.Item{Value: a.ID, Name: a.Name, Description: a.Description, On: choice.Addons[a.ID] != apps.AddonOff})
		}
		picked, err := ui.Checklist(ctx, c.Name+": tick the add-ons you want.", items)
		if err != nil {
			return err
		}
		for _, a := range c.Addons {
			switch {
			case !slices.Contains(picked, a.ID):
				choice.Addons[a.ID] = apps.AddonOff
			case choice.Addons[a.ID] != apps.AddonLocked:
				choice.Addons[a.ID] = apps.AddonOn
			}
		}
		// A collection with nothing ticked stays off.
		choice.Enable = len(picked) > 0
		u.FirefoxAddons[c.ID] = choice
	}
	return collectStoreAddons(ctx, ui, u, previousStore)
}

// pinStoreAddon is swapped in tests; the installer asks addons.mozilla.org.
var pinStoreAddon = firefoxstore.Pin

// collectStoreAddons asks for more add-ons by ID. A listed add-on that was
// pinned before keeps its pin; a new one is pinned to its current version
// after the user has seen what it may do.
func collectStoreAddons(ctx context.Context, ui prompt.UI, u *config.User, previous []apps.FirefoxStoreAddon) error {
	const storeLabel = "More add-ons from addons.mozilla.org by add-on ID, comma-separated (optional). about:support lists the ID of each installed add-on."
	ids := []string{}
	for _, a := range previous {
		ids = append(ids, a.ID)
	}
	label, answer := storeLabel, strings.Join(ids, ", ")
	for {
		var err error
		answer, err = ui.Value(ctx, label, answer)
		if err != nil {
			return err
		}
		store, err := storeAddonsFor(ctx, ui, u, previous, answer)
		if err == nil {
			u.FirefoxStoreAddons = store
			return nil
		}
		if errors.Is(err, prompt.ErrCancelled) || ctx.Err() != nil {
			return err
		}
		label = err.Error() + "\n\n" + storeLabel
	}
}

func storeAddonsFor(ctx context.Context, ui prompt.UI, u *config.User, previous []apps.FirefoxStoreAddon, answer string) ([]apps.FirefoxStoreAddon, error) {
	store := []apps.FirefoxStoreAddon{}
	for _, id := range strings.Split(answer, ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if i := slices.IndexFunc(previous, func(a apps.FirefoxStoreAddon) bool { return a.ID == id }); i >= 0 {
			a := previous[i]
			if a.Mode == apps.AddonOff {
				a.Mode = apps.AddonOn
			}
			store = append(store, a)
			continue
		}
		// Check the ID before asking the network about it.
		if err := apps.CheckStoreAddonID(id); err != nil {
			return nil, fmt.Errorf("firefoxStoreAddons: %w", err)
		}
		if slices.ContainsFunc(store, func(a apps.FirefoxStoreAddon) bool { return a.ID == id }) {
			return nil, fmt.Errorf("firefoxStoreAddons: %q listed twice", id)
		}
		a, err := pinStoreAddon(ctx, id, apps.AddonOn)
		if err != nil {
			return nil, err
		}
		ok, err := ui.Confirm(ctx, fmt.Sprintf("Install %s %s (%s)? It may use: %s", a.Name, a.Version, a.ID, permissionList(a.Permissions)), true)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s left out; remove it from the list or confirm it", id)
		}
		store = append(store, a)
	}
	if err := apps.ValidateFirefoxAddons(u.FirefoxAddons, store); err != nil {
		return nil, err
	}
	return store, nil
}

func permissionList(permissions []string) string {
	if len(permissions) == 0 {
		return "nothing special"
	}
	return strings.Join(permissions, ", ")
}

// aiMemoryGB is swapped in tests; the installer runs on the target machine.
var aiMemoryGB = profile.MemoryGB

func collectAI(ctx context.Context, ui prompt.UI, u *config.User) error {
	backend, choices := "local", []string{"local", "central-server"}
	ramGB, err := aiMemoryGB()
	if err != nil {
		return err
	}
	lowMemory := !profile.LocalFits(ramGB)
	if lowMemory {
		fmt.Fprintf(ui.Out, "No local AI model fits %d GiB RAM; local AI needs 8 GiB. Use a central server, turn AI off, or name a small model yourself.\n", ramGB)
		backend, choices = "off", []string{"off", "central-server", "local"}
	}
	backend, err = ui.Choice(ctx, "AI backend", backend, choices)
	if err != nil {
		return err
	}
	if backend == "off" {
		u.Apps = slices.DeleteFunc(u.Apps, func(id string) bool { return id == "ai" })
		return nil
	}
	if backend == "central-server" {
		if err := collectAIServer(ctx, ui, u); err != nil {
			return err
		}
	} else {
		u.OverrideAISelection = lowMemory
		if !lowMemory {
			u.OverrideAISelection, err = ui.Confirm(ctx, "Override automatic hardware-aware AI model selection?", false)
			if err != nil {
				return err
			}
		}
		if u.OverrideAISelection {
			model := "qwen2.5-coder:14b"
			if lowMemory {
				model = "qwen2.5-coder:1.5b"
			}
			for {
				value, err := ui.Value(ctx, "Exact Ollama model identifier", model)
				if err != nil {
					return err
				}
				override, err := profile.NewOverride(true, value)
				if err == nil {
					u.OverrideModelWith = override.Model
					break
				}
				fmt.Fprintf(ui.Out, "%v\n", err)
			}
		}
	}
	u.AIAgentMode, err = ui.Choice(ctx, "AI permission profile", "workspace", []string{"workspace", "owner-conservative", "owner-full-local"})
	return err
}

func collectTailscale(ctx context.Context, ui prompt.UI, u *config.User) error {
	t := &config.TailscaleIntent{HomeSubnets: []string{}, TrustedWifis: []string{}, SiteRouterTargets: []string{}}
	u.Tailscale = t

	ask := func(label, def string, set func(string) error) error {
		for {
			value, err := ui.Value(ctx, label, def)
			if err != nil {
				return err
			}
			err = set(value)
			if err == nil {
				err = config.ValidateTailscale(t)
			}
			if err != nil {
				fmt.Fprintf(ui.Out, "%v\n", err)
				continue
			}
			return nil
		}
	}

	if err := ask("Home LAN subnets kept out of the tunnel at home (comma-separated)", "192.168.8.0/24", func(v string) (err error) {
		t.HomeSubnets, err = config.SplitList(v)
		return err
	}); err != nil {
		return err
	}

	vpn, err := ui.Confirm(ctx, "Use a Tailscale exit node as VPN on networks that are not yours?", false)
	if err != nil || !vpn {
		return err
	}
	for t.ExitNode == "" {
		// The tailnet is not reachable before the first login, so "auto"
		// picks the home router's exit node at runtime.
		if err := ask("Exit node (auto = your home router's, or a tailnet host name or 100.x address)", "auto", func(v string) error {
			t.ExitNode = strings.TrimSpace(v)
			return nil
		}); err != nil {
			return err
		}
	}
	if err := ask("Your Wi-Fi names where no VPN is needed, comma-separated, quote names with commas (e.g. home-5Ghz, home-2.4Ghz, \"cafe, upstairs\")", "", func(v string) (err error) {
		t.TrustedWifis, err = config.SplitList(v)
		return err
	}); err != nil {
		return err
	}
	if len(t.TrustedWifis) > 0 {
		// e.g. a relative's Wi-Fi: its printer stays usable while the
		// internet still goes through home. gjallarctl vpn trust-wifi
		// --exit-node picks another node per Wi-Fi later.
		if err := ask("Of these, Wi-Fis that still send the internet through the exit node (comma-separated, empty = none)", "", func(v string) error {
			names, err := config.SplitList(v)
			t.WifiExitNodes = nil
			for _, ssid := range names {
				if t.WifiExitNodes == nil {
					t.WifiExitNodes = map[string]string{}
				}
				t.WifiExitNodes[ssid] = t.ExitNode
			}
			return err
		}); err != nil {
			return err
		}
	}
	// Any subnet router of the tailnet counts, a friend's too: the tailnet
	// admin approved its routes, and the signed ping shows it is here.
	t.RouterHome, err = ui.Confirm(ctx, "Switch the VPN off on any LAN where a subnet router of your tailnet is (yours or a friend's in the same tailnet)?", true)
	if err != nil || t.RouterHome {
		return err
	}
	t.SiteRouterTrust, err = ui.Confirm(ctx, "Then switch it off near such a router only when it reaches your home LAN?", true)
	if err != nil || !t.SiteRouterTrust {
		return err
	}
	return ask("Home hosts that router must reach (host:port, comma-separated)", config.DefaultSiteRouterTarget(t.HomeSubnets), func(v string) (err error) {
		t.SiteRouterTargets, err = config.SplitList(v)
		return err
	})
}

func collectProjectTools(ctx context.Context, ui prompt.UI, u *config.User) error {
	// Canonical installer intent is the generic webApplications list.
	// Legacy Plane/Draw.io fields are populated by normalization only as a
	// temporary compatibility bridge for older generated-state consumers.
	u.WebApplications = nil
	u.PlaneEnable = false
	u.PlaneHost = ""
	u.DrawioEnable = false
	u.DrawioSelfHosted = false
	u.DrawioHost = ""

	for _, id := range config.SupportedWebApplicationIDs() {
		label := id

		switch id {
		case config.WebApplicationPlane:
			label = "Plane"
		case config.WebApplicationDrawio:
			label = "Draw.io"
		case config.WebApplicationTeams:
			label = "Microsoft Teams"
		}

		enabled, err := ui.Confirm(
			ctx,
			"Enable "+label+" web application integration?",
			false,
		)
		if err != nil {
			return err
		}
		if !enabled {
			continue
		}

		application := config.WebApplicationIntent{ID: id}

		switch id {
		case config.WebApplicationPlane:
			application.Endpoint, err = askEndpoint(
				ctx,
				ui,
				"Plane endpoint (blank skips Plane)",
				checkEndpoint,
			)
			if err != nil {
				return err
			}
			if application.Endpoint == "" {
				continue
			}

		case config.WebApplicationDrawio:
			selfHosted, err := ui.Confirm(
				ctx,
				"Use a self-hosted Draw.io server?",
				false,
			)
			if err != nil {
				return err
			}

			if selfHosted {
				application.Endpoint, err = askEndpoint(
					ctx,
					ui,
					"Draw.io endpoint (blank uses the public diagrams.net)",
					checkEndpoint,
				)
				if err != nil {
					return err
				}
			}

		case config.WebApplicationTeams:
			// Teams owns its default service endpoint.

		default:
			return fmt.Errorf(
				"unsupported web application integration: %q",
				id,
			)
		}

		u.WebApplications = append(
			u.WebApplications,
			application,
		)
	}

	var err error

	u.NextcloudEnable, err = ui.Confirm(
		ctx,
		"Enable Nextcloud integration?",
		false,
	)
	if err != nil {
		return err
	}

	if u.NextcloudEnable {
		u.NextcloudHost, err = askEndpoint(
			ctx,
			ui,
			"Nextcloud server (blank skips Nextcloud)",
			func(host string) error {
				probe := config.User{NextcloudEnable: true, NextcloudHost: host}
				return config.NormalizeProjectTools(&probe)
			},
		)
		if err != nil {
			return err
		}
		u.NextcloudEnable = u.NextcloudHost != ""
	}

	return config.NormalizeProjectTools(u)
}

// askEndpoint asks for label until check accepts the answer, so an invalid
// endpoint is asked again instead of failing the whole install. A blank answer
// returns "" so the caller can skip the service; re-asking it trapped anyone
// who said yes without having a server.
func askEndpoint(ctx context.Context, ui prompt.UI, label string, check func(string) error) (string, error) {
	for {
		value, err := ui.Value(ctx, label, "")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			return "", nil
		}
		if err := check(value); err != nil {
			fmt.Fprintf(ui.Out, "%v\n", err)
			continue
		}
		return value, nil
	}
}

func checkEndpoint(endpoint string) error {
	_, err := config.NormalizeExternalServiceEndpoint(endpoint)
	return err
}

// defaultDotfilesDir is where the new user's checkout lives. The checkout the
// installer runs from only counts when it already sits in that user's home;
// otherwise it names the installing account, not the one being created.
func defaultDotfilesDir(username, root string) string {
	home := filepath.Join("/home", username)
	if rel, err := filepath.Rel(home, filepath.Clean(root)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.Clean(root)
	}
	return filepath.Join(home, "Documents", "gjallarOS")
}

func normalizePreset(u *config.User, root string) {
	if u.DotfilesDir == "" {
		u.DotfilesDir = defaultDotfilesDir(u.Username, root)
	}
	u.DotfilesDir = strings.ReplaceAll(u.DotfilesDir, "usernamehere", u.Username)
	if normalized, err := xkb.Normalize(u.KeyboardLayout); err == nil {
		u.KeyboardLayout, u.KeyboardVariant = normalized.Name, normalized.Variant
	}
}

func disableTPMDependentSecurity(u *config.User) {
	u.SecureBootPrompt = false
	u.SecureBootEnable = false
	u.LUKSTPM2Enable = false
	u.JODSPrebootLockEnable = false
	u.USBTrustEnforce = false
	u.USBTrustTPMHandle = ""
}

func normalizeManagementSafety(u *config.User) {
	if u.EndpointManagedDevice {
		// A managed endpoint must never enter the managed state with a weaker
		// local boot/recovery posture. These are one security contract.
		u.SecureBootEnable = true
		u.LUKSTPM2Enable = true
		u.RecoveryEnable = true
		u.RecoveryPartitionEnable = true
		u.JODSPrebootLockEnable = true
		return
	}

	u.JODSPrebootLockEnable = false
}

func configureWeatherLocation(ctx context.Context, ui prompt.UI, u *config.User, out io.Writer) error {
	configuredCity := strings.TrimSpace(u.WeatherCity)
	configuredCountry := strings.TrimSpace(u.WeatherCountry)
	if configuredCity != "" && configuredCountry != "" {
		u.WeatherCity = configuredCity
		u.WeatherCountry = configuredCountry
		fmt.Fprintf(out, "Weather location configured: %s, %s\n", configuredCity, configuredCountry)
		return nil
	}

	detected, detectErr := detectNetworkLocation(ctx)
	if detectErr == nil {
		fmt.Fprintf(out, "Approximate network location detected: %s, %s\n", detected.City, detected.Country)
		correct, err := ui.Confirm(ctx, "Is this the correct weather location: "+detected.City+", "+detected.Country+"?", true)
		if err != nil {
			return err
		}
		if correct {
			u.WeatherCity, u.WeatherCountry = detected.City, detected.Country
			return nil
		}
	} else {
		fmt.Fprintf(out, "Network location detection unavailable: %v\n", detectErr)
	}

	city, err := ui.Value(ctx, "Weather city", u.WeatherCity)
	if err != nil {
		return err
	}
	country, err := ui.Value(ctx, "Weather country", u.WeatherCountry)
	if err != nil {
		return err
	}
	city, country = strings.TrimSpace(city), strings.TrimSpace(country)
	if city == "" || country == "" {
		return errors.New("weather city and country are required")
	}
	u.WeatherCity, u.WeatherCountry = city, country
	return nil
}

func configureRecoveryProvisioning(ctx context.Context, ui prompt.UI, opt options, s *state) error {
	if !s.user.RecoveryEnable {
		return nil
	}

	// Canonical fresh installation owns recovery partition #3 on the
	// selected target disk. It must never ask the user for a second GPT
	// disk or for GjallarOS release-signing private/public key files.
	//
	// Release signing and per-device recovery identity are separate trust
	// roles. Fresh storage provisioning only creates the canonical recovery
	// storage here.
	if !s.existing {
		s.recoveryDisk = ""
		s.recoveryPartition = ""
		s.recoverySigningKey = ""
		s.recoverySigningPublicKey = ""
		return nil
	}
	if opt.recoveryDisk != "" && opt.recoveryPartition != "" {
		return errors.New("choose either --recovery-disk or --recovery-partition, not both")
	}
	explicitTarget := opt.recoveryDisk != "" || opt.recoveryPartition != ""
	s.recoveryDisk = opt.recoveryDisk
	s.recoveryPartition = opt.recoveryPartition
	s.recoverySigningKey = opt.recoverySigningKey
	s.recoverySigningPublicKey = opt.recoverySigningPublicKey
	if s.recoveryPartition == "" {
		s.recoveryPartition = findRecoveryPartition(ctx)
	}

	// Existing encrypted-Btrfs installations may provision recovery storage,
	// but the running root is never resized in place. GJAL-67 discovery and
	// planning are read-only on the live system; any required contraction is
	// executed only from the trusted one-shot maintenance context with the
	// target root mounted at /mnt.
	//
	// An already-existing dedicated recovery partition remains usable without
	// entering the resize path.

	// recoveryPartitionEnable authorizes creation/resizing of dedicated
	// recovery storage. An already-existing canonical recovery partition
	// remains usable even when automatic partition provisioning is disabled.
	if s.recoveryPartition == "" &&
		!s.user.RecoveryPartitionEnable &&
		!explicitTarget {
		if s.user.EndpointManagedDevice && !s.existing {
			return errors.New(
				"managed fresh installation requires creation of an independent recovery partition",
			)
		}
		return nil
	}

	if explicitTarget {
		if !filepath.IsAbs(s.recoverySigningKey) {
			return errors.New("--recovery-signing-key must be an absolute runtime path")
		}
		if !filepath.IsAbs(s.recoverySigningPublicKey) {
			return errors.New("--recovery-signing-public-key must be an absolute runtime path")
		}
		if !s.user.SecureBootEnable {
			return errors.New("physical recovery partition provisioning requires Secure Boot")
		}
		return nil
	}
	if s.recoveryPartition != "" {
		// After the maintenance boot the partition is the one this install
		// made for itself and is still empty. Filling it is why the run
		// continues, so asking again would only let the default undo it.
		yes := os.Getenv("GJALLAR_INSTALLER_RESUME_STATE") == installerresume.StateMaintenanceReboot
		if !yes {
			var err error
			yes, err = ui.Confirm(ctx, "Install/update the recovery environment on "+s.recoveryPartition+" after a successful rebuild? That partition will be formatted.", false)
			if err != nil {
				return err
			}
		}
		if !yes {
			s.recoveryPartition = ""
			if s.user.EndpointManagedDevice && !s.existing {
				return errors.New("managed fresh installation requires an independent recovery partition")
			}
			return nil
		}
	} else {
		// Fresh canonical installation owns recovery partition #3 on the
		// validated target disk. Do not ask for a second recovery disk here.
		//
		// Existing-layout GJAL-65 resize orchestration is a separate path and
		// still rediscovers the actual Btrfs/LUKS/GPT topology before mutation.
		s.recoveryDisk = ""
	}
	// As on a fresh install, the image is built here and signed with this
	// device's Secure Boot key. The offline release key pair is only used
	// when an operator passes both paths.
	if (s.recoverySigningKey == "") != (s.recoverySigningPublicKey == "") {
		return errors.New("pass both --recovery-signing-key and --recovery-signing-public-key, or neither")
	}
	if s.recoverySigningKey != "" && !filepath.IsAbs(s.recoverySigningKey) {
		return errors.New("recovery signing key path must be absolute")
	}
	if s.recoverySigningPublicKey != "" && !filepath.IsAbs(s.recoverySigningPublicKey) {
		return errors.New("recovery signing public-key path must be absolute")
	}
	if !s.user.SecureBootEnable {
		return errors.New("physical recovery partition provisioning requires Secure Boot")
	}
	return nil
}

// mountSourceDevice returns the device of a findmnt SOURCE value. A Btrfs
// subvolume root is reported as "/dev/mapper/cryptroot[/@]" unless findmnt
// runs with --nofsroot; the suffix is never part of the device name.
func mountSourceDevice(source string) string {
	source = strings.TrimSpace(source)
	if i := strings.IndexByte(source, '['); i > 0 && strings.HasSuffix(source, "]") {
		source = source[:i]
	}
	return source
}

// rootEncrypted reports whether mountpoint sits on a device-mapper (LUKS)
// mapping, the first thing discoverInstalledRecoveryTopology requires.
func rootEncrypted(ctx context.Context, mountpoint string) bool {
	mounted, err := exec.CommandContext(ctx, "findmnt", "-nvro", "SOURCE", "--target", mountpoint).Output()
	return err == nil && strings.HasPrefix(mountSourceDevice(string(mounted)), "/dev/mapper/")
}

// collectJODSManagement asks for JODS management only where the managed
// security contract (Secure Boot, TPM2 unlock, recovery, preboot lock; see
// normalizeManagementSafety) can hold. Elsewhere the install would stop at
// the Secure Boot check after the whole interview.
func collectJODSManagement(ctx context.Context, ui prompt.UI, blocker string, u *config.User) error {
	if blocker != "" {
		u.EndpointManagedDevice = false
		fmt.Fprintf(ui.Out, "No JODS management: %s\n", blocker)
		return nil
	}
	var err error
	u.EndpointManagedDevice, err = ui.Confirm(ctx, "Manage this machine with JODS?", false)
	return err
}

// jodsManagementBlocker names why this machine cannot be a managed
// endpoint, or returns "" when it can.
func jodsManagementBlocker(secureBootSupported, tpm2 bool) string {
	switch {
	case !tpm2:
		return "a managed machine needs TPM2, and none was detected."
	case !secureBootSupported:
		return "a managed machine needs GjallarOS Secure Boot, and ODDC has no Secure Boot setup for this machine."
	}
	return ""
}

// collectUSBTrust offers USB trust review only with an ODDC model: the
// system refuses USB trust without one, since the model names the internal
// devices that must stay trusted. Blocking is offered only with TPM2 (the
// approved-device list is signed by a TPM key the system creates on first
// boot) and an encrypted root (turning blocking off asks for the disk
// passphrase).
func collectUSBTrust(ctx context.Context, ui prompt.UI, modelSelected, encryptedRoot, tpm2 bool, u *config.User) error {
	u.USBTrustEnforce = false
	if !modelSelected {
		u.USBGuardEnable = false
		u.USBTrustTPMHandle = ""
		fmt.Fprintln(ui.Out, "No USB trust review: ODDC has no model for this machine, and USB trust needs one to know its internal devices.")
		return nil
	}
	var err error
	u.USBGuardEnable, err = ui.Confirm(ctx, "Enable USB trust review? New USB devices are logged and you can approve them.", false)
	if err != nil || !u.USBGuardEnable {
		u.USBTrustTPMHandle = ""
		return err
	}
	if !tpm2 {
		u.USBTrustTPMHandle = ""
		fmt.Fprintln(ui.Out, "USB blocking stays off: it needs TPM2 to sign the list of approved devices.")
		return nil
	}
	if u.USBTrustTPMHandle == "" {
		u.USBTrustTPMHandle = config.DefaultUSBTrustTPMHandle
	}
	if !encryptedRoot {
		fmt.Fprintln(ui.Out, "USB blocking stays off: turning it off again needs the disk passphrase, and this root is not encrypted.")
		return nil
	}
	fmt.Fprintln(ui.Out, "With blocking on, only built-in devices and devices you approve work.")
	fmt.Fprintln(ui.Out, "A USB keyboard or mouse that is not built in stays blocked until you approve it with the built-in keyboard.")
	u.USBTrustEnforce, err = ui.Confirm(ctx, "Block unknown USB devices from the first boot?", false)
	return err
}

func discoverInstalledRecoveryTopology(
	ctx context.Context,
	installedRoot string,
) (recoveryresize.Topology, error) {
	mounted, err := exec.CommandContext(
		ctx,
		"findmnt",
		"-nvro",
		"SOURCE",
		"--target",
		installedRoot,
	).Output()
	if err != nil {
		return recoveryresize.Topology{}, fmt.Errorf(
			"discover installed root mapping: %w",
			err,
		)
	}

	mapping := mountSourceDevice(string(mounted))
	if !strings.HasPrefix(mapping, "/dev/mapper/") {
		return recoveryresize.Topology{}, fmt.Errorf(
			"recovery partitioning currently requires an encrypted Btrfs root; mounted root source is %q",
			mapping,
		)
	}

	mappingName := filepath.Base(mapping)

	if err := attached(ctx, "sudo", "-v"); err != nil {
		return recoveryresize.Topology{}, fmt.Errorf("authorize encrypted-root inspection: %w", err)
	}

	status, err := exec.CommandContext(
		ctx,
		"sudo",
		"-n",
		"cryptsetup",
		"status",
		mappingName,
	).Output()
	if err != nil {
		return recoveryresize.Topology{}, fmt.Errorf(
			"inspect installed LUKS mapping %s: %w",
			mappingName,
			err,
		)
	}

	rootPartition := ""
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 && fields[0] == "device:" {
			rootPartition = fields[1]
			break
		}
	}

	if rootPartition == "" {
		return recoveryresize.Topology{}, fmt.Errorf(
			"cryptsetup status for %s did not report a backing device",
			mappingName,
		)
	}

	resolvedRoot, err := filepath.EvalSymlinks(rootPartition)
	if err != nil {
		return recoveryresize.Topology{}, fmt.Errorf(
			"resolve installed root partition %s: %w",
			rootPartition,
			err,
		)
	}

	parentRaw, err := exec.CommandContext(
		ctx,
		"lsblk",
		"-dnro",
		"PKNAME",
		resolvedRoot,
	).Output()
	if err != nil {
		return recoveryresize.Topology{}, fmt.Errorf(
			"resolve installed root parent disk: %w",
			err,
		)
	}

	parent := strings.TrimSpace(string(parentRaw))
	if parent == "" {
		return recoveryresize.Topology{}, fmt.Errorf(
			"installed root partition %s has no parent disk",
			resolvedRoot,
		)
	}
	if !strings.HasPrefix(parent, "/dev/") {
		parent = filepath.Join("/dev", parent)
	}

	topology, err := recoveryresize.DiscoverTopology(
		ctx,
		recoveryresize.SystemRunner(),
		recoveryresize.DiscoveryInput{
			RootMountpoint:            installedRoot,
			ExpectedDiskPath:          parent,
			ExpectedRootPartitionPath: resolvedRoot,
			ExpectedMappingPath:       mapping,
		},
	)
	if err != nil {
		return recoveryresize.Topology{}, err
	}

	return topology, nil
}

func prepareInstalledRecoveryStorage(
	ctx context.Context,
	ui prompt.UI,
	out io.Writer,
	installedRoot string,
	s *state,
) (bool, error) {
	topology, err := discoverInstalledRecoveryTopology(ctx, installedRoot)
	if err != nil {
		return false, err
	}

	plan, err := recoveryprovision.BuildExistingPlan(
		ctx,
		recoveryresize.SystemRunner(),
		topology,
	)
	var present *recoveryprovision.RecoveryPresentError
	if errors.As(err, &present) {
		s.recoveryPartition = present.Partition
		fmt.Fprintf(
			out,
			"PASS: JODS-RECOVERY already present at %s; no storage changes were made\n",
			present.Partition,
		)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"build existing-layout recovery plan: %w",
			err,
		)
	}

	result, err := recoveryprovision.Prepare(
		ctx,
		recoveryresize.SystemRunner(),
		recoveryprovision.Input{
			Plan:                      plan,
			UI:                        ui,
			Out:                       out,
			RootMountpoint:            "/",
			ExpectedRootPartitionPath: topology.RootPartitionPath,
			ExpectedMappingPath:       topology.MappingPath,
			Unattended:                s.user.UnattendedInstall,
		},
	)
	if err != nil {
		return false, err
	}

	switch result.Status {
	case recoveryprovision.StatusReady:
		s.recoveryPartition = result.RecoveryPartition
		fmt.Fprintf(
			out,
			"PASS: existing encrypted-Btrfs layout has exact 12 GiB JODS-RECOVERY at %s\n",
			result.RecoveryPartition,
		)
		return false, nil

	case recoveryprovision.StatusResizeRequired:
		if !s.user.UnattendedInstall {
			confirmed, err := ui.Confirm(
				ctx,
				"Recovery storage requires shrinking the encrypted Btrfs root. Reboot once into trusted maintenance mode and continue safely?",
				false,
			)
			if err != nil {
				return false, err
			}
			if !confirmed {
				return false, errors.New(
					"recovery-storage maintenance was not authorized; no disk changes were made",
				)
			}
		}

		fmt.Fprintln(
			out,
			"PASS: live root inspection complete; no mutation was performed",
		)
		fmt.Fprintln(
			out,
			"PLAN: install and boot the one-shot /mnt maintenance environment",
		)

		return true, nil

	default:
		return false, fmt.Errorf(
			"unexpected installed recovery provisioning state %q",
			result.Status,
		)
	}
}

var findRecoveryPartition = func(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "lsblk", "-pnro", "PATH,PARTLABEL,LABEL").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[1] == "JODS-RECOVERY" || fields[1] == "JODSRECOV") {
			return fields[0]
		}
		if len(fields) >= 3 && fields[2] == "JODSRECOV" {
			return fields[0]
		}
	}
	return ""
}

func provisionRecoveryPartition(ctx context.Context, root string, s state) error {
	if s.recoveryDisk == "" && s.recoveryPartition == "" {
		return nil
	}
	createScript := filepath.Join(root, "scripts", "recovery", "create-partition.sh")
	signScript := filepath.Join(root, "scripts", "recovery", "sign-image.sh")
	if s.recoveryDisk != "" {
		if err := attached(ctx, "sudo", createScript, s.recoveryDisk); err != nil {
			return fmt.Errorf("create recovery partition: %w", err)
		}
		s.recoveryPartition = findRecoveryPartition(ctx)
		if s.recoveryPartition == "" {
			return errors.New("recovery partition creation completed but JODS-RECOVERY was not detected")
		}
	}
	// The recovery configuration imports untracked generated/state.nix, so a
	// git+file build never evaluates; build from the staged source as
	// deploy.Apply does. That also works when installer-resume.service runs
	// as root, where Nix refuses the user-owned Git repository (e2e-full,
	// 2026-10-05).
	source, err := flakesource.Stage(root, "")
	if err != nil {
		return fmt.Errorf("build recovery image: %w", err)
	}
	defer source.Close()
	storePath, err := buildStaged(ctx, source, "gjallar-recovery-iso")
	if err != nil {
		return fmt.Errorf("build recovery image: %w", err)
	}
	// install-partition.sh with its tools pinned: the running system need
	// not have xorriso, sbctl, or mkfs.vfat in PATH.
	installTool, err := buildStaged(ctx, source, "gjallar-recovery-install")
	if err != nil {
		return fmt.Errorf("build recovery installer: %w", err)
	}
	installScript := filepath.Join(installTool, "bin", "gjallar-recovery-install")
	images, err := filepath.Glob(filepath.Join(storePath, "iso", "*.iso"))
	if err != nil || len(images) != 1 {
		return fmt.Errorf("recovery build produced %d ISO images", len(images))
	}
	// Canonical provisioning never asks for offline release keys: the image
	// was just built here from the installed source, and install-partition.sh
	// signs its boot chain with this endpoint's Secure Boot key. Release
	// signing applies only when an operator supplied the release key pair.
	if s.recoverySigningKey == "" && s.recoverySigningPublicKey == "" {
		if err := attached(ctx, "sudo", installScript, "--local-build", s.recoveryPartition, images[0]); err != nil {
			return fmt.Errorf("install recovery partition: %w", err)
		}
		return nil
	}
	releaseDir, err := os.MkdirTemp("", "gjallar-recovery-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(releaseDir)
	if err := attached(ctx, signScript, images[0], s.recoverySigningKey, s.recoverySigningPublicKey, releaseDir); err != nil {
		return fmt.Errorf("sign recovery image release: %w", err)
	}
	manifest := filepath.Join(releaseDir, filepath.Base(images[0])+".manifest")
	if err := attached(ctx, "sudo", installScript, s.recoveryPartition, images[0], manifest, manifest+".sig", filepath.Join(releaseDir, "recovery-signing-public.pem")); err != nil {
		return fmt.Errorf("install recovery partition: %w", err)
	}
	return nil
}

// buildStaged evaluates attr and builds it in two nix processes, so the
// evaluator's heap is freed before the build starts; one `nix build` keeps it
// through every compile and ran a 4 GiB machine out of memory.
func buildStaged(ctx context.Context, source flakesource.Source, attr string) (string, error) {
	eval := exec.CommandContext(ctx, "nix", "eval", "--raw", source.Ref(attr)+".drvPath")
	eval.Stderr = os.Stderr
	drv, err := eval.Output()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "nix", "build", strings.TrimSpace(string(drv))+"^*", "--no-link", "--print-out-paths")
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func detectAndRenderState(
	ctx context.Context,
	root string,
	s *state,
	hardware discovery.Hardware,
	resolvedDevice oddc.Resolved,
) error {
	u := s.user
	if u.AIAgentMode == "" {
		u.AIAgentMode = "workspace"
	}
	g, err := graphics.Detect(ctx)
	if err != nil {
		return err
	}
	// Without local AI there is no model to pick. An empty aiModel makes a
	// later switch to local AI stop at the Nix assertion instead of pulling a
	// model sized for some other machine.
	var ai profile.Result
	// A central AI server holds the model; local hardware does not matter.
	if u.HasApp("ai") && u.AIEndpoint == "" {
		// u holds the override from either the preset file or the answers;
		// the file does not exist yet in an interactive install.
		override, err := profile.NewOverride(u.OverrideAISelection, u.OverrideModelWith)
		if err != nil {
			return fmt.Errorf("overrideModelWith: %w", err)
		}
		ai, err = profile.DetectWith(ctx, override)
		if err != nil {
			return err
		}
		if ai.Model == "" {
			// Only a preset gets here; collectAI sets an override. Rebuilds sync
			// apps from user.config.json, so the file itself has to change.
			return fmt.Errorf("user.config.json enables local AI, but no local model fits %d GiB RAM (8 GiB needed): set aiEndpoint, set overrideAiSelection with overrideModelWith, or remove \"ai\" from apps", ai.RAMGB)
		}
	}
	system, err := detectedNixSystem()
	if err != nil {
		return err
	}

	rootPasswordFile := s.render.RootPasswordFile
	userPasswordFile := s.render.UserPasswordFile
	s.render = nixrender.FromUser(u)
	s.render.System = system
	s.render.TouchscreenEnable = s.touchscreen
	s.render.PenTabletEnable = s.penTablet
	s.render.OrientationSensorEnable = s.orientationSensor
	s.render.RootPasswordFile = rootPasswordFile
	s.render.UserPasswordFile = userPasswordFile
	s.render.ODDCModel = resolvedDevice.ModelID
	s.render.GraphicsBusID = g.BusID
	s.render.GraphicsIntegratedBusID = g.IntegratedBusID
	s.render.AIModel = ai.Model
	s.render.AIAccelerationProfile = ai.AccelerationProfile
	s.render.AIContextTokens = ai.ContextTokens
	s.render.AIVRAMMB = ai.VRAMMB
	s.render.WMs = []string{"hyprland"}

	return nil
}

func configureSecrets(context.Context, string, *state, io.Writer) error {
	return nil
}

func controlBinary() string {
	return control.Binary()
}
func controlOutput(ctx context.Context, binary string, stderr io.Writer, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gjallarctl %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
func controlAttached(ctx context.Context, binary string, args ...string) error {
	return attached(ctx, binary, args...)
}
func attached(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func privilegedFileExists(ctx context.Context, path string) bool {
	return exec.CommandContext(ctx, "sudo", "test", "-s", path).Run() == nil
}

// rootPasswordArgs applies a new root hash to the live account on an installed
// host: root already exists there, and mutable NixOS users keep their old hash.
func rootPasswordArgs(persistentInstalledHost bool) []string {
	args := []string{"installer", "local-password", "--username", "root", "--apply"}
	if persistentInstalledHost {
		args = append(args, "--apply-account")
	}
	return args
}

// ensureUserPassword gives the desktop account a password: the greeter, sudo
// and polkit ask for it. A usable password on this host stays. A new, locked
// or empty account gets one; a fresh target always does, since its account
// does not exist yet. NixOS reads hashedPasswordFile only when it creates an
// account, so an existing one gets the hash through chpasswd.
func (s *state) ensureUserPassword(ctx context.Context, persistentInstalledHost bool, out, errOut io.Writer) error {
	username := s.user.Username
	stored, err := credential.Path(username)
	if err != nil {
		return err
	}
	exists, usable, err := credential.AccountStatus(ctx, username)
	if err != nil {
		return err
	}
	if persistentInstalledHost && usable {
		if privilegedFileExists(ctx, stored) {
			s.render.UserPasswordFile = stored
		}
		fmt.Fprintf(out, "Existing password for %s retained.\n", username)
		return nil
	}
	path, err := controlOutput(ctx, s.control, errOut, userPasswordArgs(username, persistentInstalledHost && exists)...)
	if err != nil {
		return err
	}
	lines := strings.Fields(strings.TrimSpace(path))
	if len(lines) > 0 {
		s.render.UserPasswordFile = lines[len(lines)-1]
	}
	fmt.Fprint(out, path)
	return nil
}

func userPasswordArgs(username string, applyAccount bool) []string {
	args := []string{"installer", "local-password", "--username", username, "--apply"}
	if applyAccount {
		args = append(args, "--apply-account")
	}
	return args
}

// freshPasswordFiles lists the hashes a fresh target needs: root for local
// recovery, the user for the greeter.
func freshPasswordFiles(r nixrender.Settings) []string {
	files := []string{r.RootPasswordFile}
	if r.UserPasswordFile != "" {
		files = append(files, r.UserPasswordFile)
	}
	return files
}

func requireNixOS(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "ID=nixos" || line == `ID="nixos"` {
			return nil
		}
	}
	return errors.New("installer must run on NixOS")
}
func existingInstall(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "generated", "state.nix"))
	return err == nil && (strings.Contains(string(data), "Generated by gjallarctl") || strings.Contains(string(data), "Generated by scripts/installation/install.sh"))
}
func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
func validateSelections(u config.User, o discovery.Options) error {
	contains := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	for _, check := range []struct {
		name, value string
		allowed     []string
	}{{"shell", u.Shell, o.Shells}} {
		if !contains(check.allowed, check.value) {
			return fmt.Errorf("unsupported %s: %q", check.name, check.value)
		}
	}
	for _, value := range u.Editors {
		if !contains(o.Editors, value) {
			return fmt.Errorf("unsupported editor: %q", value)
		}
	}
	// A retired browser stays valid in a config that already lists it (an
	// update in place or an old preset); the flake adds Firefox next to it.
	for _, value := range u.Browsers {
		if !contains(o.Browsers, value) && !contains(o.RetiredBrowsers, value) {
			return fmt.Errorf("unsupported browser: %q", value)
		}
	}
	if !filepath.IsAbs(u.DotfilesDir) {
		return fmt.Errorf("dotfiles path must be absolute: %q", u.DotfilesDir)
	}
	return nil
}
func fail(out io.Writer, err error) int {
	// Ctrl-C at a prompt is the user leaving, not a failure; keep any detail
	// that came with it, such as a cleanup that went wrong on the way out.
	if errors.Is(err, context.Canceled) || errors.Is(err, credential.ErrInterrupted) {
		switch err.Error() {
		case context.Canceled.Error(), credential.ErrInterrupted.Error():
			fmt.Fprintln(out, "Installer stopped.")
		default:
			fmt.Fprintln(out, "Installer stopped:", err)
		}
		return 130
	}
	fmt.Fprintln(out, "ERROR:", err)
	return 1
}

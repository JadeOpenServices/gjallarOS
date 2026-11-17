package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/control"
	"io"
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
	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
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
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/firmware"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/hardwareconfig"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
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
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/zonecity"
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
	channel                                                  string
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
	recoveryDeclined         bool
	recoverySigningKey       string
	recoverySigningPublicKey string
	secureBootFirmware       oddc.EffectiveSecureBootFirmwarePolicy
	shimFacts                *secureboot.ShimFacts
	deviceGate               string
	// oddcModel is the ODDC model id once resolved, "" when none matches.
	oddcModel string
	// oddcChecked is true once the ODDC lookup ran, so a report about an
	// earlier failure does not claim that no model matches.
	oddcChecked bool
	// saveToDrive is set on live and recovery runs: their home folder is
	// gone at the next start, so a report goes to a USB drive (Plane 343).
	saveToDrive bool
	// targetDisk is the disk this run installs to, once known. The report
	// step never offers it as a drive to save to.
	targetDisk string
}

const (
	installerSecureBootResumeMarker = "/var/lib/gjallarOS/installer-resume-after-secure-boot"
	installerReleaseRebootScheduled = 75
)

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
			return fmt.Errorf("trust resumed gjallarOS repository: %w", err)
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
// the maintenance boot. By then the machine is a gjallarOS install, so the
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

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) (code int) {
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
	f.StringVar(&opt.repo, "repo", cwd, "gjallarOS repository")
	f.BoolVar(&opt.skipHardware, "skip-hardware", false, "skip hardware generation")
	f.BoolVar(&opt.refreshHardware, "refresh-hardware", false, "regenerate hardware configuration")
	f.BoolVar(&opt.noRebuild, "no-rebuild", false, "do not install a boot generation")
	f.BoolVar(&opt.acceptExisting, "accept-existing", false, "allow an existing gjallarOS installation to be updated")
	f.BoolVar(&opt.forceRedeploy, "force-redeploy", false, "force a full clean gjallarOS redeployment without repartitioning or wiping user data")
	f.BoolVar(&opt.recovery, "recovery", false, "run from the trusted gjallarOS recovery environment")
	f.StringVar(&opt.targetDisk, "target-disk", "", "whole physical disk for a destructive fresh gjallarOS installation")
	f.StringVar(&opt.recoveryDisk, "recovery-disk", "", "GPT disk with unallocated space for a recovery partition")
	f.StringVar(&opt.recoveryPartition, "recovery-partition", "", "existing dedicated recovery partition")
	f.StringVar(&opt.recoverySigningKey, "recovery-signing-key", "", "runtime path to offline Ed25519 recovery release private key")
	f.StringVar(&opt.recoverySigningPublicKey, "recovery-signing-public-key", "", "runtime path to separately pinned Ed25519 recovery release public key")
	f.StringVar(&opt.channel, "channel", "", "release channel the install follows: "+strings.Join(config.ReleaseChannels, ", "))
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		return 2
	}
	// The run's log (Plane 343) sees what the installer prints from here on.
	// It runs last, after the report offer, so the copy holds the whole run.
	runLog, out, errOut := startInstallLog(out, errOut)
	defer func() { runLog.finish(ctx, errOut, code) }()
	root, err := installercheck.ResolveRepository(opt.repo)
	if err != nil {
		return fail(errOut, err)
	}
	opt.repo = root
	if err := requireNixOS("/etc/os-release"); err != nil {
		return fail(errOut, err)
	}
	if !opt.recovery {
		if err := requireSystemCheckoutRun(installercheck.SystemRepositoryFile, root, geteuid()); err != nil {
			return fail(errOut, err)
		}
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
	runLog.user = &s.user
	// A failure from here on may become an install report the user saves
	// (Plane 292). Failures before this point are about the checkout, not
	// the machine, and get no report.
	recorder := &failureRecorder{Writer: errOut}
	errOut = recorder
	defer func() {
		if code == 1 && recorder.err != nil && reportAllowed(in, s.user) {
			offerInstallReport(ctx, ui, out, reportInput{
				reason:      installreport.ReasonFailed,
				repo:        root,
				recovery:    opt.recovery,
				user:        s.user,
				oddcModel:   s.oddcModel,
				oddcChecked: s.oddcChecked,
				failure:     recorder.err,
				log:         runLog,
				toDrive:     s.saveToDrive,
				targetDisk:  s.targetDisk,
			})
		}
	}()
	installedRoot := "/"
	presetPath := filepath.Join(root, "user.config.json")
	if opt.recovery && opt.acceptExisting {
		installedRoot = "/mnt"
		if err := importInstalledAnswers(installedRoot, presetPath, out); err != nil {
			return fail(errOut, err)
		}
	}
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
	if err := applyChannelFlag(&s.user, opt.channel, s.preset, presetPath); err != nil {
		return fail(errOut, err)
	}
	runLog.refreshKnown()
	if opt.recovery {
		if opt.acceptExisting {
			s.existing, err = detectRecoveryInstalledRoot(ctx, installedRoot)
			if err != nil {
				return fail(errOut, err)
			}
		} else {
			// The embedded repository is installation source material, not evidence
			// that the recovery environment itself is an installed gjallarOS system.
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
	switch {
	case opt.recovery:
		installlog.Mark(installlog.RunHost, installlog.Choice("kind", "recovery"))
		s.saveToDrive = true
		if s.existing {
			runLog.target = installedRoot
		}
	case persistentInstalledHost:
		installlog.Mark(installlog.RunHost, installlog.Choice("kind", "installed"))
		runLog.target = "/"
	default:
		installlog.Mark(installlog.RunHost, installlog.Choice("kind", "live"))
		s.saveToDrive = true
	}
	s.targetDisk = opt.targetDisk
	if err := validateTargetDiskContext(opt.targetDisk, persistentInstalledHost); err != nil {
		return fail(errOut, err)
	}

	forceRedeploy := opt.forceRedeploy || s.user.ForceRedeploy

	if s.existing && !forceRedeploy && !opt.acceptExisting {
		choice, err := ui.Choice(
			ctx,
			"Existing gjallarOS installation detected",
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

	// A rerun offers the installed system's answers instead of new-install
	// defaults. Recovery reads its preset from the installed root instead.
	var running *config.User
	if s.existing && !s.preset && !opt.recovery {
		running = runningAnswers(ctx, out)
	}

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
	// Before prepareHost: an in-place run changes /etc/nixos there, and a
	// refusal has to leave the machine as it was.
	encrypted := opt.recovery || !persistentInstalledHost
	if !encrypted {
		luks, err := diskcrypto.RootDevice(ctx)
		if err != nil {
			return fail(errOut, fmt.Errorf("check system disk encryption: %w", err))
		}
		encrypted = luks != ""
	}
	if err := stableOnUnencryptedDisk(ctx, ui, out, encrypted, &s.user, s.preset, presetPath); err != nil {
		return fail(errOut, err)
	}
	if skip := liveHostPreparationSkip(
		opt,
		os.Getenv("GJALLAR_INSTALLER_RESUME_STATE"),
		persistentInstalledHost,
	); skip != "" {
		fmt.Fprintln(out, skip)
	} else if code := prepareHost(
		ctx,
		ui,
		opt,
		s,
		originalArgs,
		runLog,
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
		return fail(errOut, fmt.Errorf("resolve gjallarOS source revision: %w", err))
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
		reportBestEffort(out)
		if reportAllowed(in, s.user) {
			offerInstallReport(ctx, ui, out, reportInput{
				reason:      installreport.ReasonUnknown,
				repo:        root,
				recovery:    opt.recovery,
				user:        s.user,
				oddcChecked: true,
				toDrive:     s.saveToDrive,
				targetDisk:  s.targetDisk,
			})
		}
	} else {
		fmt.Fprintf(out, "ODDC canonical model: %s\n", resolvedDevice.ModelID)
	}
	s.oddcModel = resolvedDevice.ModelID
	s.oddcChecked = true

	if needsDeviceRebind {
		if s.user.UnattendedInstall || s.user.Management.Enable {
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
	partitionable := !persistentInstalledHost || rootEncrypted(ctx, "/")
	// A fresh install picks its disk before the questions, so USB blocking
	// knows whether the system will run from a USB drive (wiki 752 Q11).
	freshTarget := ""
	mayInstall := !opt.noRebuild && (!s.preset || s.user.RunRebuild || opt.forceRedeploy)
	if mayInstall && !s.existing {
		// A first install clones this commit into the system checkout,
		// fresh and in place (Plane 313); an unpushed commit or a folder
		// already in the way stops here, before anything is changed.
		if err := checkReleasedRevision(ctx, sourceRevision); err != nil {
			return fail(errOut, fmt.Errorf("the installer cannot set up %s: %w", systemcheckout.Path, err))
		}
		if persistentInstalledHost {
			if err := checkoutFree(installedRoot); err != nil {
				return fail(errOut, err)
			}
		}
	}
	if !persistentInstalledHost && mayInstall {
		freshTarget, err = selectFreshTargetDisk(ctx, ui, out, opt.targetDisk)
		if err != nil {
			return fail(errOut, fmt.Errorf("select fresh installation target: %w", err))
		}
		s.targetDisk = freshTarget
	}
	usbSystemDisk := systemDiskOnUSB(ctx, persistentInstalledHost, root, freshTarget)
	if s.preset {
		normalizePreset(&s.user, root)
		if s.user.USBGuardEnable && resolvedDevice.ModelID == "" {
			return fail(errOut, errors.New("usbguardEnable needs an ODDC model, and ODDC has none for this machine; set usbguardEnable to false"))
		}
		if s.user.USBTrustEnforce && usbSystemDisk != "" {
			return fail(errOut, fmt.Errorf("usbTrustEnforce is refused: %s Set usbTrustEnforce to false", usbSystemDisk))
		}
		if s.user.USBGuardEnable && s.user.USBTrustTPMHandle == "" && diskcrypto.TPMAvailable() {
			s.user.USBTrustTPMHandle = config.DefaultUSBTrustTPMHandle
		}
	} else if err := collectInteractive(ctx, ui, root, managementBlocker(s.secureBootFirmware.Policy.Supported, diskcrypto.TPMAvailable()), hardware, choices, running, &s.user); err != nil {
		return fail(errOut, err)
	}
	if !s.existing {
		// ROADMAP section 14, checkpoint 4: a new install clones the official
		// repository into the root-owned system checkout.
		s.user.DotfilesDir = systemcheckout.Path
	}
	configureWeatherLocation(&s.user, running == nil && !s.existing, out)
	// Resolve managed security requirements before checking the physical TPM
	// boundary so policy normalization cannot re-enable TPM-dependent features
	// after a no-TPM decision.
	normalizeManagementSafety(&s.user)

	tpmAvailable := diskcrypto.TPMAvailable()
	// Set when the TPM-dependent security is turned off below; the USB
	// choice, asked after Secure Boot, then offers no blocking either.
	tpmSecurityOff := false
	securityRequested := s.user.SecureBootPrompt ||
		s.user.SecureBootEnable ||
		s.user.LUKSTPM2Enable ||
		s.user.PrebootLock.Enable ||
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
				"gjallarOS requires TPM2 for its Secure Boot and measured-boot security policy.",
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
		tpmSecurityOff = true

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
				"Updated user.config.json for this TPM-less machine: secureBootPrompt=false, secureBootEnable=false, luksTpm2Enable=false, prebootLock.enable=false, usbTrustEnforce=false, usbTrustTpmHandle=\"\".",
			)
		}
	}

	// Read before any Secure Boot question: turning Secure Boot off and
	// installing own keys both need the firmware setup. On locked firmware
	// the only way in is shim, which must already start this system.
	var firmwareTrust *secureboot.FirmwareTrust
	if trust, trustErr := secureboot.ReadFirmwareTrust(); trustErr != nil {
		fmt.Fprintf(out, "What the firmware trusts could not be read (%v).\n", trustErr)
	} else {
		firmwareTrust = &trust
		fmt.Fprint(out, firmwareTrustReport(trust))
		if s.preset && s.user.SecureBootEnable && s.user.SecureBootShim {
			fmt.Fprintln(out, "user.config.json: Secure Boot through shim; the firmware keys stay as they are.")
		} else if !s.user.UnattendedInstall {
			locked, err := askFirmwareLocked(ctx, ui, trust)
			if err != nil {
				return fail(errOut, err)
			}
			if locked {
				if installedRoot != "/" {
					fmt.Fprint(out, lockedFirmwareStop("Installing from recovery media cannot use shim yet."))
					return 0
				}
				facts, ok, err := chooseLockedShim(ctx, ui, out, root, trust, s.user.LUKSTPM2Enable)
				if err != nil {
					return fail(errOut, err)
				}
				if !ok {
					return 0
				}
				s.shimFacts = &facts
				s.user.SecureBootEnable = true
				s.user.SecureBootShim = true
				s.user.FirmwarePasswordLock = false
				if tpmAvailable && !s.user.Management.Enable {
					if err := collectStartupUnlock(ctx, ui, diskcrypto.ModelTPMKind(), s.chassisType, s.touchscreen, &s.user); err != nil {
						return fail(errOut, err)
					}
				}
			}
		}
	}

	if tpmAvailable &&
		s.user.SecureBootPrompt &&
		!s.user.SecureBootShim &&
		!s.user.Management.Enable &&
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

		// ODDC has a tested setup for this model, so yes is the default.
		s.user.SecureBootEnable, err = ui.Confirm(
			ctx,
			promptText,
			true,
		)
		if err != nil {
			return fail(errOut, err)
		}
		if !s.user.SecureBootEnable {
			fmt.Fprint(out, secureBootDeclinedNotice(partitionable, "You can set it up later with: gjallarctl secure-boot setup"))
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
			if err := collectStartupUnlock(ctx, ui, diskcrypto.ModelTPMKind(), s.chassisType, s.touchscreen, &s.user); err != nil {
				return fail(errOut, err)
			}
		}
	} else if tpmAvailable &&
		s.user.SecureBootPrompt &&
		!s.user.SecureBootEnable &&
		!s.user.Management.Enable {
		fmt.Fprint(out, noSecureBootPolicyNotice(resolvedDevice.ModelID))
		shimOK := false
		if !s.user.UnattendedInstall && installedRoot == "/" && firmwareTrust != nil {
			facts, ok, err := offerUnknownModelShim(ctx, ui, out, root, *firmwareTrust, s.user.LUKSTPM2Enable)
			if err != nil {
				return fail(errOut, err)
			}
			if ok {
				shimOK = true
				s.shimFacts = &facts
				s.user.SecureBootEnable = true
				s.user.SecureBootShim = true
				s.user.FirmwarePasswordLock = false
				if err := collectStartupUnlock(ctx, ui, diskcrypto.ModelTPMKind(), s.chassisType, s.touchscreen, &s.user); err != nil {
					return fail(errOut, err)
				}
			}
		}
		if !shimOK {
			fmt.Fprint(out, secureBootDeclinedNotice(partitionable, "After the install, gjallarctl secure-boot setup can try it through shim while the firmware keeps its factory keys."))
		}
	} else if s.user.SecureBootEnable &&
		!s.user.SecureBootShim &&
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

	// shim needs no ODDC key policy: the firmware keys stay as they are.
	if err := validateSecureBootFirmwareSupport(
		s.user.SecureBootEnable && !s.user.SecureBootShim,
		resolvedDevice.ModelID,
		s.secureBootFirmware,
	); err != nil {
		if mayOfferSecureBootFallback(
			s.user.SecureBootEnable,
			s.user.Management.Enable,
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
			tpmSecurityOff = true

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
					"firmware enforces Secure Boot but gjallarOS Secure Boot is not set up for this install; turn Secure Boot off in firmware setup and rerun",
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
				if err := runLog.restart(ctx, errOut, true); err != nil {
					return fail(errOut, fmt.Errorf("reboot into firmware setup: %w", err))
				}
				return 0
			case secureBootOffCancel:
				fmt.Fprintln(out, "Installation cancelled. Nothing was changed.")
				return 0
			}
		}
	}

	// Asked only now: the recovery partition depends on the Secure Boot
	// decision, and nothing is partitioned or shrunk without it. USB comes
	// last in the security block, after the TPM decision.
	if !s.preset {
		if err := collectRecovery(ctx, ui, partitionable, s.user.SecureBootEnable, &s.user); err != nil {
			return fail(errOut, err)
		}
		if err := collectUSBTrust(ctx, ui, resolvedDevice.ModelID != "", partitionable, tpmAvailable && !tpmSecurityOff, usbSystemDisk, running, &s.user); err != nil {
			return fail(errOut, err)
		}
	} else if s.user.RecoveryPartitionEnable && !s.user.SecureBootEnable {
		s.user.RecoveryPartitionEnable = false
		fmt.Fprintln(out, noRecoveryPartitionNotice)
	} else if s.user.RecoveryPartitionEnable && s.user.SecureBootShim {
		s.user.RecoveryPartitionEnable = false
		fmt.Fprintln(out, shimNoRecoveryPartitionNotice)
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
	if s.user.Management.Enable && s.user.Management.AllowInsecureTLS {
		if err := confirmInsecureJODS(ctx, ui); err != nil {
			return fail(errOut, err)
		}
	}
	// Management-only boot restrictions must never survive in an unmanaged
	// preset or a user declining JODS enrollment.
	if err := validateSelections(s.user, choices); err != nil {
		return fail(errOut, err)
	}
	if s.user.PrebootLock.Enable && (!s.user.RecoveryEnable || !s.user.SecureBootEnable) {
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
		out,
	); err != nil {
		return fail(errOut, err)
	}
	if err := configureSecrets(ctx, root, &s, errOut); err != nil {
		return fail(errOut, err)
	}
	write := s.user.WriteConfig
	if !s.preset {
		// Q-O: one summary and one question for writing and installing.
		write, err = confirmInstall(ctx, ui, s.user, installTarget{
			disk:          freshTarget,
			redeploy:      opt.forceRedeploy,
			encryptedRoot: partitionable,
			writeOnly:     opt.noRebuild,
		})
		if err != nil {
			return fail(errOut, err)
		}
	}
	if !write {
		fmt.Fprintln(out, "Nothing changed: nothing was written or installed. Run the installer again to install.")
		return 0
	}
	var compatibilitySource *installstate.Existing
	if persistentInstalledHost {
		// The host name the running system has, not the one chosen above:
		// nixos-rebuild picks the flake configuration by it.
		host, err := os.Hostname()
		if err != nil {
			return fail(errOut, err)
		}
		compatibilitySource = &installstate.Existing{Dir: filepath.Join(installedRoot, "etc/nixos"), Host: host}
	}
	if err := installstate.Ensure(ctx, root, compatibilitySource, s.user.Username, pinnedRelease); err != nil {
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
	if s.render.Management.Enable && !s.existing {
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
		kept := keptHardwareTPM2(
			s.render.LUKSTPM2Enable,
			s.render.Management.Enable,
			persistentInstalledHost,
			s.render.SecureBootEnable,
			hardwarePath,
		)
		switch kept {
		case keptTPM2NoLUKS, keptTPM2NoSecureBoot:
			s.render.LUKSTPM2Enable = false
			if err := saveTPM2Off(&s.user, presetPath, out); err != nil {
				return fail(errOut, err)
			}
			if kept == keptTPM2NoLUKS {
				fmt.Fprintln(out, "No encrypted system disk in the kept hardware configuration; TPM2 disk unlock stays off.")
			} else {
				fmt.Fprintln(out, "gjallarOS Secure Boot is not set up on this install; TPM2 disk unlock stays off.")
			}
			if err := nixrender.WriteAtomic(settingsPath, s.render); err != nil {
				return fail(errOut, err)
			}
		}

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
		reportDroppedSwap(out, hardwareResult.droppedSwap)
		if !tpm2AllowedForSecureBoot(s.render.SecureBootEnable) {
			s.render.LUKSTPM2Enable = false
			if err := saveTPM2Off(&s.user, presetPath, out); err != nil {
				return fail(errOut, err)
			}
			fmt.Fprintln(
				out,
				"gjallarOS Secure Boot is not set up on this install; TPM2 disk unlock stays off.",
			)
		} else if tpm2FollowsRequest(
			s.render.Management.Enable,
			persistentInstalledHost,
		) {
			s.render.LUKSTPM2Enable = s.user.LUKSTPM2Enable
		} else {
			tpmOut, err := controlOutput(ctx, s.control, errOut, installerTPM2Args(root, hardwarePath, s.user.LUKSTPM2Enable)...)
			if err != nil {
				return fail(errOut, err)
			}
			fmt.Fprint(out, tpmOut)
			s.render.LUKSTPM2Enable = strings.Contains(tpmOut, "luks_tpm2_enable=true")
			// Without a LUKS mapping the request can never work here. A
			// missing TPM keeps the saved request, as before.
			if noLUKSMapping(hardwarePath) {
				if err := saveTPM2Off(&s.user, presetPath, out); err != nil {
					return fail(errOut, err)
				}
			}
		}
		if err := nixrender.WriteAtomic(settingsPath, s.render); err != nil {
			return fail(errOut, err)
		}
		if !s.render.Management.Enable && !s.existing {
			// A fresh target does not have its canonical LUKS2 root yet.
			// Running installer luks here would operate against the installer
			// environment instead of the future target.
			//
			// rootprovision creates the initial human recovery-capable keyslot.
			// TPM2 enrollment is intentionally deferred until the installed
			// gjallarOS lifecycle can act against its own verified root device.
			fmt.Fprintln(
				out,
				"Fresh-target TPM2 LUKS enrollment deferred until the installed gjallarOS lifecycle.",
			)
		} else if s.existing {
			fmt.Fprintln(out, "Existing LUKS keyslots retained; disk-key migration was not rerun.")
		}
	}
	if s.render.PrebootLock.Enable && !s.render.LUKSTPM2Enable {
		return fail(errOut, errors.New("JODS preboot locking requires verified TPM2 LUKS enrollment; configuration was left unactivated"))
	}
	if notice := developerConfirmNotice(s.user, s.existing, s.render.LUKSTPM2Enable); notice != "" {
		fmt.Fprintln(out, notice)
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
	if s.render.SecureBootEnable && s.render.SecureBootShim {
		// Before the rebuild: it puts the gjallarOS boot chain first, which
		// shim starts only once it lists the gjallarOS key.
		if installedRoot != "/" {
			return fail(errOut, errors.New("Secure Boot through shim installs only on the running system"))
		}
		if err := secureboot.PrepareShim(ctx, out); err != nil {
			return fail(errOut, err)
		}
		if s.shimFacts == nil {
			facts, err := buildShimFacts(ctx, root)
			if err != nil {
				return fail(errOut, err)
			}
			s.shimFacts = &facts
		}
		sbOn, err := secureboot.FirmwareSecureBoot()
		if err != nil {
			return fail(errOut, fmt.Errorf("read Secure Boot state: %w", err))
		}
		if sbOn && !persistentInstalledHost {
			// A fresh install from the installer stick: the live system
			// keeps no answers across a restart, so the key is queued after
			// the install (FinishFreshShim). Check now what would stop it
			// then, before the disk changes.
			if err := secureboot.CheckFreshShim(ctx, ui, out, *s.shimFacts); err != nil {
				return fail(errOut, err)
			}
		} else if sbOn {
			listed, err := secureboot.QueueShimKey(ctx, ui, out, *s.shimFacts, "the installer")
			if err != nil {
				return fail(errOut, err)
			}
			if !listed {
				fmt.Fprintln(out, "Nothing about how this system starts has changed yet. Restart now; the installer keeps your answers.")
				return 0
			}
		} else {
			// Secure Boot off: the firmware starts any boot chain, so the
			// rebuild may put shim first now. Only the gjallarOS shim can
			// show MokManager, so the key is queued after the first start
			// through it (gjallarctl secure-boot setup), not here.
			trust, err := secureboot.ReadFirmwareTrust()
			if err != nil {
				return fail(errOut, fmt.Errorf("read firmware trust: %w", err))
			}
			choice, err := secureboot.ChoosePath(trust, *s.shimFacts, nil)
			if err != nil {
				return fail(errOut, err)
			}
			if choice.Path != secureboot.PathShim {
				return fail(errOut, fmt.Errorf("this firmware would not start shim: %s", choice.Reason))
			}
		}
	} else if s.render.SecureBootEnable {
		if err := secureboot.Prepare(ctx, out); err != nil {
			return fail(errOut, err)
		}
	}
	runLog.refreshKnown()
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
	if opt.forceRedeploy || (!s.preset && !opt.noRebuild) {
		// An interactive run said yes to "Install now?" above.
		runRebuild = true
	}
	if needsDeviceRebind && !runRebuild {
		return fail(
			errOut,
			errors.New(
				"Recovery device rebind requires installing the regenerated system; rebuild cannot be skipped",
			),
		)
	}

	// freshRoot is where a fresh install mounted its new root.
	freshRoot := ""
	if runRebuild {
		if persistentInstalledHost {
			maintenanceRequired := false

			if needsRecoveryStorage(s) {
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

				if err := deployInPlace(
					ctx,
					root,
					installedRoot,
					s.user.Hostname,
					sourceRevision,
					s.user.Channel(),
					!s.existing,
					out,
				); err != nil {
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
				// still left no recovery partition; another one would loop.
				if os.Getenv("GJALLAR_INSTALLER_RESUME_STATE") == installerresume.StateMaintenanceReboot {
					return fail(
						errOut,
						errors.New("recovery-storage maintenance did not provision the recovery partition; automatic retry disabled"),
					)
				}

				fmt.Fprintln(
					out,
					"PASS: one-shot recovery-storage maintenance environment installed",
				)

				// The rerun after maintenance formats and fills the recovery partition
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
			targetDisk := freshTarget
			if targetDisk == "" {
				targetDisk, err = selectFreshTargetDisk(
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
			}
			s.targetDisk = targetDisk
			if s.user.USBTrustEnforce {
				if reason := systemDiskOnUSB(ctx, false, root, targetDisk); reason != "" {
					return fail(errOut, fmt.Errorf("USB blocking is on, but %s Nothing was changed; run the installer again and leave USB blocking off", reason))
				}
			}

			result, err := runFreshBareMetal(
				ctx,
				ui,
				root,
				targetDisk,
				s.user.Hostname,
				sourceRevision,
				s.user.Channel(),
				hardware,
				resolvedDevice,
				opt.recovery,
				freshRecoveryPartition(s.user),
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
			runLog.target = result.MountPoint
			runLog.bootDisk = targetDisk
			freshRoot = result.MountPoint
			s.recoveryPartition = result.RecoveryPartition
		}
	}

	// Secure Boot continuation is intentionally independent of runRebuild.
	//
	// -no-rebuild means "do not install another NixOS generation"; it must
	// never mean "skip an already-started Secure Boot transaction".
	shimSBOff := false
	if s.render.SecureBootEnable && s.render.SecureBootShim {
		sbOn, err := secureboot.FirmwareSecureBoot()
		if err != nil {
			return fail(errOut, fmt.Errorf("read Secure Boot state: %w", err))
		}
		shimSBOff = !sbOn
	}
	freshShim := s.render.SecureBootEnable && s.render.SecureBootShim && !shimSBOff && !persistentInstalledHost
	if freshRoot != "" && !freshShim {
		// The fresh shim path queues this with the gjallarOS key for one
		// MokManager visit; every other fresh install queues it alone.
		notice, err := secureboot.QueueStickDelete(ctx, out)
		if err != nil {
			return fail(errOut, err)
		}
		fmt.Fprint(out, notice)
	}
	if shimSBOff {
		installlog.Mark(installlog.SecureBootContinue, installlog.Choice("path", "shim-sb-off"))
		// The rest of the switch runs from gjallarOS itself; until Secure
		// Boot enforces, each start shows the next step.
		if err := armSecureBootResume(ctx, out); err != nil {
			return fail(errOut, err)
		}
		fmt.Fprintln(out, "Restarting into gjallarOS through shim. Then run gjallarctl secure-boot setup in a terminal: it puts the gjallarOS key into shim's list.")
		if err := runLog.restart(ctx, errOut, false); err != nil {
			return fail(errOut, fmt.Errorf("restart: %w", err))
		}
		return 0
	} else if freshShim {
		installlog.Mark(installlog.SecureBootContinue, installlog.Choice("path", "shim-fresh"))
		if freshRoot == "" {
			return fail(errOut, errors.New("Secure Boot through shim: no new system was installed in this run"))
		}
		// The verifier exists only in the target's store.
		if _, err := secureboot.FinishFreshShim(ctx, ui, out, *s.shimFacts, []string{
			"nixos-enter", "--root", freshRoot, "--",
			"/nix/var/nix/profiles/system/sw/bin/gjallar-verify-secure-boot-artifacts",
		}); err != nil {
			return fail(errOut, err)
		}
		if err := armSecureBootResume(ctx, out); err != nil {
			return fail(errOut, err)
		}
		fmt.Fprintln(out, "Restarting into MokManager...")
		if err := runLog.restart(ctx, errOut, false); err != nil {
			return fail(errOut, fmt.Errorf("restart: %w", err))
		}
		return 0
	} else if s.render.SecureBootEnable && s.render.SecureBootShim {
		installlog.Mark(installlog.SecureBootContinue, installlog.Choice("path", "shim"))
		next, err := secureboot.ContinueShimInstall(
			ctx,
			ui,
			out,
			"/nix/var/nix/profiles/system/sw/bin/gjallar-verify-secure-boot-artifacts",
		)
		if err != nil {
			return fail(errOut, err)
		}
		if next == secureboot.ContinuationReboot {
			if err := armSecureBootResume(ctx, out); err != nil {
				return fail(errOut, err)
			}
			fmt.Fprintln(out, "Restarting into gjallarOS through shim...")
			if err := runLog.restart(ctx, errOut, false); err != nil {
				return fail(errOut, fmt.Errorf("restart: %w", err))
			}
			return 0
		}
	} else if s.render.SecureBootEnable {
		installlog.Mark(installlog.SecureBootContinue, installlog.Choice("path", "own-keys"))
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
			if err := armSecureBootResume(ctx, out); err != nil {
				return fail(errOut, err)
			}

			fmt.Fprintln(
				out,
				"Secure Boot firmware instructions acknowledged.",
			)
			fmt.Fprintln(
				out,
				"Rebooting directly into firmware setup...",
			)

			if err := runLog.restart(ctx, errOut, true); err != nil {
				return fail(
					errOut,
					fmt.Errorf("reboot into firmware setup: %w", err),
				)
			}

			return 0
		}
	}

	if runRebuild {
		if s.render.Management.Enable {
			if s.existing {
				if err := activateJODSEnrollment(ctx, true); err != nil {
					return fail(errOut, err)
				}
				fmt.Fprintln(out, "JODS enrollment submitted; retry timer enabled while approval is pending.")
			} else {
				fmt.Fprintln(
					out,
					"JODS enrollment deferred to the installed gjallarOS lifecycle; installer environment was not enrolled.",
				)
			}
		}
		if s.user.AutoReboot {
			_ = runLog.restart(ctx, errOut, false)
		} else if !s.render.Management.Enable {
			yes, _ := ui.Confirm(
				ctx,
				"Deployment complete. Reboot now?",
				false,
			)
			if yes {
				_ = runLog.restart(ctx, errOut, false)
			}
		}
	}
	if s.render.Management.Enable && !runRebuild {
		fmt.Fprintln(out, "JODS configured, not contacted: enrollment waits for a successful installation rebuild.")
	}

	if !runRebuild {
		fmt.Fprintf(out, "Configuration written to %s; the next boot generation was not installed.\n", filepath.Join(root, "generated", "state.nix"))
		switch {
		case opt.noRebuild:
			fmt.Fprintln(out, "--no-rebuild skipped it. Run the installer again without it to install.")
		case s.preset:
			fmt.Fprintln(out, "runRebuild is false in user.config.json. Set it to true and run the installer again to install.")
		default:
			fmt.Fprintf(out, "Run the installer again from %s and answer y to install it.\n", root)
		}
		return 0
	}
	fmt.Fprintln(out, "gjallarOS installation complete.")
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
	if err := runJODSCommand(ctx, "sudo", "systemctl", "start", "mdm-agent-enroll.service"); err != nil {
		return fmt.Errorf("submit JODS enrollment: %w", err)
	}
	if err := runJODSCommand(ctx, "sudo", "systemctl", "enable", "--now", "mdm-agent-enroll.timer"); err != nil {
		return fmt.Errorf("enable JODS enrollment retry timer: %w", err)
	}
	return nil
}

// liveHostPreparationSkip reports why the running system must not be
// rebuilt, staged or rebooted before the installer operates on its target.
func liveHostPreparationSkip(opt options, resumeState string, persistentInstalledHost bool) string {
	switch {
	case resumeState == installerresume.StateMaintenanceReboot:
		// The gjallarOS generation was deployed before the maintenance boot.
		// The bootstrap /etc/nixos config lacks the flake's helpers, so it
		// rebuilt and switched to the bare base system (e2e-full, 2026-10-05).
		return "Resumed after maintenance; gjallarOS already deployed, live-host rebuild skipped."
	case opt.recovery:
		// Recovery media is a purpose-built execution environment. Never mutate
		// or rebuild the live recovery system before operating on the target.
		return "Recovery environment validated; live-host rebuild skipped."
	case !persistentInstalledHost:
		// Live media, with or without --target-disk: the disk is picked
		// later. /etc/nixos there describes no bootable system, so its
		// rebuild fails (no root file system, no boot loader), and it only
		// fills the RAM-backed store; nixos-install builds the target.
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
	runLog *logKeeper,
	out,
	errOut io.Writer,
) int {
	// A completed gjallarOS installation is flake-owned: the flake pins the
	// NixOS release and brings every installer prerequisite. Staging a release
	// or rebuilding the bootstrap /etc/nixos/configuration.nix would put the
	// machine back on its pre-install base generation, and a clean redeploy
	// once did that under the running desktop (Plane 199). The flake
	// deployment later in the run is the only system change on a rerun.
	if s.existing {
		fmt.Fprintln(out, "Existing gjallarOS installation detected; skipping /etc/nixos release and bootstrap rebuilds.")
		return offerFirmwareUpdates(ctx, ui, s, out, errOut)
	}
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
			opt.repo,
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

		if err := runLog.restart(ctx, errOut, false); err != nil {
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
	configPath := "/etc/nixos/configuration.nix"
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fail(errOut, err)
	}
	plan := bootstrap.Build(data, s.preset)
	if len(plan.Missing) > 0 {
		fmt.Fprintf(out, "Missing helpers/options: %s\nProposed configuration:\n%s\n", strings.Join(plan.Missing, ", "), plan.Updated)
		if !s.user.Management.Enable && os.Getenv("GJALLAR_INSTALLER_RESUMED") != "1" {
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
	return offerFirmwareUpdates(ctx, ui, s, out, errOut)
}

func offerFirmwareUpdates(
	ctx context.Context,
	ui prompt.UI,
	s state,
	out,
	errOut io.Writer,
) int {
	var err error
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
// machine or the checkout; the prompts show made-up examples instead. On a
// rerun, running holds the installed system's own answers (runningAnswers),
// and every question offers them; nil on a new install.
func collectInteractive(ctx context.Context, ui prompt.UI, root string, managedBlocker string, hardware discovery.Hardware, o discovery.Options, running *config.User, u *config.User) error {
	var err error
	a := interviewDefaults(running, o)
	if running != nil {
		// Kept, not asked: the theme belongs to the desktop shell, and the
		// USB trust key stays at the TPM handle it was created at.
		u.Theme = running.Theme
		u.USBTrustTPMHandle = running.USBTrustTPMHandle
		// The weather city is answered in the bar, and an empty one means
		// the user turned the weather off.
		u.WeatherCity, u.WeatherCountry = running.WeatherCity, running.WeatherCountry
	}
	// O1 order: region, account, management, then the security block in
	// the main flow (Secure Boot, unlock, recovery, USB) and the summary.
	if err := collectRegion(ctx, ui, running, a, u); err != nil {
		return err
	}
	if u.Username, err = askPersonal(ctx, ui, "Username", a.Username, "alex", true); err != nil {
		return err
	}
	if u.Hostname, err = ui.Value(ctx, "Hostname", a.Hostname); err != nil {
		return err
	}
	if err := collectJODSManagement(ctx, ui, managedBlocker, running, u); err != nil {
		return err
	}
	if u.Management.Enable {
		localDevelopment, err := ui.Confirm(ctx, "Use a clearly marked local-development JODS server?", false)
		if err != nil {
			return err
		}
		endpointDefault := strings.TrimSpace(a.Management.Endpoint)
		if endpointDefault == "" && !localDevelopment {
			endpointDefault = "https://admin.oss-ad.eu:1666"
		}
		u.Management.Endpoint, err = ui.Value(ctx, "JODS HTTPS endpoint", endpointDefault)
		if err != nil {
			return err
		}
		u.Management.PolicySigningPublicKey, err = ui.Value(ctx, "JODS policy-signing Ed25519 public key (64 hexadecimal characters)", a.Management.PolicySigningPublicKey)
		if err != nil {
			return err
		}
		u.Management.RecoveryCommandSigningPublicKey, err = ui.Value(ctx, "JODS recovery-command Ed25519 public key (different 64 hexadecimal characters)", a.Management.RecoveryCommandSigningPublicKey)
		if err != nil {
			return err
		}
		enrollmentModes := []string{"auto", "manual", "jade-registry-only"}
		u.Management.EnrollmentMode, err = ui.Choice(ctx, "JODS enrollment mode", offeredOr(a.Management.EnrollmentMode, enrollmentModes, "manual"), enrollmentModes)
		if err != nil {
			return err
		}
		if localDevelopment {
			u.Management.AllowInsecureTLS, err = ui.Confirm(ctx, "Allow a self-signed JODS TLS certificate for local development?", a.Management.AllowInsecureTLS)
			if err != nil {
				return err
			}
		}
		deviceDefault := "pc"
		if hardware.FormFactor == "laptop" {
			deviceDefault = "laptop"
		}
		deviceClasses := []string{"pc", "vm", "laptop", "kiosk", "workstation"}
		u.Management.DeviceClass, err = ui.Choice(ctx, "JODS device class", offeredOr(a.Management.DeviceClass, deviceClasses, deviceDefault), deviceClasses)
		if err != nil {
			return err
		}
		profileDefault := "hyprland"
		if running != nil && running.Management.DesktopProfile != "" {
			profileDefault = running.Management.DesktopProfile
		}
		u.Management.DesktopProfile, err = ui.Value(ctx, "JODS desktop profile", profileDefault)
		if err != nil {
			return err
		}
		u.Management.DesktopProfile = strings.ToLower(strings.TrimSpace(u.Management.DesktopProfile))
	}
	if err != nil {
		return err
	}
	// Not asked; Settings changes them. Clamshell mode only helps a laptop,
	// so a new install turns it on for a laptop chassis only.
	u.TouchpadWorkspaceSwipe = a.TouchpadWorkspaceSwipe
	u.ClamshellEnable = a.ClamshellEnable
	if running == nil {
		u.ClamshellEnable = hardware.FormFactor == "laptop"
	}
	// Not asked: the git name and email stay empty on a new install (git
	// then sets none) until Settings fills them; a rerun keeps them.
	u.Name, u.Email, u.GitHubUsername = a.Name, a.Email, a.GitHubUsername
	// Not asked (Q-L): gjallarOS signs the settings files. An own OpenSSH
	// key is a Settings > Security change (settingsd ownkey), and a rerun
	// keeps the key the running system has.
	u.SettingsSigningKey = a.SettingsSigningKey
	// Not asked: a rerun keeps the running system's checkout, and a new
	// install gets the system checkout once collection is done.
	u.DotfilesDir = defaultDotfilesDir(u.Username, root)
	if running != nil && running.DotfilesDir != "" && running.Username == u.Username {
		u.DotfilesDir = running.DotfilesDir
	}
	// Not asked: one shell exists, and a new install ships VSCodium and
	// Firefox. A rerun keeps the running choices this checkout still offers.
	u.Shell, u.Editors, u.Browsers = a.Shell, a.Editors, a.Browsers
	u.PreferredEditor = offeredOr(a.PreferredEditor, u.Editors, u.Editors[0])
	u.PreferredBrowser = offeredOr(a.PreferredBrowser, u.Browsers, u.Browsers[0])
	// Theme selection belongs to the desktop shell after installation.
	// Keep the default theme only as the bootstrap rendering contract.
	if u.Theme == "" {
		u.Theme = first(o.Themes)
	}
	if err := collectPrinting(ctx, ui, a, u); err != nil {
		return err
	}
	if err := appChoices(running, u); err != nil {
		return err
	}
	// Device-specific hardware policy is resolved through ODDC.
	// Do not ask users to manually select a vendor/model that hardware
	// discovery and the resolved ODDC device graph already determine.
	u.WriteConfig = true
	u.RunRebuild = true
	return nil
}

// noRecoveryPartitionNotice says why a recovery partition was dropped: its
// image is signed with this machine's Secure Boot key, and without that key
// nothing checks it before it starts.
const noRecoveryPartitionNotice = "No recovery partition: it needs gjallarOS Secure Boot, which is off for this install. The disk is not shrunk."

// shimNoRecoveryPartitionNotice: starting recovery through shim is not
// tested yet (Plane 293).
const shimNoRecoveryPartitionNotice = "No recovery partition: starting recovery through shim is not tested yet. The disk is not shrunk."

// freshRecoveryPartition says whether a fresh disk gets a recovery partition. The
// boot entry alone needs no partition; the partition comes only with
// gjallarOS Secure Boot (collectRecovery).
func freshRecoveryPartition(u config.User) bool {
	return u.RecoveryEnable && u.RecoveryPartitionEnable
}

// The recovery choices (Q-N). Only the allowed ones are shown.
const (
	recoveryFull      = "Boot entry and recovery partition (recommended)"
	recoveryEntryOnly = "Boot entry only"
	recoveryNone      = "None"
)

// collectRecovery asks for recovery as one choice: the trusted boot entry
// with or without its partition, or none. The partition needs gjallarOS
// Secure Boot with its own keys and a root on encrypted Btrfs; without them
// the choice says why and leaves the partition out. A managed machine is
// not asked: it always gets the boot entry, and the partition where it can.
func collectRecovery(ctx context.Context, ui prompt.UI, partitionable, secureBoot bool, u *config.User) error {
	partition := secureBoot && !u.SecureBootShim && partitionable
	why := ""
	switch {
	case !secureBoot:
		why = noRecoveryPartitionNotice
	case u.SecureBootShim:
		why = shimNoRecoveryPartitionNotice
	case !partitionable:
		why = "No recovery partition: this root is not on encrypted Btrfs, which the recovery partition needs."
	}
	if u.Management.Enable {
		u.RecoveryEnable, u.RecoveryPartitionEnable = true, partition
		fmt.Fprintln(ui.Out, "Recovery: a managed machine always gets the trusted gjallarOS recovery/JODS boot entry.")
		if why != "" {
			fmt.Fprintln(ui.Out, why)
		}
		return nil
	}
	fmt.Fprintln(ui.Out, "Recovery: a trusted gjallarOS recovery/JODS boot entry, and a recovery partition that keeps working when the system does not start.")
	if why != "" {
		fmt.Fprintln(ui.Out, why)
	}
	options := []string{recoveryEntryOnly, recoveryNone}
	def := recoveryEntryOnly
	if partition {
		options = append([]string{recoveryFull}, options...)
		def = recoveryFull
	}
	choice, err := ui.Choice(ctx, "Recovery", def, options)
	if err != nil {
		return err
	}
	u.RecoveryEnable = choice != recoveryNone
	u.RecoveryPartitionEnable = choice == recoveryFull
	return nil
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

// appChoices fills in what the installer no longer asks (Q-H, Q-I, Q-J):
// optional apps, their own settings, web apps and Firefox add-ons. A new
// install gets the catalogue defaults; a rerun keeps the running choices.
// The first-boot guide (Plane 354) and gjallarctl change them later.
func appChoices(running, u *config.User) error {
	catalogue, err := apps.Catalogue()
	if err != nil {
		return err
	}
	u.Apps = []string{}
	for _, app := range catalogue {
		selected := app.Default
		if running != nil {
			selected = running.HasApp(app.ID)
		}
		if selected {
			u.Apps = append(u.Apps, app.ID)
		}
	}
	keep := running
	if keep == nil {
		keep = &config.User{}
	}
	// Tailscale on a new install: no home subnets, no exit node, no
	// trusted Wi-Fi. gjallarctl vpn reads that the same as no intent.
	u.Tailscale = nil
	if u.HasApp("tailscale") {
		u.Tailscale = &config.TailscaleIntent{HomeSubnets: []string{}, TrustedWifis: []string{}, SiteRouterTargets: []string{}}
		if keep.Tailscale != nil {
			t := *keep.Tailscale
			u.Tailscale = &t
		}
	}
	u.ThunderbirdSmartCard = u.HasApp("thunderbird") && keep.ThunderbirdSmartCard
	u.AIAgentMode, u.OverrideAISelection, u.OverrideModelWith = "", false, ""
	u.AIEndpoint, u.AIRemoteModel, u.AIRemoteContextTokens = "", "", 0
	if u.HasApp("ai") {
		u.AIAgentMode, u.OverrideAISelection, u.OverrideModelWith = keep.AIAgentMode, keep.OverrideAISelection, keep.OverrideModelWith
		u.AIEndpoint, u.AIRemoteModel, u.AIRemoteContextTokens = keep.AIEndpoint, keep.AIRemoteModel, keep.AIRemoteContextTokens
	}
	u.WebApplications = slices.Clone(keep.WebApplications)
	u.NextcloudEnable, u.NextcloudHost = keep.NextcloudEnable, keep.NextcloudHost
	if err := config.NormalizeProjectTools(u); err != nil {
		return err
	}
	// uBlock Origin is locked on by default; the other add-ons are offered
	// in the first-boot guide and by gjallarctl addons.
	u.FirefoxAddons = apps.CompleteFirefoxAddons(keep.FirefoxAddons)
	u.FirefoxStoreAddons = slices.Clone(keep.FirefoxStoreAddons)
	if u.FirefoxStoreAddons == nil {
		u.FirefoxStoreAddons = []apps.FirefoxStoreAddon{}
	}
	return apps.ValidateFirefoxAddons(u.FirefoxAddons, u.FirefoxStoreAddons)
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
	u.PrebootLock.Enable = false
	u.USBTrustEnforce = false
	u.USBTrustLapseDays = 0
	u.USBTrustTPMHandle = ""
}

func normalizeManagementSafety(u *config.User) {
	if u.Management.Enable {
		// A managed endpoint must never enter the managed state with a weaker
		// local boot/recovery posture. These are one security contract.
		u.SecureBootEnable = true
		u.LUKSTPM2Enable = true
		u.RecoveryEnable = true
		u.RecoveryPartitionEnable = true
		u.PrebootLock.Enable = true
		return
	}

	u.PrebootLock.Enable = false
}

// Printing choices, in the order the question lists them.
const (
	printingOff     = "off"
	printingUSB     = "usb: printers and scanners plugged into this computer"
	printingNetwork = "network: also find printers on the local network"
)

// collectPrinting asks one question for printing. CUPS only ever listens on
// this computer; the network choice adds mDNS discovery, which opens UDP
// 5353 to the local network.
func collectPrinting(ctx context.Context, ui prompt.UI, a config.User, u *config.User) error {
	def := printingOff
	switch {
	case a.PrintingEnable && a.NetworkPrintingEnable:
		def = printingNetwork
	case a.PrintingEnable:
		def = printingUSB
	}
	choice, err := ui.Choice(ctx, "Printers and scanners", def, []string{printingOff, printingUSB, printingNetwork})
	if err != nil {
		return err
	}
	u.PrintingEnable = choice != printingOff
	u.NetworkPrintingEnable = choice == printingNetwork
	return nil
}

// configureWeatherLocation fills the weather city and country without
// asking and without the network. Values already set (a preset, or the
// running system on a rerun) stay. Otherwise, when guess is set, the
// timezone names them; the bar asks the user to confirm or change them.
// Empty values hide the weather.
func configureWeatherLocation(u *config.User, guess bool, out io.Writer) {
	u.WeatherCity = strings.TrimSpace(u.WeatherCity)
	u.WeatherCountry = strings.TrimSpace(u.WeatherCountry)
	if u.WeatherCity != "" && u.WeatherCountry != "" {
		fmt.Fprintf(out, "Weather location: %s, %s\n", u.WeatherCity, u.WeatherCountry)
		return
	}
	u.WeatherCity, u.WeatherCountry = "", ""
	if guess {
		if place, ok := zonecity.Guess(u.Timezone); ok {
			u.WeatherCity, u.WeatherCountry = place.City, place.Country
			fmt.Fprintf(out, "Weather location from the timezone: %s, %s (the bar asks you to confirm it)\n", place.City, place.Country)
			return
		}
	}
	fmt.Fprintln(out, "Weather location: none yet; the bar asks for your city.")
}

func configureRecoveryProvisioning(ctx context.Context, ui prompt.UI, opt options, s *state) error {
	if !s.user.RecoveryEnable {
		return nil
	}

	// Canonical fresh installation owns recovery partition #3 on the
	// selected target disk. It must never ask the user for a second GPT
	// disk or for gjallarOS release-signing private/public key files.
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
		found, err := findRecoveryPartition(ctx)
		// Without Secure Boot the partition is only named in a notice, so
		// a lookup that fails must not stop the rebuild.
		if err != nil && (s.user.SecureBootEnable || explicitTarget) {
			return fmt.Errorf("find the recovery partition: %w", err)
		}
		s.recoveryPartition = found
	}
	// Without gjallarOS Secure Boot there is no recovery partition, even
	// one an older installer already made (e2e-genluks 2026-10-19: the
	// notice said no recovery partition, then the run offered to format
	// the leftover one).
	if !s.user.SecureBootEnable && !explicitTarget {
		if s.recoveryPartition != "" {
			fmt.Fprintf(ui.Out, "The recovery partition %s stays unused: it needs gjallarOS Secure Boot.\n", s.recoveryPartition)
		}
		s.recoveryPartition = ""
		return nil
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
		if s.user.Management.Enable && !s.existing {
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
			s.recoveryDeclined = true
			if s.user.Management.Enable && !s.existing {
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

// systemDiskOnUSB says why USB blocking must stay off because of the disk the
// system runs from, or returns "" when that disk is known and not on USB
// (targetdisk.USBSystemDiskReason). An installed host runs from the disk
// under root; a fresh install from freshTarget.
func systemDiskOnUSB(ctx context.Context, persistentInstalledHost bool, root, freshTarget string) string {
	if persistentInstalledHost {
		return targetdisk.MountedUSBReason(ctx, root)
	}
	return targetdisk.USBSystemDiskReason(ctx, freshTarget)
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
func collectJODSManagement(ctx context.Context, ui prompt.UI, blocker string, running, u *config.User) error {
	if blocker != "" {
		u.Management.Enable = false
		fmt.Fprintf(ui.Out, "No JODS management: %s\n", blocker)
		return nil
	}
	var err error
	u.Management.Enable, err = ui.Confirm(ctx, "Manage this machine with JODS?", running != nil && running.Management.Enable)
	return err
}

// managementBlocker names why this machine cannot be a managed
// endpoint, or returns "" when it can.
func managementBlocker(secureBootSupported, tpm2 bool) string {
	switch {
	case !tpm2:
		return "a managed machine needs TPM2, and none was detected."
	case !secureBootSupported:
		return "a managed machine needs gjallarOS Secure Boot, and ODDC has no Secure Boot setup for this machine."
	}
	return ""
}

// The USB trust choices (Q-K). Blocking is listed only where it can work.
const (
	usbTrustOff    = "Off"
	usbTrustReview = "Review new devices"
	usbTrustBlock  = "Review and block unknown devices from the first boot"
)

// DefaultUSBTrustLapseDays is how long an approved USB device stays
// approved without use on a new install. Settings > Security changes it.
const DefaultUSBTrustLapseDays = 30

// collectUSBTrust asks for USB trust as one choice. It is offered only with
// an ODDC model: the system refuses USB trust without one, since the model
// names the internal devices that must stay trusted. Blocking is offered
// only with TPM2 (the approved-device list is signed by a TPM key the
// system creates on first boot), an encrypted root (turning blocking off
// asks for the disk passphrase) and a system disk that is not on USB
// (usbSystemDisk says why not, see systemDiskOnUSB). Approvals expire after
// DefaultUSBTrustLapseDays without use; a rerun keeps the running value.
func collectUSBTrust(ctx context.Context, ui prompt.UI, modelSelected, encryptedRoot, tpm2 bool, usbSystemDisk string, running, u *config.User) error {
	was := config.User{}
	if running != nil {
		was = *running
	}
	u.USBTrustEnforce = false
	u.USBTrustLapseDays = 0
	if !modelSelected {
		u.USBGuardEnable = false
		u.USBTrustTPMHandle = ""
		fmt.Fprintln(ui.Out, "No USB trust review: ODDC has no model for this machine, and USB trust needs one to know its internal devices.")
		return nil
	}
	noBlock := ""
	switch {
	case !tpm2:
		noBlock = "it needs TPM2 to sign the list of approved devices."
	case usbSystemDisk != "":
		noBlock = usbSystemDisk
	case !encryptedRoot:
		noBlock = "turning it off again needs the disk passphrase, and this root is not encrypted."
	}
	fmt.Fprintln(ui.Out, "USB devices: review logs each new USB device so you can approve it.")
	options := []string{usbTrustOff, usbTrustReview}
	if noBlock == "" {
		options = append(options, usbTrustBlock)
		fmt.Fprintln(ui.Out, "With blocking, only built-in devices and devices you approve work. A USB keyboard or mouse that is not built in stays blocked until you approve it with the built-in keyboard.")
	} else {
		fmt.Fprintf(ui.Out, "Blocking is not offered: %s\n", noBlock)
	}
	// A new install defaults to review (Jade 2026-11-15); a rerun keeps
	// what the machine runs.
	def := usbTrustReview
	if running != nil && !was.USBGuardEnable {
		def = usbTrustOff
	}
	if was.USBTrustEnforce && noBlock == "" {
		def = usbTrustBlock
	}
	choice, err := ui.Choice(ctx, "USB devices", def, options)
	if err != nil {
		return err
	}
	u.USBGuardEnable = choice != usbTrustOff
	u.USBTrustEnforce = choice == usbTrustBlock
	if !u.USBGuardEnable || !tpm2 {
		// Approvals are kept only under the TPM key.
		u.USBTrustTPMHandle = ""
		return nil
	}
	if u.USBTrustTPMHandle == "" {
		u.USBTrustTPMHandle = config.DefaultUSBTrustTPMHandle
	}
	// A rerun keeps the expiry the machine has, 0 included, once USB trust
	// ran with a signed list; a value set while it was off counts too.
	u.USBTrustLapseDays = DefaultUSBTrustLapseDays
	if (was.USBGuardEnable && was.USBTrustTPMHandle != "") || was.USBTrustLapseDays > 0 {
		u.USBTrustLapseDays = was.USBTrustLapseDays
	}
	if u.USBTrustLapseDays > 0 {
		fmt.Fprintf(ui.Out, "Approved devices expire after %d days without use and then ask again; Settings > Security changes this.\n", u.USBTrustLapseDays)
		fmt.Fprintln(ui.Out, "Without a built-in USB keyboard, the keyboard and mouse you used last never expire, so you cannot lock yourself out.")
	}
	return nil
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
			"PASS: recovery partition already present at %s; no storage changes were made\n",
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
			"PASS: existing encrypted-Btrfs layout has an exact 12 GiB recovery partition at %s\n",
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

// findRecoveryPartition finds the recovery partition of the running system:
// only on the disk under /, so a second disk or a stick with the same GPT
// name is never picked (Plane 307). "" means that disk has none.
var findRecoveryPartition = func(ctx context.Context) (string, error) {
	disk, err := targetdisk.MountedDisk(ctx, "/")
	if err != nil {
		return "", fmt.Errorf("find the system disk: %w", err)
	}
	return targetdisk.PartitionByLabel(ctx, disk, diskplan.RecoveryPartLabel)
}

// needsRecoveryStorage says whether the rebuild still has to find or make
// the recovery partition. A "no" to the recovery question also leaves
// recoveryPartition empty, and the storage check would then find the
// partition again and format it (e2e-fw13 2026-10-20).
func needsRecoveryStorage(s state) bool {
	return s.user.RecoveryEnable &&
		s.user.RecoveryPartitionEnable &&
		s.recoveryPartition == "" &&
		!s.recoveryDeclined
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
		found, err := targetdisk.PartitionByLabel(ctx, s.recoveryDisk, diskplan.RecoveryPartLabel)
		if err != nil {
			return fmt.Errorf("find the new recovery partition: %w", err)
		}
		if found == "" {
			return fmt.Errorf("recovery partition creation completed but %s has no %s partition", s.recoveryDisk, diskplan.RecoveryPartLabel)
		}
		s.recoveryPartition = found
	}
	// The recovery image reads the untracked keyboard file written into the
	// stage below, so a git+file build would miss it; build from the staged
	// source as deploy.Apply does. That also works when installer-resume.service runs
	// as root, where Nix refuses the user-owned Git repository (e2e-full,
	// 2026-10-05).
	keyboard, err := recoveryKeyboardFor(s.user.KeyboardLayout, s.user.KeyboardVariant)
	if err != nil {
		return fmt.Errorf("build recovery image: %w", err)
	}
	source, err := flakesource.Stage(root, "")
	if err != nil {
		return fmt.Errorf("build recovery image: %w", err)
	}
	defer source.Close()
	if err := writeRecoveryKeyboard(source.Dir, keyboard); err != nil {
		return fmt.Errorf("build recovery image: %w", err)
	}
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
	staged, release, err := stageRecoveryRelease(ctx, releaseDir, images[0])
	if err != nil {
		return fmt.Errorf("stage recovery image release: %w", err)
	}
	defer privilegedCommand(context.WithoutCancel(ctx), "rm", "-rf", "--", staged)
	args := append([]string{installScript, s.recoveryPartition, images[0]}, release...)
	if err := attached(ctx, "sudo", args...); err != nil {
		return fmt.Errorf("install recovery partition: %w", err)
	}
	return nil
}

// armSecureBootResume makes the first boot with Secure Boot verified run
// the installer's final checks (installer-post-secure-boot).
func armSecureBootResume(ctx context.Context, out io.Writer) error {
	if err := attached(ctx, "sudo", "install", "-d", "-m", "0700", "/var/lib/gjallarOS"); err != nil {
		return fmt.Errorf("create installer continuation directory: %w", err)
	}
	if err := attached(ctx, "sudo", "install", "-m", "0600", "/dev/null", installerSecureBootResumeMarker); err != nil {
		return fmt.Errorf("arm installer continuation across Secure Boot reboot: %w", err)
	}
	fmt.Fprintln(out, "Installer continuation armed across Secure Boot reboot.")
	fmt.Fprintln(out, "After Secure Boot is verified, gjallarOS will automatically run the final installer checks.")
	return nil
}

// buildShimFacts builds the pinned shim from the checkout and reads what it
// needs from the firmware. The running system is not gjallarOS yet, so it
// has no /etc/gjallar/shim.json.
func buildShimFacts(ctx context.Context, root string) (secureboot.ShimFacts, error) {
	source, err := flakesource.Stage(root, "")
	if err != nil {
		return secureboot.ShimFacts{}, fmt.Errorf("build shim: %w", err)
	}
	defer source.Close()
	paths, err := buildStaged(ctx, source, "gjallar-shim")
	if err != nil {
		return secureboot.ShimFacts{}, fmt.Errorf("build shim: %w", err)
	}
	for _, path := range strings.Fields(paths) {
		facts := filepath.Join(path, "share", "shim", "shim.json")
		if _, err := os.Stat(facts); err == nil {
			return secureboot.LoadShimFacts(facts)
		}
	}
	return secureboot.ShimFacts{}, errors.New("the shim build has no share/shim/shim.json")
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
	out io.Writer,
) error {
	u := s.user
	if u.AIAgentMode == "" {
		u.AIAgentMode = "workspace"
	}
	g, err := graphics.Detect(ctx)
	if err != nil {
		return err
	}
	ai, err := pickLocalAI(ctx, &s.user, s.preset, out)
	if err != nil {
		return err
	}
	u.Apps = s.user.Apps
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

// detectLocalAI is profile.DetectWith; tests swap it.
var detectLocalAI = profile.DetectWith

// pickLocalAI picks the local AI model for the ai app. Without local AI, or
// with a central AI server, there is no model to pick: an empty aiModel makes
// a later switch to local AI stop at the Nix assertion instead of pulling a
// model sized for some other machine.
//
// The interview no longer asks about AI (O4), so a rerun keeps the running
// apps. When no model fits this machine (a disk moved to a smaller one), an
// interactive run turns local AI off and says so; gjallarctl ai enable
// explains the ways to turn it on anyway. A preset names its apps itself and
// stops instead.
func pickLocalAI(ctx context.Context, u *config.User, preset bool, out io.Writer) (profile.Result, error) {
	if !u.HasApp("ai") || u.AIEndpoint != "" {
		return profile.Result{}, nil
	}
	// u holds the override from either the preset file or the running
	// config; the file does not exist yet in a new install.
	override, err := profile.NewOverride(u.OverrideAISelection, u.OverrideModelWith)
	if err != nil {
		return profile.Result{}, fmt.Errorf("overrideModelWith: %w", err)
	}
	ai, err := detectLocalAI(ctx, override)
	if err != nil {
		return profile.Result{}, err
	}
	if ai.Model != "" {
		return ai, nil
	}
	if preset {
		return profile.Result{}, fmt.Errorf("the preset enables local AI, but no local model fits %d GiB RAM (8 GiB needed): set aiEndpoint, set overrideAiSelection with overrideModelWith, or remove \"ai\" from apps", ai.RAMGB)
	}
	u.Apps = slices.DeleteFunc(slices.Clone(u.Apps), func(id string) bool { return id == "ai" })
	fmt.Fprintf(out, "Local AI is turned off: no local model fits %d GiB RAM (8 GiB needed). After the install, gjallarctl ai enable lists the ways to turn it on anyway.\n", ai.RAMGB)
	return profile.Result{}, nil
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
	recordFailure(out, err)
	fmt.Fprintln(out, "ERROR:", err)
	installlog.Mark(installlog.RunFailed)
	return 1
}

// applyChannelFlag sets the release channel --channel names (Plane 297).
// A preset already says which channel it follows; a different flag is a
// mistake to show, not a choice to make silently.
func applyChannelFlag(user *config.User, channel string, preset bool, presetPath string) error {
	if channel == "" {
		return nil
	}
	if !slices.Contains(config.ReleaseChannels, channel) {
		return fmt.Errorf("unknown release channel %q; use %s", channel, strings.Join(config.ReleaseChannels, ", "))
	}
	if preset && user.Channel() != channel {
		return fmt.Errorf("%s follows the %s channel; change releaseChannel there or leave out --channel %s", presetPath, user.Channel(), channel)
	}
	user.ReleaseChannel = channel
	return nil
}

// stableOnUnencryptedDisk keeps staging, dev and developer mode off an
// unencrypted system disk (Plane 283, wiki 752 Q212). Their confirmation
// needs the security chip PIN or the disk recovery key; a plain disk has
// neither, so every system rebuild after the install would be refused
// (e2e-shim run 9, 2026-11-10). A fresh disk is always encrypted, so this
// only stops in-place runs. The person may switch to stable instead; a
// preset then says stable too.
func stableOnUnencryptedDisk(ctx context.Context, ui confirmer, out io.Writer, encrypted bool, user *config.User, preset bool, presetPath string) error {
	need := devgate.Needs(user.Channel(), user.DeveloperMode)
	if encrypted || need.Empty() {
		return nil
	}
	var what []string
	if need.Channels {
		what = append(what, "the "+user.Channel()+" channel")
	}
	if need.DeveloperMode {
		what = append(what, "developer mode")
	}
	want := strings.Join(what, " and ")
	needs := "needs"
	if len(what) > 1 {
		needs = "need"
	}
	fmt.Fprintf(out, "The system disk is not encrypted. %s %s an encrypted disk: confirming it takes the security chip PIN or the disk recovery key, and this disk has neither, so every system update would be refused.\n", upperFirst(want), needs)
	ok, err := ui.Confirm(ctx, "Install with the stable channel and developer mode off instead?", true)
	if err != nil || !ok {
		fix := "run the installer again with --channel stable"
		if preset {
			fix = "set releaseChannel to stable and developerMode to false in " + presetPath + " and run the installer again"
		}
		return fmt.Errorf("%s %s an encrypted system disk; nothing was changed. Install on a fresh disk to get encryption, or %s", want, needs, fix)
	}
	user.ReleaseChannel = "stable"
	user.DeveloperMode = false
	// The system rebuild reads the channel from the preset, not from
	// this run (e2e-shim, 2026-11-11).
	if preset {
		if err := config.WriteAtomic(presetPath, *user); err != nil {
			return fmt.Errorf("save the stable channel to %s: %w", presetPath, err)
		}
		fmt.Fprintf(out, "Following the stable channel; developer mode is off. Saved in %s.\n", presetPath)
		return nil
	}
	fmt.Fprintln(out, "Following the stable channel; developer mode is off.")
	return nil
}

type confirmer interface {
	Confirm(ctx context.Context, message string, defaultYes bool) (bool, error)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// developerConfirmNotice says how a fresh install of a dev or staging
// channel or with developer mode gets its developer confirmation (Plane
// 283, Q192). With chip unlock the first start records it while the
// person sets up the chip; without it the person confirms once by hand,
// and system rebuilds wait until then.
func developerConfirmNotice(user config.User, existing, tpm2 bool) string {
	if existing || devgate.Needs(user.Channel(), user.DeveloperMode).Empty() {
		return ""
	}
	what := "the " + user.Channel() + " channel"
	if !devgate.Needs(user.Channel(), false).Channels {
		what = "developer mode"
	}
	if tpm2 {
		return "The first start confirms " + what + " when you set up the security chip unlock."
	}
	return "To keep " + what + ", run gjallarctl luks recovery-key after the first start and then gjallarctl settings confirm-developer once with the recovery key; system rebuilds wait until then."
}

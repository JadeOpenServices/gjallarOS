package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/baremetalinstall"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/freshdiskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/freshgpt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/hardwareconfig"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installconfirm"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/mounttree"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/rootprovision"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

type freshBareMetalResult struct {
	TargetDisk        string
	RootPARTUUID      string
	LUKSUUID          string
	RecoveryPartition string
	// ReleaseTargetState unmounts the target state bound over the live host
	// paths; call it when the installer is done with the target.
	ReleaseTargetState func()
}

func runFreshBareMetal(
	ctx context.Context,
	ui prompt.UI,
	repo string,
	targetPath string,
	hostname string,
	revision string,
	hardware discovery.Hardware,
	resolvedDevice oddc.Resolved,
	recovery bool,
	enableRecovery bool,
	passwordFiles []string,
	hostOverlay *oddc.HostOverlay,
	out io.Writer,
) (freshBareMetalResult, error) {
	if out == nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"fresh installation output writer is required",
		)
	}

	targetPath = strings.TrimSpace(targetPath)

	fmt.Fprintln(out, "STAGE: validating fresh installation target")

	observed, err := targetdisk.Validate(
		ctx,
		targetdisk.Input{
			Path: targetPath,
		},
	)
	if err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"validate fresh target disk: %w",
			err,
		)
	}

	plan, err := freshdiskplan.Build(
		freshdiskplan.Input{
			Observed:       observed,
			EnableRecovery: enableRecovery,
		},
	)
	if err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"build canonical fresh disk plan: %w",
			err,
		)
	}

	// Never infer unattended authorization from managed-device, recovery, JODS,
	// preset, or any other feature state. Interactive fresh installs require
	// the exact destructive phrase here.
	if err := installconfirm.Confirm(
		ctx,
		ui,
		out,
		plan,
		observed,
		installconfirm.Options{
			Unattended: false,
		},
	); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"fresh disk authorization: %w",
			err,
		)
	}

	fmt.Fprintln(out, "STAGE: creating fresh canonical partition table")

	if _, err := freshgpt.Provision(
		ctx,
		freshgpt.Input{
			Plan: plan,
			Out:  out,
		},
	); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"provision fresh GPT: %w",
			err,
		)
	}

	passphrase, err := readFreshLUKSPassphrase(out)
	if err != nil {
		return freshBareMetalResult{}, err
	}
	secret := []byte(passphrase)
	passphrase = ""
	defer zero(secret)

	fmt.Fprintln(out, "STAGE: provisioning encrypted Btrfs root")

	rootResult, err := rootprovision.Provision(
		ctx,
		rootprovision.Input{
			Plan:       plan,
			Passphrase: secret,
			Out:        out,
		},
	)
	if err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"provision encrypted root: %w",
			err,
		)
	}

	// Fresh canonical storage has exactly one owner. mounttree prepares the
	// ESP, encrypted Btrfs root mount, and the exact 12 GiB JODS-RECOVERY
	// partition described by the canonical disk plan. Recovery artifact
	// population is a later trust operation and must not own filesystem layout.
	fmt.Fprintln(out, "STAGE: preparing fresh target mount tree")

	if _, err := mounttree.Prepare(
		ctx,
		mounttree.Input{
			Plan: plan,
			Out:  out,
		},
	); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"prepare fresh target mount tree: %w",
			err,
		)
	}

	fmt.Fprintln(
		out,
		"STAGE: materializing machine-local device profile into target",
	)

	profileDestination := filepath.Join(
		rootResult.MountPoint,
		"var",
		"lib",
		"gjallarOS",
		"device-profile",
	)
	if err := materializeODDCCapsule(
		ctx,
		repo,
		profileDestination,
		hardware,
		resolvedDevice,
		recovery,
		"initial",
	); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"materialize fresh target device profile: %w",
			err,
		)
	}
	if resolvedDevice.ModelID == "" {
		stageODDCDraft(ctx, profileDestination, out)
	}

	fmt.Fprintln(out, "STAGE: staging account password hashes into target")

	if err := stageFreshPasswordFiles(ctx, passwordFiles); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"stage fresh target password hashes: %w",
			err,
		)
	}

	releaseTargetState, err := bindFreshTargetState(ctx, rootResult.MountPoint, out)
	if err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"move installer state onto fresh target: %w",
			err,
		)
	}
	keepTargetState := false
	defer func() {
		if !keepTargetState {
			releaseTargetState()
		}
	}()

	hardwarePath := hardwareconfig.Target(repo)

	fmt.Fprintln(out, "STAGE: generating target hardware configuration")

	if _, err := hardwareconfig.GenerateTarget(
		ctx,
		repo,
		hardwarePath,
		time.Now(),
	); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"generate fresh target hardware configuration: %w",
			err,
		)
	}

	fmt.Fprintln(out, "STAGE: installing GjallarOS into prepared target")

	if _, err := baremetalinstall.Install(
		ctx,
		baremetalinstall.Input{
			Repo:     repo,
			Hostname: hostname,
			Out:      out,
		},
	); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"install fresh GjallarOS: %w",
			err,
		)
	}

	if err := saveODDCHostOverlay(
		ctx,
		rootResult.MountPoint,
		hostOverlay,
	); err != nil {
		return freshBareMetalResult{}, fmt.Errorf(
			"commit fresh target ODDC host state: %w",
			err,
		)
	}

	if hostOverlay != nil {
		fmt.Fprintln(
			out,
			"PASS: machine-local ODDC host state committed to fresh target.",
		)
	}

	fmt.Fprintln(out, "STAGE: cloning the system checkout into the target")

	if err := cloneSystemCheckout(
		ctx,
		repo,
		rootResult.MountPoint,
		revision,
		out,
	); err != nil {
		return freshBareMetalResult{}, err
	}

	result := freshBareMetalResult{
		TargetDisk:         plan.TargetDisk.Path,
		RootPARTUUID:       plan.Root.Partition.PARTUUID,
		LUKSUUID:           rootResult.LUKSUUID,
		ReleaseTargetState: releaseTargetState,
	}
	keepTargetState = true

	if plan.Recovery != nil {
		// Return stable identity, never /dev/nvmeXpN naming as authority.
		result.RecoveryPartition =
			"/dev/disk/by-partuuid/" +
				strings.ToLower(plan.Recovery.PARTUUID)
	}

	fmt.Fprintln(
		out,
		"PASS: canonical fresh bare-metal installation completed",
	)

	return result, nil
}

func stageFreshPasswordFiles(ctx context.Context, paths []string) error {
	const sourceRoot = "/var/lib/gjallarOS/passwords"
	const targetRoot = "/mnt/var/lib/gjallarOS/passwords"

	// Both directories are root-only; the hashes are copied root-to-root via
	// sudo and never pass through the unprivileged installer process.
	if _, err := privilegedCommand(ctx, "mkdir", "-p", "-m", "0700", "--", targetRoot); err != nil {
		return fmt.Errorf("create target password directory: %w", err)
	}

	seen := map[string]bool{}

	for _, source := range paths {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}

		source = filepath.Clean(source)

		rel, err := filepath.Rel(sourceRoot, source)
		if err != nil {
			return fmt.Errorf("resolve password hash path %q: %w", source, err)
		}

		if rel == "." ||
			filepath.IsAbs(rel) ||
			rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
			strings.Contains(rel, string(filepath.Separator)) {
			return fmt.Errorf(
				"password hash is outside canonical runtime directory: %s",
				source,
			)
		}

		if seen[rel] {
			continue
		}
		seen[rel] = true

		// find -P never follows symlinks: only a non-empty regular file without
		// group/other permission bits is printed.
		checked, err := privilegedCommand(
			ctx, "find", source, "-maxdepth", "0", "-type", "f", "!", "-perm", "/077", "-size", "+0",
		)
		if err != nil {
			return fmt.Errorf("inspect password hash %s: %w", source, err)
		}
		if strings.TrimSpace(string(checked)) != source {
			return fmt.Errorf(
				"password hash must be a non-empty regular non-symlink file with mode 0600: %s",
				source,
			)
		}

		destination := filepath.Join(targetRoot, rel)
		protectedTmp := filepath.Join(targetRoot, "."+rel+".tmp")
		if _, err := privilegedCommand(
			ctx, "install", "-m", "0600", "-o", "root", "-g", "root", "--", source, protectedTmp,
		); err != nil {
			return fmt.Errorf("install target password hash %s: %w", destination, err)
		}
		if _, err := privilegedCommand(ctx, "mv", "-f", "--", protectedTmp, destination); err != nil {
			_, _ = privilegedCommand(ctx, "rm", "-f", "--", protectedTmp)
			return fmt.Errorf("install target password hash %s: %w", destination, err)
		}
	}

	return nil
}

func readFreshLUKSPassphrase(out io.Writer) (string, error) {
	// The encryption secret must not use prompt.UI.Value because that would
	// echo/log normal text input. Read directly from the controlling terminal
	// with echo disabled.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf(
			"open controlling terminal for LUKS passphrase: %w",
			err,
		)
	}
	defer tty.Close()

	passphrase, err := credential.ReadConfirmedPassword(
		tty,
		out,
		"encrypted GjallarOS root",
	)
	if err != nil {
		return "", fmt.Errorf(
			"read LUKS passphrase: %w",
			err,
		)
	}

	if passphrase == "" {
		return "", fmt.Errorf("LUKS passphrase must not be empty")
	}

	return passphrase, nil
}

func zero(secret []byte) {
	for i := range secret {
		secret[i] = 0
	}
}

// Compile-time contract: fresh canonical storage is a diskplan.Plan and does
// not gain another parallel storage-model representation.
var _ diskplan.Plan

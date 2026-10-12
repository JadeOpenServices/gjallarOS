package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deploy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddcvalidation"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

const secureBootUsage = "Usage: gjallarctl secure-boot {status|setup|update-dbx} [--repo PATH]"

const dbxUpdateTool = "gjallar-secure-boot-dbx-update"

// runSecureBoot sets up Secure Boot on an installed system the way the
// installer does: same ownership checks, recovery gate, verification and
// firmware handoff (internal/installer/secureboot/flow.go). The boot
// services from system/security/secure-boot then enroll and verify.
func runSecureBoot(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, secureBootUsage)
		return 0
	}
	repo := ""
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] != "--repo" || i+1 >= len(rest) {
			fmt.Fprintln(stderr, secureBootUsage)
			return 2
		}
		i++
		repo = rest[i]
	}
	ctx := context.Background()

	switch args[0] {
	case "status":
		return runSecureBootStatus(ctx, stdout, stderr)
	case "setup":
		return runSecureBootSetup(ctx, repo, stdout, stderr)
	case "update-dbx":
		if repo != "" {
			fmt.Fprintln(stderr, secureBootUsage)
			return 2
		}
		return execRootTool(dbxUpdateTool, "Secure Boot is not enabled on this system", stderr)
	default:
		fmt.Fprintln(stderr, secureBootUsage)
		return 2
	}
}

func runSecureBootStatus(ctx context.Context, stdout, stderr io.Writer) int {
	inspection, err := secureboot.Inspect(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Ownership: %s\n  %s\n", inspection.State, inspection.Description)
	if inspection.SecureBoot {
		fmt.Fprintln(stdout, "Firmware:  Secure Boot is on")
	} else {
		fmt.Fprintln(stdout, "Firmware:  Secure Boot is off")
	}
	return 0
}

func runSecureBootSetup(ctx context.Context, repo string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	repo, err := installercheck.DiscoverRepository(repo)
	if err != nil {
		return fail(fmt.Errorf("resolve GjallarOS repository: %w", err))
	}
	configPath := filepath.Join(repo, "user.config.json")
	user, err := config.Load(configPath)
	if err != nil {
		return fail(err)
	}

	device, err := oddcvalidation.LoadDeviceContext(repo)
	if err != nil {
		return fail(err)
	}
	firmware, err := oddc.ResolveSecureBootFirmwarePolicy(device.Resolved)
	if err != nil {
		return fail(fmt.Errorf("resolve Secure Boot setup for this model: %w", err))
	}
	if device.Resolved.ModelID == "" || firmware.SourceEntity == "" || !firmware.Policy.Supported {
		reason := "ODDC has no Secure Boot setup for this model"
		if firmware.Policy.UnsupportedReason != "" {
			reason += ": " + firmware.Policy.UnsupportedReason
		}
		return fail(errors.New(reason + ". Nothing was changed."))
	}

	ui := prompt.UI{Reader: bufio.NewReader(os.Stdin), Out: stdout}

	if !user.SecureBootEnable {
		fmt.Fprint(stdout, secureboot.OwnershipPlan(firmware.Policy, user.LUKSTPM2Enable))
		question := "Set up Secure Boot with GjallarOS keys?"
		if name := strings.TrimSpace(firmware.Policy.FirmwareName); name != "" {
			question = "Set up Secure Boot with GjallarOS keys on " + name + "?"
		}
		yes, err := ui.Confirm(ctx, question, false)
		if err != nil {
			return fail(err)
		}
		if !yes {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return 0
		}

		if err := secureboot.Prepare(ctx, stdout); err != nil {
			return fail(err)
		}
		if err := config.SetBool(configPath, "secureBootEnable", true); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "secureBootEnable is on in user.config.json; building the signed boot files.")
		if err := deploy.Apply(ctx, repo, user.Hostname); err != nil {
			// The running system and its config stay in step; the keys and
			// the pending record stay for the next run.
			if revert := config.SetBool(configPath, "secureBootEnable", false); revert != nil {
				fmt.Fprintf(stderr, "ERROR: turn secureBootEnable off again: %v\n", revert)
			}
			return fail(err)
		}
	} else if err := secureboot.Prepare(ctx, stdout); err != nil {
		return fail(err)
	}

	next, err := secureboot.Continue(
		ctx,
		ui,
		stdout,
		device.Resolved.ModelID,
		firmware,
		user.FirmwarePasswordLock,
	)
	if err != nil {
		return fail(err)
	}
	if next == secureboot.ContinuationNone {
		return offerDBXUpdate(ctx, ui, stdout, stderr)
	}
	if !secureboot.NeedsFirmwareReboot(next) {
		return 0
	}

	reboot, err := ui.Confirm(ctx, "Restart into firmware setup now?", true)
	if err != nil {
		return fail(err)
	}
	if !reboot {
		fmt.Fprintln(stdout, "Restart into firmware setup when you are ready: systemctl reboot --firmware-setup")
		return 0
	}
	cmd := exec.CommandContext(ctx, "sudo", "systemctl", "reboot", "--firmware-setup")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fail(fmt.Errorf("restart into firmware setup: %w", err))
	}
	return 0
}

// offerDBXUpdate runs once Secure Boot is active: the dbx the firmware
// shipped with may be years old, and revoked boot loaders stay trusted until
// a newer one is written.
func offerDBXUpdate(ctx context.Context, ui prompt.UI, stdout, stderr io.Writer) int {
	if _, err := exec.LookPath(dbxUpdateTool); err != nil {
		return 0
	}
	yes, err := ui.Confirm(ctx, "Check LVFS for a newer dbx revocation list now?", true)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if !yes {
		fmt.Fprintln(stdout, "Check later with: gjallarctl secure-boot update-dbx")
		return 0
	}
	argv := rootToolArgv(dbxUpdateTool, os.Geteuid())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "ERROR: update dbx: %v\n", err)
		return 1
	}
	return 0
}

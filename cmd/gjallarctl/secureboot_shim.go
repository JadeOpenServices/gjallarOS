package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deploy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddcvalidation"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// chooseShimPath decides between the shim path and gjallarOS firmware keys
// (Plane 293, Jade Q189): shim wherever the firmware keeps its maker's keys
// and would start the pinned shim, own keys on request, in Setup Mode or
// when db/dbx rule shim out.
func chooseShimPath(ctx context.Context, user config.User, ownKeys bool, out io.Writer) (bool, error) {
	if user.SecureBootShim {
		if ownKeys {
			return false, errors.New("secureBootShim is on, so this system uses shim; --own-keys needs secureBootShim off and Secure Boot off first")
		}
		return true, nil
	}
	if ownKeys {
		return false, nil
	}
	// The key files and the ownership record are root-only; say so before
	// sudo asks, so the password prompt does not come out of nowhere.
	fmt.Fprintln(out, "Checking the Secure Boot keys and records (root-only files) before showing the plan.")
	inspection, err := secureboot.Inspect(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect Secure Boot ownership: %w", err)
	}
	switch {
	case inspection.Stage == secureboot.StagePendingMOK, inspection.State == secureboot.StateGjallarShim:
		return true, nil
	case inspection.State != secureboot.StateOEMFactoryDerived:
		// Own keys are set up or pending, or the firmware is in a state
		// only that flow reports on.
		return false, nil
	}

	facts, err := secureboot.LoadShimFacts(secureboot.ShimFactsPath)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(out, "This system has no pinned shim; using gjallarOS firmware keys.")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	trust, err := secureboot.ReadFirmwareTrust()
	if err != nil {
		return false, fmt.Errorf("read firmware trust: %w", err)
	}
	choice, err := secureboot.ChoosePath(trust, facts, nil)
	if err != nil {
		return false, err
	}
	if choice.Path == secureboot.PathShim {
		fmt.Fprintf(out, "Secure Boot path: shim (%s).\n", choice.Reason)
		return true, nil
	}
	fmt.Fprintf(out, "Secure Boot path: gjallarOS firmware keys (%s).\n", choice.Reason)
	return false, nil
}

// runShimSetup is `gjallarctl secure-boot setup` on the shim path. Each run
// does the next step and says which restart comes next.
func runShimSetup(ctx context.Context, repo, configPath string, user config.User, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	firmwareName, tested := shimFirmware(repo)
	ui := prompt.UI{Reader: bufio.NewReader(os.Stdin), Out: stdout}

	if !user.SecureBootEnable || !user.SecureBootShim {
		if !tested {
			// Same best-effort offer as the installer (Jade Q221).
			fmt.Fprintln(stdout, untestedShimNotice)
		}
		fmt.Fprint(stdout, secureboot.ShimPlan(firmwareName, user.LUKSTPM2Enable))
		question := "Set up Secure Boot through shim?"
		if firmwareName != "" {
			question = "Set up Secure Boot through shim on " + firmwareName + "?"
		}
		yes, err := ui.Confirm(ctx, question, false)
		if err != nil {
			return fail(err)
		}
		if !yes {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return 0
		}
		if err := secureboot.PrepareShim(ctx, stdout); err != nil {
			return fail(err)
		}
		settings := map[string]any{"secureBootShim": true, "secureBootEnable": true}
		if onSystemCheckout(configPath) {
			// Same as the own-keys path: root writes the system checkout and
			// a critical change waits; the key and record stay for next run.
			if code := askSystemSettings(settings, stdout, stderr); code != 0 {
				return code
			}
			if user, err = config.Load(configPath); err != nil {
				return fail(err)
			}
			if !user.SecureBootEnable || !user.SecureBootShim {
				fmt.Fprintln(stdout, "Once the change is applied and the rebuild is done, restart and run gjallarctl secure-boot setup again.")
				return 0
			}
		} else {
			for _, key := range []string{"secureBootShim", "secureBootEnable"} {
				if err := config.SetBool(configPath, key, true); err != nil {
					return fail(err)
				}
			}
			fmt.Fprintln(stdout, "secureBootShim and secureBootEnable are on in user.config.json; building the signed boot files.")
			if err := deploy.Apply(ctx, repo, user.Hostname); err != nil {
				// Keep the running system and its config in step.
				for _, key := range []string{"secureBootEnable", "secureBootShim"} {
					if revert := config.SetBool(configPath, key, false); revert != nil {
						fmt.Fprintf(stderr, "ERROR: turn %s off again: %v\n", key, revert)
					}
				}
				return fail(err)
			}
		}
	} else if err := secureboot.PrepareShim(ctx, stdout); err != nil {
		return fail(err)
	}

	next, err := secureboot.ContinueShim(ctx, ui, stdout, firmwareName, user.FirmwarePasswordLock)
	if err != nil {
		return fail(err)
	}
	if next == secureboot.ContinuationNone {
		return offerDBXUpdate(ctx, ui, stdout, stderr)
	}
	return restartFor(ctx, ui, next, stdout, stderr)
}

// untestedShimNotice goes before the shim question on a model without an
// ODDC Secure Boot setup: best effort, the question stays default no.
const untestedShimNotice = "Nobody has tested Secure Boot through shim on this model yet. It needs nothing model-specific, only a firmware that keeps its factory keys; the checks above passed."

// shimFirmware returns the firmware's name from ODDC, for the messages
// only, and whether ODDC has a tested Secure Boot setup for this model.
// Missing ODDC data counts as untested.
func shimFirmware(repo string) (string, bool) {
	device, err := oddcvalidation.LoadDeviceContext(repo)
	if err != nil || device.Resolved.ModelID == "" {
		return "", false
	}
	firmware, err := oddc.ResolveSecureBootFirmwarePolicy(device.Resolved)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(firmware.Policy.FirmwareName), firmware.SourceEntity != "" && firmware.Policy.Supported
}

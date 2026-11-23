// Package installconfirm implements the explicit authorization gate that must
// precede destructive bare-metal disk provisioning.
//
// The package itself performs no disk modification. It validates that the
// canonical plan still refers to the previously validated physical disk,
// prints/logs the exact non-secret installation plan, and requires either two
// explicit default-No interactive confirmations or explicitly configured unattended mode.
package installconfirm

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

// Options controls only the destructive-confirmation policy.
//
// Unattended must come from explicit unattended-install configuration. Recovery,
// JODS, managed-device, or other feature state must never be mapped into it.
type Options struct {
	Unattended bool
}

// Confirm validates and displays the exact intended destructive plan.
//
// No destructive operation is performed here.
func Confirm(
	ctx context.Context,
	ui prompt.UI,
	out io.Writer,
	plan diskplan.Plan,
	observed targetdisk.Result,
	options Options,
) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate destructive installation plan: %w", err)
	}

	if err := validateIdentity(plan.TargetDisk, observed); err != nil {
		return err
	}

	printPlan(out, plan, observed)

	if options.Unattended {
		fmt.Fprintln(
			out,
			"UNATTENDED: explicit unattendedInstall=true authorizes destructive installation.",
		)
		return nil
	}

	checked, err := ui.Confirm(
		ctx,
		fmt.Sprintf(
			"Have you checked the target disk name thoroughly and confirmed %s is the disk you intend to erase?",
			plan.TargetDisk.Path,
		),
		false,
	)
	if err != nil {
		return fmt.Errorf("confirm target disk identity: %w", err)
	}
	if !checked {
		return fmt.Errorf("destructive installation was not authorized")
	}

	certain, err := ui.Confirm(
		ctx,
		fmt.Sprintf(
			"Are you absolutely sure you want to permanently erase %s?",
			plan.TargetDisk.Path,
		),
		false,
	)
	if err != nil {
		return fmt.Errorf("confirm destructive erase: %w", err)
	}
	if !certain {
		return fmt.Errorf("destructive installation was not authorized")
	}

	fmt.Fprintf(out, "Confirmed: everything on %s will be erased.\n", plan.TargetDisk.Path)
	return nil
}

func validateIdentity(planned diskplan.Disk, observed targetdisk.Result) error {
	if filepath.Clean(planned.Path) != filepath.Clean(observed.Path) {
		return fmt.Errorf(
			"target disk changed after validation: planned %q, observed %q",
			planned.Path,
			observed.Path,
		)
	}

	if strings.TrimSpace(planned.Model) != strings.TrimSpace(observed.Model) {
		return fmt.Errorf("target disk model changed after validation")
	}

	if planned.SizeBytes != observed.SizeBytes {
		return fmt.Errorf(
			"target disk size changed after validation: planned %d, observed %d",
			planned.SizeBytes,
			observed.SizeBytes,
		)
	}

	plannedWWN := strings.TrimSpace(planned.WWN)
	observedWWN := strings.TrimSpace(observed.WWN)
	plannedSerial := strings.TrimSpace(planned.Serial)
	observedSerial := strings.TrimSpace(observed.Serial)

	if plannedWWN != "" {
		if observedWWN == "" || plannedWWN != observedWWN {
			return fmt.Errorf("target disk WWN changed after validation")
		}
		return nil
	}

	if plannedSerial == "" || observedSerial == "" || plannedSerial != observedSerial {
		return fmt.Errorf("target disk serial changed after validation")
	}

	return nil
}

func printPlan(out io.Writer, plan diskplan.Plan, observed targetdisk.Result) {
	fmt.Fprintln(out, "=== DESTRUCTIVE INSTALLATION PLAN ===")
	fmt.Fprintf(out, "Target:   %s\n", plan.TargetDisk.Path)
	fmt.Fprintf(out, "Model:    %s\n", observed.Model)
	fmt.Fprintf(out, "Serial:   %s\n", displayIdentity(observed.Serial))
	fmt.Fprintf(out, "WWN:      %s\n", displayIdentity(observed.WWN))
	fmt.Fprintf(out, "Size:     %d bytes\n", observed.SizeBytes)
	fmt.Fprintf(out, "GPT GUID: %s\n", plan.TargetDisk.GPTDiskGUID)

	printPartition(out, "ESP", plan.ESP)
	printPartition(out, "ROOT", plan.Root.Partition)
	fmt.Fprintf(out, "Root encryption: %s\n", plan.Root.Encryption.Type)

	if plan.Recovery != nil {
		printPartition(out, "RECOVERY", *plan.Recovery)
	} else {
		fmt.Fprintln(out, "Recovery: disabled")
	}

	fmt.Fprintln(out, "=== END DESTRUCTIVE INSTALLATION PLAN ===")
}

func printPartition(out io.Writer, name string, part diskplan.Partition) {
	fmt.Fprintf(
		out,
		"%s: partition=%d size=%d bytes filesystem=%s mount=%s label=%s\n",
		name,
		part.Number,
		part.SizeBytes,
		part.Filesystem.Type,
		part.MountPoint,
		part.Label,
	)
}

func displayIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "<not reported>"
	}
	return value
}

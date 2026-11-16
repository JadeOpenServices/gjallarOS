package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

// freshTargetMinimumBytes is the smallest disk a fresh install takes (Q227):
// ESP 1 GiB + recovery 12 GiB + the system, the install swap file, and room
// for builds and the first update. It is checked before anything is erased,
// for a chosen disk and for --target-disk alike.
const freshTargetMinimumBytes uint64 = 40 << 30

// discoverTargetDisks lists the disks a fresh install could use. Tests swap
// it for a fixed list; the installer always uses targetdisk.Discover.
var discoverTargetDisks = targetdisk.Discover

// validateTargetDiskContext rejects --target-disk on a persistent installed
// host. Fresh installation only runs from a live or recovery environment; an
// installed host takes the in-place path, which would otherwise silently
// ignore the requested disk and modify the running system instead.
func validateTargetDiskContext(targetDisk string, persistentInstalledHost bool) error {
	if strings.TrimSpace(targetDisk) == "" || !persistentInstalledHost {
		return nil
	}
	return fmt.Errorf(
		"--target-disk %s requests a fresh installation, which must run from the gjallarOS/NixOS live or recovery environment; this system boots from a persistent root, so nothing was changed",
		targetDisk,
	)
}

func selectFreshTargetDisk(
	ctx context.Context,
	ui prompt.UI,
	out io.Writer,
	explicit string,
) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		fmt.Fprintf(out, "Using explicitly configured target disk: %s\n", explicit)
		installlog.Mark(installlog.DiskGiven)
		return explicit, nil
	}

	eligible, rejected, err := discoverTargetDisks(
		ctx,
		freshTargetMinimumBytes,
	)
	if err != nil {
		return "", err
	}

	if len(rejected) != 0 {
		fmt.Fprintln(out, "Not offered:")
		for _, item := range rejected {
			fmt.Fprintf(out, "  %s\n", item.Text())
		}
	}

	if len(eligible) == 0 {
		fmt.Fprintln(out, "No eligible installation target disks were found.")
		return "", fmt.Errorf(
			"no eligible whole target disk passed installer safety validation",
		)
	}

	// Q228: every disk shows its bus, the end of its serial, its partitions
	// with their labels, and what the erase destroys, before anything is
	// asked. Nothing is preselected and the one-disk question defaults to No,
	// so Enter alone never erases a disk.
	details := targetDiskDetails(eligible)
	fmt.Fprint(out, details)

	switch len(eligible) {
	case 1:
		selected := eligible[0]
		question := fmt.Sprintf(
			"Erase %s and install gjallarOS on it? It is the only disk found",
			selected.Summary(),
		)
		if ui.GTK {
			question = details + "\n" + question
		}
		yes, err := ui.Confirm(ctx, question, false)
		if err != nil {
			return "", fmt.Errorf("confirm target disk: %w", err)
		}
		if !yes {
			return "", errors.New("no disk chosen; nothing was changed")
		}
		fmt.Fprintf(out, "Selected target disk: %s\n", selected.Path)
		installlog.Mark(installlog.DiskOnlyOneConfirmed)
		return selected.Path, nil
	}

	fmt.Fprintln(out, "Multiple eligible installation target disks detected:")

	options := make([]string, 0, len(eligible))
	paths := make(map[string]string, len(eligible))
	for _, disk := range eligible {
		label := disk.Summary()
		options = append(options, label)
		paths[label] = disk.Path
	}

	label := "Select the whole disk to erase and install gjallarOS onto"
	if ui.GTK {
		label = details + "\n" + label
	}
	selected, err := ui.Choice(ctx, label, "", options)
	if err != nil {
		return "", fmt.Errorf("select target disk: %w", err)
	}

	path, ok := paths[selected]
	if !ok {
		return "", fmt.Errorf(
			"selected target disk is not part of the validated candidate set",
		)
	}

	fmt.Fprintf(out, "Selected target disk: %s\n", path)
	installlog.Mark(installlog.DiskChosen, installlog.Count("disks", len(eligible)))
	return path, nil
}

// targetDiskDetails is the block shown above the disk question: per disk
// its summary line, warnings, and partitions.
func targetDiskDetails(disks []targetdisk.Result) string {
	var b strings.Builder
	fmt.Fprintln(&b, "The disk you choose is erased completely:")
	for _, disk := range disks {
		fmt.Fprintf(&b, "\n  %s\n", disk.Summary())
		for _, warning := range disk.Warnings() {
			fmt.Fprintf(&b, "    ! %s\n", warning)
		}
		for _, line := range disk.Details() {
			fmt.Fprintf(&b, "      %s\n", line)
		}
	}
	fmt.Fprintln(&b)
	return b.String()
}

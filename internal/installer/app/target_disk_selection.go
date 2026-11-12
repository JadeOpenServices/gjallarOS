package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

const freshTargetMinimumBytes uint64 = 8 * 1024 * 1024 * 1024

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
		return explicit, nil
	}

	eligible, rejected, err := targetdisk.Discover(
		ctx,
		freshTargetMinimumBytes,
	)
	if err != nil {
		return "", err
	}

	switch len(eligible) {
	case 0:
		fmt.Fprintln(out, "No eligible installation target disks were found.")

		if len(rejected) != 0 {
			fmt.Fprintln(out, "Rejected whole disks:")
			for _, item := range rejected {
				fmt.Fprintf(
					out,
					"  %s — %s\n",
					item.Path,
					item.Reason,
				)
			}
		}

		return "", fmt.Errorf(
			"no eligible whole target disk passed installer safety validation",
		)

	case 1:
		selected := eligible[0]
		yes, err := ui.Confirm(
			ctx,
			fmt.Sprintf(
				"Install gjallarOS on %s — %s — %s? It is the only disk found",
				selected.Path,
				displayDiskModel(selected.Model),
				formatDiskSize(selected.SizeBytes),
			),
			true,
		)
		if err != nil {
			return "", fmt.Errorf("confirm target disk: %w", err)
		}
		if !yes {
			return "", errors.New("no disk chosen; nothing was changed")
		}
		fmt.Fprintf(out, "Selected target disk: %s\n", selected.Path)
		return selected.Path, nil
	}

	fmt.Fprintln(out, "Multiple eligible installation target disks detected:")

	options := make([]string, 0, len(eligible))
	paths := make(map[string]string, len(eligible))

	for _, disk := range eligible {
		label := fmt.Sprintf(
			"%s — %s — %s",
			disk.Path,
			displayDiskModel(disk.Model),
			formatDiskSize(disk.SizeBytes),
		)

		options = append(options, label)
		paths[label] = disk.Path
	}

	selected, err := ui.Choice(
		ctx,
		"Select the whole disk to erase and install gjallarOS onto",
		options[0],
		options,
	)
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
	return path, nil
}

func displayDiskModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown model"
	}
	return model
}

func formatDiskSize(bytes uint64) string {
	const (
		gib = uint64(1024 * 1024 * 1024)
		tib = uint64(1024 * 1024 * 1024 * 1024)
	)

	if bytes >= tib {
		return fmt.Sprintf("%.2f TiB", float64(bytes)/float64(tib))
	}

	return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
}

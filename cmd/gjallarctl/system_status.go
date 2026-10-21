package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/localchanges"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// systemStatus shows what the running system was built from and, for
// developer mode, how the built local files, root's copy and the owner's
// folder relate. It reads only what every user may read.
func systemStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl system status")
		return 2
	}
	var built flakesource.BuildSource
	data, err := os.ReadFile(buildSourcePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(stdout, "Running system: built without a record of its source")
	case err != nil:
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	default:
		if err := json.Unmarshal(data, &built); err != nil {
			fmt.Fprintf(stderr, "ERROR: %s: %v\n", buildSourcePath, err)
			return 1
		}
		fmt.Fprintf(stdout, "Running system: %s (oddc %s)\n", shortRevision(built.Revision), shortRevision(built.ODDC))
	}
	for _, line := range timeLines(readClock(), readTimeBoot(), clockNow()) {
		fmt.Fprintln(stdout, line)
	}

	developerMode := "unknown"
	if bus, ok := openSettings(stderr); ok {
		ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
		key, err := settingsKey(ctx, bus, "developerMode")
		cancel()
		bus.Close()
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
		} else if string(key.Value) == "true" {
			developerMode = "on"
		} else {
			developerMode = "off"
		}
	}
	fmt.Fprintf(stdout, "Developer mode: %s\n", developerMode)

	if built.Local == nil {
		fmt.Fprintln(stdout, "Built with local files: none")
	} else {
		fmt.Fprintf(stdout, "Built with local files: %s\n", settings.ShortHash(built.Local.Hash))
		for _, file := range built.Local.Files {
			fmt.Fprintf(stdout, "  %s\n", settings.Shown(json.RawMessage(file), 0))
		}
	}
	builtHash := ""
	if built.Local != nil {
		builtHash = built.Local.Hash
	}

	root, err := readRootCopy(localChangesPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	switch {
	case len(root.Files) == 0:
		fmt.Fprintf(stdout, "System copy (%s): empty\n", localChangesPath)
	case root.Empty():
		fmt.Fprintf(stdout, "System copy (%s): no .nix file to build\n", localChangesPath)
	case root.Hash() == builtHash:
		fmt.Fprintf(stdout, "System copy (%s): built\n", localChangesPath)
	case developerMode == "off":
		fmt.Fprintf(stdout, "System copy (%s): %s, not built while developer mode is off\n", localChangesPath, settings.ShortHash(root.Hash()))
	default:
		fmt.Fprintf(stdout, "System copy (%s): %s, not built yet; the next rebuild builds it\n", localChangesPath, settings.ShortHash(root.Hash()))
	}

	dir, err := localFolder()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	mine, err := localchanges.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(stdout, "Your folder (%s): missing\n", dir)
	case err != nil:
		fmt.Fprintf(stdout, "Your folder (%s): cannot be applied: %v\n", dir, err)
	case mine.Hash() == root.Hash():
		fmt.Fprintf(stdout, "Your folder (%s): same as the system copy\n", dir)
	default:
		fmt.Fprintf(stdout, "Your folder (%s): %s, differs from the system copy; gjallarctl system apply shows how\n", dir, settings.ShortHash(mine.Hash()))
	}
	return 0
}

func shortRevision(rev string) string {
	if rev == "" {
		return "unknown"
	}
	return settings.ShortHash(rev)
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// The way back from developer mode in the trusted recovery entry; tests
// replace these.
var (
	developerOffRebuild     = []string{"/run/current-system/sw/bin/gjallar-system-rebuild"}
	developerOffOwnership   = systemcheckout.CheckOwnership
	nextBootBuildSourcePath = filepath.Join(systemProfilePath, "etc/gjallar/build-source.json")
)

// systemDeveloperOff turns developer mode off on the system checkout and
// builds the official system for the next boot (ROADMAP section 14). It
// runs as root in the trusted recovery entry only: there the settings
// service has no login session to take the change from, and the entry
// itself never holds local modules. The change goes through the same
// checks as one from the settings service.
func systemDeveloperOff(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl system developer-off")
		return 2
	}
	if settingsEUID() != 0 {
		fmt.Fprintln(stderr, "ERROR: gjallarctl system developer-off runs as root; in the running system use: gjallarctl settings set developerMode=false")
		return 2
	}
	if !inRecoveryEntry() {
		fmt.Fprintln(stderr, "ERROR: gjallarctl system developer-off runs in the trusted recovery boot entry only")
		return 1
	}
	if err := systemCheckoutMissing(); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v; nothing changed\n", err)
		return 1
	}
	if err := developerOffOwnership(systemCloneDest); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v; nothing changed\n", err)
		return 1
	}
	path := filepath.Join(systemCloneDest, "user.config.json")
	on, err := developerModeIn(path)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v; nothing changed\n", err)
		return 1
	}
	if on {
		if err := writeDeveloperOff(path); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Developer mode: off in %s\n", path)
	} else {
		// Off already, but the last rebuild may have failed and left the
		// local build as the next boot.
		fmt.Fprintln(stdout, "Developer mode is already off; rebuilding the official system.")
	}

	cmd := exec.Command(developerOffRebuild[0], developerOffRebuild[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "ERROR: the rebuild failed (%v); the next boot is unchanged. Developer mode stays off; run this again once the cause is fixed\n", err)
		return 1
	}
	built, err := nextBootSource()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if built.Local != nil {
		fmt.Fprintf(stderr, "ERROR: the next boot still holds local files %s\n", settings.ShortHash(built.Local.Hash))
		return 1
	}
	fmt.Fprintf(stdout, "PASS: the next boot is the official system %s without local files. Restart with: systemctl reboot\n", shortRevision(built.Revision))
	return 0
}

func developerModeIn(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	user, err := config.Parse(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return user.DeveloperMode, nil
}

// writeDeveloperOff writes developerMode=false the way the settings service
// does and reads it back.
func writeDeveloperOff(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	change, err := settings.Plan(data, map[string]json.RawMessage{"developerMode": json.RawMessage("false")})
	if err != nil {
		return err
	}
	updated, err := change.Apply(data)
	if err != nil {
		return err
	}
	if err := config.WriteText(path, updated); err != nil {
		return err
	}
	on, err := developerModeIn(path)
	if err != nil {
		return fmt.Errorf("read back %s: %w", path, err)
	}
	if on {
		return fmt.Errorf("%s still turns developer mode on after writing it", path)
	}
	return nil
}

func nextBootSource() (flakesource.BuildSource, error) {
	var built flakesource.BuildSource
	data, err := os.ReadFile(nextBootBuildSourcePath)
	if errors.Is(err, fs.ErrNotExist) {
		return built, fmt.Errorf("%s is missing; the next boot has no record of its source", nextBootBuildSourcePath)
	}
	if err != nil {
		return built, err
	}
	if err := json.Unmarshal(data, &built); err != nil {
		return built, fmt.Errorf("%s: %w", nextBootBuildSourcePath, err)
	}
	return built, nil
}

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

// Paths the first-start grant reads and writes; tests replace them.
var (
	grantEnrollmentMarker = "/var/lib/gjallarOS/tpm2-enrollment-complete"
	grantUserConfig       = filepath.Join(systemcheckout.Path, "user.config.json")
	grantPath             = devgate.GrantPath
)

// runLUKSGrantDeveloper records the developer confirmation for an install
// made with a dev or staging channel or with developer mode (Plane 297,
// Q192). The first-start chip enrollment calls it right after the person
// typed the disk passphrase or recovery key and set the startup PIN, so
// that proof also confirms the channel the installer recorded. It only
// works before that enrollment is complete: later the settings service
// asks for the proof itself (gjallarctl settings confirm-developer).
func runLUKSGrantDeveloper(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != devgate.ProofPIN && args[0] != devgate.ProofRecovery) {
		fmt.Fprintln(stderr, "Usage: gjallarctl luks grant-developer {pin|recovery}")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "ERROR: grant-developer belongs to the first-start chip enrollment and runs as root")
		return 1
	}
	if err := grantDeveloperAtFirstStart(args[0], time.Now(), stdout); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

func grantDeveloperAtFirstStart(proof string, now time.Time, stdout io.Writer) error {
	if _, err := os.Stat(grantEnrollmentMarker); err == nil {
		return errors.New("the chip enrollment is already complete; confirm with gjallarctl settings confirm-developer")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	cfg, err := config.Load(grantUserConfig)
	if err != nil {
		return err
	}
	want := devgate.Needs(cfg.Channel(), cfg.DeveloperMode)
	if want.Empty() {
		return nil
	}
	// The settings service owns this directory (StateDirectory, 0700); on
	// the first start it may not have run yet.
	if err := os.MkdirAll(filepath.Dir(grantPath), 0o700); err != nil {
		return fmt.Errorf("write the developer grant: %w", err)
	}
	g, err := devgate.Load(grantPath)
	if err != nil {
		return err
	}
	g.Channels = g.Channels || want.Channels
	g.DeveloperMode = g.DeveloperMode || want.DeveloperMode
	g.Proof = proof
	g.Time = now.UTC()
	if err := devgate.Save(grantPath, g); err != nil {
		return err
	}
	what := "the " + cfg.Channel() + " channel"
	switch {
	case want.Channels && want.DeveloperMode:
		what += " and developer mode"
	case want.DeveloperMode:
		what = "developer mode"
	}
	fmt.Fprintf(stdout, "GjallarOS: %s confirmed with this first start.\n", what)
	return nil
}

package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/gittest"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

type refuseRunner struct{}

func (refuseRunner) Output(_ context.Context, name string, _ ...string) ([]byte, error) {
	return nil, errors.New(name + " as root called in a test")
}

func TestMain(m *testing.M) {
	gittest.NoBackgroundMaintenance()
	// No test writes a log into the developer's runtime folder or restarts
	// the machine; tests that need either set their own.
	openInstallLog = func() (*installlog.Log, error) { return nil, nil }
	reboot = func(context.Context, bool) error { return errors.New("reboot called in a test") }
	writeRootFile = func(context.Context, []byte, string) error { return errors.New("root write called in a test") }
	bootNextRunner = refuseRunner{}
	listSaveDrives = func(context.Context, string) ([]targetdisk.SaveDrive, error) {
		return nil, errors.New("drive list called in a test")
	}
	os.Exit(m.Run())
}

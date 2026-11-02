package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/gittest"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

func TestMain(m *testing.M) {
	gittest.NoBackgroundMaintenance()
	// Tests never follow the machine's own system checkout pointer.
	dir, err := os.MkdirTemp("", "gjallarctl-test-")
	if err != nil {
		panic(err)
	}
	installercheck.SystemRepositoryFile = filepath.Join(dir, "repository")
	// Nor the machine's developer grant.
	developerGrantPath = filepath.Join(dir, "developer-grant.json")
	// The running system of a test has the proof helper.
	devproofSocketPath = filepath.Join(dir, "gjallar-devproof.socket")
	if err := os.WriteFile(devproofSocketPath, nil, 0o600); err != nil {
		panic(err)
	}
	status := m.Run()
	os.RemoveAll(dir)
	os.Exit(status)
}

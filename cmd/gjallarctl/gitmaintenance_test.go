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
	status := m.Run()
	os.RemoveAll(dir)
	os.Exit(status)
}

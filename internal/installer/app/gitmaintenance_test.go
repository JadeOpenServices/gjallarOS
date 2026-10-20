package app

import (
	"os"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/gittest"
)

func TestMain(m *testing.M) {
	gittest.NoBackgroundMaintenance()
	os.Exit(m.Run())
}

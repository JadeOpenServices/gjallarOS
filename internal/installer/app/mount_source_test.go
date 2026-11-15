package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	installerresume "github.com/JadeOpenServices/gjallarOS/internal/installer/resume"
)

func TestMountSourceDeviceStripsSubvolumeSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"/dev/mapper/cryptroot[/@]\n": "/dev/mapper/cryptroot",
		"/dev/mapper/cryptroot":       "/dev/mapper/cryptroot",
		"/dev/vda2[/@nix]":            "/dev/vda2",
		"":                            "",
	} {
		if got := mountSourceDevice(in); got != want {
			t.Errorf("mountSourceDevice(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every installer findmnt call that reads SOURCE must pass --nofsroot (-v):
// on Btrfs subvolume layouts SOURCE is otherwise "/dev/mapper/x[/@]", which
// broke in-place recovery provisioning ("inspect installed LUKS mapping @]").
func TestFindmntSourceCallsIgnoreSubvolumeSuffix(t *testing.T) {
	bare := regexp.MustCompile(`"findmnt",\s*"-n[^v"]*o",\s*"[^"]*SOURCE`)
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if loc := bare.FindIndex(data); loc != nil {
			t.Errorf("%s: findmnt SOURCE without --nofsroot: %s", path, data[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLiveMediaNeverRebuildsTheRunningSystem(t *testing.T) {
	if liveHostPreparationSkip(options{targetDisk: "/dev/disk/by-id/nvme-x"}, "", false) == "" {
		t.Fatal("fresh --target-disk install would rebuild the live system")
	}
	// The README command passes no --target-disk; the disk is picked later.
	if liveHostPreparationSkip(options{}, "", false) == "" {
		t.Fatal("fresh install without --target-disk would rebuild the live system")
	}
	if liveHostPreparationSkip(options{recovery: true}, "", false) == "" {
		t.Fatal("recovery media would rebuild the live system")
	}
	if skip := liveHostPreparationSkip(options{}, "", true); skip != "" {
		t.Fatalf("in-place deploy skipped host preparation: %q", skip)
	}
	if liveHostPreparationSkip(options{}, installerresume.StateMaintenanceReboot, true) == "" {
		t.Fatal("maintenance continuation would rebuild the deployed system from /etc/nixos")
	}
	// A staged release still needs the bootstrap before the flake deploys.
	if skip := liveHostPreparationSkip(options{}, installerresume.StateReleaseReboot, true); skip != "" {
		t.Fatalf("release continuation skipped host preparation: %q", skip)
	}
}

package app

import (
	"os"
	"strings"
	"testing"
)

func TestTargetDiskSelectionContract(t *testing.T) {
	source, err := os.ReadFile("target_disk_selection.go")
	if err != nil {
		t.Fatal(err)
	}

	body := string(source)

	for _, want := range []string{
		"targetdisk.Discover(",
		"case 0:",
		"case 1:",
		"Automatically selected the only eligible target disk:",
		"Multiple eligible installation target disks detected:",
		"ui.Choice(",
		"Select the whole disk to erase and install gjallarOS onto",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dynamic target selection contract missing %q", want)
		}
	}
}

func TestTargetDiskSelectionRetainsExplicitOverride(t *testing.T) {
	source, err := os.ReadFile("target_disk_selection.go")
	if err != nil {
		t.Fatal(err)
	}

	body := string(source)

	if !strings.Contains(
		body,
		"Using explicitly configured target disk:",
	) {
		t.Fatal("--target-disk explicit override was removed")
	}
}

// Regression: --target-disk on an installed host was silently ignored and the
// installer modified the running system in place instead.
func TestValidateTargetDiskContext(t *testing.T) {
	if err := validateTargetDiskContext("/dev/disk/by-id/x", true); err == nil ||
		!strings.Contains(err.Error(), "live or recovery environment") {
		t.Fatalf("installed host with --target-disk: err = %v", err)
	}
	for _, tc := range []struct {
		disk      string
		installed bool
	}{
		{"/dev/disk/by-id/x", false},
		{"", true},
		{"  ", true},
		{"", false},
	} {
		if err := validateTargetDiskContext(tc.disk, tc.installed); err != nil {
			t.Errorf("validateTargetDiskContext(%q, %v) = %v", tc.disk, tc.installed, err)
		}
	}
}

func TestRootPasswordArgsApplyAccountOnlyOnInstalledHost(t *testing.T) {
	installed := strings.Join(rootPasswordArgs(true), " ")
	if !strings.HasSuffix(installed, "--apply --apply-account") {
		t.Fatalf("installed host must apply the hash to root: %q", installed)
	}
	if fresh := strings.Join(rootPasswordArgs(false), " "); strings.Contains(fresh, "--apply-account") {
		t.Fatalf("live/fresh path must not change the running system's root: %q", fresh)
	}
}

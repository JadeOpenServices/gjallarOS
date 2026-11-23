package app

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/freshdiskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

func TestTargetDiskSelectionContract(t *testing.T) {
	source, err := os.ReadFile("target_disk_selection.go")
	if err != nil {
		t.Fatal(err)
	}

	body := string(source)

	for _, want := range []string{
		"var discoverTargetDisks = targetdisk.Discover",
		"discoverTargetDisks(",
		"case 1:",
		"ui.Confirm(",
		"It is the only disk found",
		"no disk chosen; nothing was changed",
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

// fakeDisks swaps disk discovery for a fixed list for one test.
func fakeDisks(t *testing.T, eligible []targetdisk.Result, rejected []targetdisk.Rejected) {
	t.Helper()
	saved := discoverTargetDisks
	t.Cleanup(func() { discoverTargetDisks = saved })
	discoverTargetDisks = func(_ context.Context, min uint64) ([]targetdisk.Result, []targetdisk.Rejected, error) {
		if min != freshdiskplan.MinimumDiskBytes {
			t.Fatalf("discovery minimum = %d, want %d", min, freshdiskplan.MinimumDiskBytes)
		}
		return eligible, rejected, nil
	}
}

func pickDisk(t *testing.T, answers string) (string, string, error) {
	t.Helper()
	var out bytes.Buffer
	ui := prompt.UI{Reader: bufio.NewReader(strings.NewReader(answers)), Out: &out}
	disk, err := selectFreshTargetDisk(context.Background(), ui, &out, "")
	return disk.Path, out.String(), err
}

var (
	windowsDisk = targetdisk.Result{
		Path: "/dev/nvme0n1", Model: "Example NVMe 1TB", Serial: "EXAMPLE0000A1B2",
		SizeBytes: 1000 << 30, Transport: "nvme",
		Partitions: []targetdisk.Partition{
			{Path: "/dev/nvme0n1p1", FSType: "vfat", Label: "SYSTEM", PartType: "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", SizeBytes: 100 << 20},
			{Path: "/dev/nvme0n1p2", PartLabel: "Microsoft reserved partition", PartType: "e3c9e316-0b5c-4db8-817d-f92df00215ae", SizeBytes: 16 << 20},
			{Path: "/dev/nvme0n1p3", FSType: "BitLocker", PartType: "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7", SizeBytes: 900 << 30},
		},
	}
	emptyUSB = targetdisk.Result{
		Path: "/dev/sdb", Model: "Example USB SSD", Serial: "EX12", SizeBytes: 500 << 30,
		Transport: "usb", Removable: true,
	}
)

// Q228: nothing is preselected; Enter alone asks again and never picks.
func TestTargetDiskChoiceHasNoDefault(t *testing.T) {
	fakeDisks(t, []targetdisk.Result{windowsDisk, emptyUSB}, nil)
	path, out, err := pickDisk(t, "\n\n2\n")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/dev/sdb" {
		t.Fatalf("picked %q, want /dev/sdb", path)
	}
	if strings.Count(out, "Choose one listed value.") != 2 {
		t.Fatalf("Enter must not pick a disk:\n%s", out)
	}
	if strings.Contains(out, "[") {
		t.Fatalf("no default may be shown:\n%s", out)
	}
}

// Q228: the one-disk question defaults to No.
func TestTargetDiskOnlyOneDefaultsToNo(t *testing.T) {
	fakeDisks(t, []targetdisk.Result{windowsDisk}, nil)
	_, out, err := pickDisk(t, "\n")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("Enter on the only disk must refuse, err = %v", err)
	}
	if !strings.Contains(out, "[y/N]") {
		t.Fatalf("one-disk question must default to No:\n%s", out)
	}
}

// Q228: bus, serial end, partitions with labels, and the Windows and
// BitLocker warnings show before the question; the full serial never does.
func TestTargetDiskListShowsWhatIsErased(t *testing.T) {
	fakeDisks(t, []targetdisk.Result{windowsDisk, emptyUSB}, nil)
	_, out, err := pickDisk(t, "1\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/dev/nvme0n1 — NVMe — 1000.0 GiB — Example NVMe 1TB — serial …A1B2",
		"/dev/sdb — USB — 500.0 GiB — Example USB SSD — serial …EX12",
		"! BitLocker: a Windows drive on this disk is encrypted.",
		"! Windows is installed on this disk.",
		"! All 3 partitions below are erased.",
		`/dev/nvme0n1p1  100 MiB  EFI system  vfat  label "SYSTEM"`,
		`/dev/nvme0n1p2  16 MiB  Microsoft reserved  name "Microsoft reserved partition"`,
		"no partitions",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("disk list missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "EXAMPLE0000") {
		t.Fatalf("full serial shown:\n%s", out)
	}
}

// Disk text is untrusted: a label with terminal escapes must not reach the
// screen as escapes.
func TestTargetDiskListStripsEscapes(t *testing.T) {
	disk := emptyUSB
	disk.Model = "Evil\x1b]0;owned\x07\x1b[2J"
	disk.Partitions = []targetdisk.Partition{{Path: "/dev/sdb1", FSType: "ext4", Label: "a\x1b[31mred\r", SizeBytes: 1 << 30}}
	fakeDisks(t, []targetdisk.Result{disk}, []targetdisk.Rejected{{Path: "/dev/sdc", Reason: "bad \x1b[2J name"}})
	_, out, _ := pickDisk(t, "n\n")
	if strings.ContainsAny(out, "\x1b\x07\r") {
		t.Fatalf("control characters reached the screen: %q", out)
	}
}

// Q227: too-small and unsafe disks are listed as not offered, with why.
func TestTargetDiskListShowsNotOffered(t *testing.T) {
	fakeDisks(t, nil, []targetdisk.Rejected{{Path: "/dev/sdc", Reason: "too small"}})
	_, out, err := pickDisk(t, "")
	if err == nil {
		t.Fatal("no eligible disk must fail")
	}
	if !strings.Contains(out, "Not offered:\n  /dev/sdc — too small") {
		t.Fatalf("rejected disk not listed:\n%s", out)
	}
}

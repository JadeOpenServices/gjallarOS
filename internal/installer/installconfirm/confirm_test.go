package installconfirm

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

const (
	testDiskGUID     = "11111111-2222-3333-4444-555555555555"
	testESPPARTUUID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testRootPARTUUID = "11111111-aaaa-bbbb-cccc-222222222222"
	testRecPARTUUID  = "99999999-8888-7777-6666-555555555555"

	efiType       = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	linuxLUKSType = "ca7d7ccb-63ed-4c53-861c-1742536059cc"
	xbootldrType  = "bc13c2ff-59e6-4262-a352-b275fd6f7172"
)

func testPlan(withRecovery bool) diskplan.Plan {
	plan := diskplan.Plan{
		SchemaVersion: diskplan.SchemaVersion,
		TargetDisk: diskplan.Disk{
			Path:        "/dev/nvme1n1",
			Model:       "Example NVMe",
			Serial:      "SERIAL-123",
			WWN:         "eui.0011223344556677",
			SizeBytes:   1000204886016,
			GPTDiskGUID: testDiskGUID,
		},
		ESP: diskplan.Partition{
			Role:      diskplan.RoleESP,
			Number:    1,
			Label:     "EFI",
			TypeGUID:  efiType,
			PARTUUID:  testESPPARTUUID,
			SizeBytes: 1024 * 1024 * 1024,
			Filesystem: diskplan.Filesystem{
				Type:  "vfat",
				Label: "EFI",
			},
			MountPoint: "/boot",
		},
		Root: diskplan.Root{
			Partition: diskplan.Partition{
				Role:      diskplan.RoleRoot,
				Number:    2,
				Label:     "GJALLAROS",
				TypeGUID:  linuxLUKSType,
				PARTUUID:  testRootPARTUUID,
				SizeBytes: 995000000000,
				Filesystem: diskplan.Filesystem{
					Type:  "btrfs",
					Label: "GJALLAROS",
				},
				MountPoint: "/",
			},
			Encryption: diskplan.Encryption{
				Type:        diskplan.EncryptionLUKS2,
				MappingName: "cryptroot",
			},
		},
	}

	if withRecovery {
		plan.Root.Partition.SizeBytes = 990000000000
		plan.Recovery = &diskplan.Partition{
			Role:      diskplan.RoleRecovery,
			Number:    3,
			Label:     "GJALLAR-RECOVERY",
			TypeGUID:  xbootldrType,
			PARTUUID:  testRecPARTUUID,
			SizeBytes: 12 * 1024 * 1024 * 1024,
			Filesystem: diskplan.Filesystem{
				Type:  "vfat",
				Label: "GJALLAR-REC",
			},
			MountPoint: "/recovery",
		}
	}

	return plan
}

func observedDisk() targetdisk.Result {
	return targetdisk.Result{
		Path:      "/dev/nvme1n1",
		Model:     "Example NVMe",
		Serial:    "SERIAL-123",
		WWN:       "eui.0011223344556677",
		SizeBytes: 1000204886016,
	}
}

func terminalUI(input string, out *bytes.Buffer) prompt.UI {
	return prompt.UI{
		Reader: bufio.NewReader(strings.NewReader(input)),
		Out:    out,
		GTK:    false,
	}
}

func TestConfirmRequiresTwoExplicitConfirmations(t *testing.T) {
	var out bytes.Buffer

	err := Confirm(
		context.Background(),
		terminalUI("y\ny\n", &out),
		&out,
		testPlan(true),
		observedDisk(),
		Options{},
	)
	if err != nil {
		t.Fatalf("exact confirmation rejected: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"Target:   /dev/nvme1n1",
		"Model:    Example NVMe",
		"Serial:   SERIAL-123",
		"Size:     1000204886016 bytes",
		"ESP:",
		"ROOT:",
		"RECOVERY:",
		"Root encryption: luks2",
		"Have you checked the target disk name thoroughly",
		"Are you absolutely sure you want to permanently erase /dev/nvme1n1?",
		"Destructive installation explicitly authorized.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("confirmation output missing %q:\n%s", want, text)
		}
	}
}

func TestConfirmRejectsFirstConfirmationNo(t *testing.T) {
	var out bytes.Buffer

	err := Confirm(
		context.Background(),
		terminalUI("n\n", &out),
		&out,
		testPlan(true),
		observedDisk(),
		Options{},
	)

	if err == nil {
		t.Fatal("first destructive confirmation accepted No")
	}
}

func TestConfirmRejectsSecondConfirmationNo(t *testing.T) {
	var out bytes.Buffer

	err := Confirm(
		context.Background(),
		terminalUI("y\nn\n", &out),
		&out,
		testPlan(true),
		observedDisk(),
		Options{},
	)

	if err == nil {
		t.Fatal("second destructive confirmation accepted No")
	}
}

func TestConfirmUnattendedRequiresExplicitOption(t *testing.T) {
	var out bytes.Buffer

	err := Confirm(
		context.Background(),
		terminalUI("", &out),
		&out,
		testPlan(false),
		observedDisk(),
		Options{Unattended: true},
	)
	if err != nil {
		t.Fatalf("explicit unattended mode rejected: %v", err)
	}

	if !strings.Contains(out.String(), "unattendedInstall=true") {
		t.Fatalf("unattended bypass was not explicitly logged:\n%s", out.String())
	}
}

func TestConfirmationDoesNotInferConsentFromRecovery(t *testing.T) {
	var out bytes.Buffer

	// Recovery is enabled in the plan, but unattended mode is not.
	// Default-No confirmation must still fail rather than treating
	// recovery configuration as consent.
	err := Confirm(
		context.Background(),
		terminalUI("\n", &out),
		&out,
		testPlan(true),
		observedDisk(),
		Options{},
	)
	if err == nil {
		t.Fatal("recovery-enabled plan implicitly bypassed destructive confirmation")
	}
}

func TestConfirmFailsIfPhysicalIdentityChanged(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*targetdisk.Result)
	}{
		{
			name: "path",
			mutate: func(result *targetdisk.Result) {
				result.Path = "/dev/nvme0n1"
			},
		},
		{
			name: "model",
			mutate: func(result *targetdisk.Result) {
				result.Model = "Different NVMe"
			},
		},
		{
			name: "wwn",
			mutate: func(result *targetdisk.Result) {
				result.WWN = "eui.changed"
			},
		},
		{
			name: "size",
			mutate: func(result *targetdisk.Result) {
				result.SizeBytes--
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := observedDisk()
			tt.mutate(&result)

			var out bytes.Buffer
			err := Confirm(
				context.Background(),
				terminalUI("y\ny\n", &out),
				&out,
				testPlan(false),
				result,
				Options{},
			)
			if err == nil {
				t.Fatal("changed physical identity was accepted")
			}
		})
	}
}

func TestConfirmUsesSerialFallbackWhenWWNUnavailable(t *testing.T) {
	plan := testPlan(false)
	plan.TargetDisk.WWN = ""

	result := observedDisk()
	result.WWN = ""

	var out bytes.Buffer
	err := Confirm(
		context.Background(),
		terminalUI("y\ny\n", &out),
		&out,
		plan,
		result,
		Options{},
	)
	if err != nil {
		t.Fatalf("serial fallback rejected: %v", err)
	}
}

func TestConfirmRejectsInvalidCanonicalPlan(t *testing.T) {
	plan := testPlan(false)
	plan.TargetDisk.GPTDiskGUID = ""

	var out bytes.Buffer
	err := Confirm(
		context.Background(),
		terminalUI("y\ny\n", &out),
		&out,
		plan,
		observedDisk(),
		Options{},
	)
	if err == nil {
		t.Fatal("invalid canonical plan was accepted")
	}
}

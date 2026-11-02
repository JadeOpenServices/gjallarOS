package gptprovision

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

const (
	diskGUID = "11111111-2222-3333-4444-555555555555"
	espUUID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	rootUUID = "11111111-aaaa-bbbb-cccc-222222222222"
	recUUID  = "99999999-8888-7777-6666-555555555555"

	efiType  = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	luksType = "ca7d7ccb-63ed-4c53-861c-1742536059cc"
	recType  = "bc13c2ff-59e6-4262-a352-b275fd6f7172"
)

type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
}

func key(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func (f *fakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil
}

func (f *fakeRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if out, ok := f.outputs[key(name, args...)]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected command: %v", append([]string{name}, args...))
}

func plan() diskplan.Plan {
	recovery := &diskplan.Partition{
		Role:      diskplan.RoleRecovery,
		Number:    3,
		Label:     "GJALLAR-RECOVERY",
		TypeGUID:  recType,
		PARTUUID:  recUUID,
		SizeBytes: 12 * 1024 * 1024 * 1024,
		Filesystem: diskplan.Filesystem{
			Type:  "vfat",
			Label: "GJALLAR-REC",
		},
		MountPoint: "/recovery",
	}

	return diskplan.Plan{
		SchemaVersion: diskplan.SchemaVersion,
		TargetDisk: diskplan.Disk{
			Path:        "/dev/nvme0n1",
			Model:       "Example NVMe",
			Serial:      "SERIAL",
			WWN:         "eui.example",
			SizeBytes:   1000204886016,
			GPTDiskGUID: diskGUID,
		},
		ESP: diskplan.Partition{
			Role:      diskplan.RoleESP,
			Number:    1,
			Label:     "EFI",
			TypeGUID:  efiType,
			PARTUUID:  espUUID,
			SizeBytes: 1024 * 1024 * 1024,
			Filesystem: diskplan.Filesystem{
				Type: "vfat",
			},
			MountPoint: "/boot",
		},
		Root: diskplan.Root{
			Partition: diskplan.Partition{
				Role:      diskplan.RoleRoot,
				Number:    2,
				Label:     "root",
				TypeGUID:  luksType,
				PARTUUID:  rootUUID,
				SizeBytes: 900 * 1024 * 1024 * 1024,
				Filesystem: diskplan.Filesystem{
					Type: "btrfs",
				},
				MountPoint: "/",
			},
			Encryption: diskplan.Encryption{
				Type:        diskplan.EncryptionLUKS2,
				MappingName: "cryptroot",
			},
		},
		Recovery: recovery,
	}
}

func lsblk(withRecovery bool) []byte {
	recovery := ""
	if withRecovery {
		recovery = `,
        {
          "path":"/dev/nvme0n1p3",
          "type":"part",
          "size":12884901888,
          "start":1887441000,
          "partn":3,
          "partlabel":"GJALLAR-RECOVERY",
          "parttype":"` + recType + `",
          "partuuid":"` + recUUID + `",
          "mountpoints":[null]
        }`
	}

	return []byte(`{
  "blockdevices":[{
    "path":"/dev/nvme0n1",
    "type":"disk",
    "size":1000204886016,
    "start":0,
    "model":"Example NVMe",
    "serial":"SERIAL",
    "wwn":"eui.example",
    "pttype":"gpt",
    "partn":null,
    "partlabel":null,
    "parttype":null,
    "partuuid":null,
    "mountpoints":[null],
    "children":[
      {
        "path":"/dev/nvme0n1p1",
        "type":"part",
        "size":1073741824,
        "start":4096,
        "partn":1,
        "partlabel":"EFI",
        "parttype":"` + efiType + `",
        "partuuid":"` + espUUID + `",
        "mountpoints":["/boot"]
      },
      {
        "path":"/dev/nvme0n1p2",
        "type":"part",
        "size":966367641600,
        "start":2101248,
        "partn":2,
        "partlabel":"root",
        "parttype":"` + luksType + `",
        "partuuid":"` + rootUUID + `",
        "mountpoints":[null]
      }` + recovery + `
    ]
  }]
}`)
}

func runnerForFreeSpace() *fakeRunner {
	disk := "/dev/nvme0n1"
	return &fakeRunner{outputs: map[string][]byte{
		key("lsblk", "-J", "-b", "-p", "--tree",
			"-o",
			"PATH,TYPE,SIZE,START,MODEL,SERIAL,WWN,PTTYPE,PARTN,PARTLABEL,PARTTYPE,PARTUUID,MOUNTPOINTS",
			"--", disk): lsblk(false),

		key("sudo", "sgdisk", "-p", disk): []byte("Disk identifier (GUID): " + diskGUID + "\n"),

		key("sudo", "blockdev", "--getss", disk): []byte("512\n"),
		key("sudo", "sgdisk", "-F", disk):        []byte("1887441000\n"),
		key("sudo", "sgdisk", "-E", disk):        []byte("1955000000\n"),
	}}
}

func ui(input string, out *bytes.Buffer) prompt.UI {
	return prompt.UI{
		Reader: bufio.NewReader(strings.NewReader(input)),
		Out:    out,
	}
}

func TestCreatesOnlyRecoveryGPTEntry(t *testing.T) {
	p := plan()
	r := runnerForFreeSpace()

	// Second lsblk inspection sees the new recovery partition.
	count := 0
	original := r.outputs[key(
		"lsblk", "-J", "-b", "-p", "--tree",
		"-o",
		"PATH,TYPE,SIZE,START,MODEL,SERIAL,WWN,PTTYPE,PARTN,PARTLABEL,PARTTYPE,PARTUUID,MOUNTPOINTS",
		"--", p.TargetDisk.Path,
	)]
	delete(r.outputs, key(
		"lsblk", "-J", "-b", "-p", "--tree",
		"-o",
		"PATH,TYPE,SIZE,START,MODEL,SERIAL,WWN,PTTYPE,PARTN,PARTLABEL,PARTTYPE,PARTUUID,MOUNTPOINTS",
		"--", p.TargetDisk.Path,
	))

	wrapped := &sequenceRunner{
		base:  r,
		lsblk: [][]byte{original, lsblk(true)},
	}

	var out bytes.Buffer
	result, err := provision(
		context.Background(),
		Input{
			Plan: p,
			UI:   ui("y\n", &out),
			Out:  &out,
		},
		wrapped,
	)
	_ = count
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != StatusCreated {
		t.Fatalf("unexpected status %q", result.Status)
	}

	updated := false
	for _, call := range wrapped.calls() {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "--rereadpt") {
			t.Fatalf("whole-disk reread fails while root is open: %v", call)
		}
		if strings.HasPrefix(joined, "sudo partx --update --nr ") {
			updated = true
		}
	}
	if !updated {
		t.Fatal("new recovery partition was not added to the kernel table")
	}

	for _, call := range wrapped.calls() {
		joined := strings.Join(call, " ")
		for _, forbidden := range []string{
			"--zap-all",
			"--clear",
			"mkfs",
			"cryptsetup",
			"resize2fs",
			"mount ",
		} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("forbidden mutation executed: %v", call)
			}
		}
	}
}

func TestInsufficientSpaceReturnsResizeRequired(t *testing.T) {
	p := plan()
	r := runnerForFreeSpace()
	r.outputs[key("sudo", "sgdisk", "-F", p.TargetDisk.Path)] =
		[]byte("1950000000\n")
	r.outputs[key("sudo", "sgdisk", "-E", p.TargetDisk.Path)] =
		[]byte("1950000100\n")

	var out bytes.Buffer
	result, err := provision(
		context.Background(),
		Input{Plan: p, UI: ui("", &out), Out: &out},
		r,
	)
	if err != nil {
		t.Fatalf("resize-required state returned an error: %v", err)
	}

	if result.Status != StatusResizeRequired {
		t.Fatalf("unexpected status: %q", result.Status)
	}

	if !strings.Contains(result.Message, "Btrfs recovery resize") {
		t.Fatalf("message is not actionable: %q", result.Message)
	}

	for _, call := range r.calls {
		if len(call) >= 2 && call[0] == "sudo" &&
			(call[1] == "sgdisk" &&
				strings.Contains(strings.Join(call, " "), "--new=")) {
			t.Fatalf("partition mutation ran without enough space: %v", call)
		}
	}
}

func TestExistingCanonicalRecoveryIsAccepted(t *testing.T) {
	p := plan()
	r := runnerForFreeSpace()
	r.outputs[key(
		"lsblk", "-J", "-b", "-p", "--tree",
		"-o",
		"PATH,TYPE,SIZE,START,MODEL,SERIAL,WWN,PTTYPE,PARTN,PARTLABEL,PARTTYPE,PARTUUID,MOUNTPOINTS",
		"--", p.TargetDisk.Path,
	)] = lsblk(true)

	var out bytes.Buffer
	result, err := provision(
		context.Background(),
		Input{Plan: p, UI: ui("", &out), Out: &out},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != StatusAlreadyPresent {
		t.Fatalf("unexpected status %q", result.Status)
	}
}

type sequenceRunner struct {
	base  *fakeRunner
	lsblk [][]byte
	n     int
}

func (s *sequenceRunner) calls() [][]string {
	return s.base.calls
}

func (s *sequenceRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) error {
	return s.base.Run(ctx, name, args...)
}

func (s *sequenceRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	if name == "lsblk" {
		s.base.calls = append(
			s.base.calls,
			append([]string{name}, args...),
		)
		if s.n >= len(s.lsblk) {
			return nil, fmt.Errorf("unexpected extra lsblk call")
		}
		out := s.lsblk[s.n]
		s.n++
		return out, nil
	}
	return s.base.Output(ctx, name, args...)
}

func TestPlannedFilesystemDoesNotReplaceRuntimeCapabilityDetection(t *testing.T) {
	p := plan()
	p.Root.Partition.Filesystem.Type = "ext4"

	r := &fakeRunner{
		outputs: map[string][]byte{},
	}
	var out bytes.Buffer

	_, err := provision(
		context.Background(),
		Input{
			Plan: p,
			UI:   ui("", &out),
			Out:  &out,
		},
		r,
	)
	if err == nil {
		t.Fatal("expected disk inspection to continue past planned filesystem metadata")
	}
	if !strings.Contains(err.Error(), "inspect GPT target") {
		t.Fatalf(
			"planned filesystem unexpectedly decided runtime capability: %v",
			err,
		)
	}

	if len(r.calls) == 0 {
		t.Fatal("gptprovision did not inspect the actual target disk")
	}

	first := strings.Join(r.calls[0], " ")
	if !strings.Contains(first, "lsblk") {
		t.Fatalf(
			"expected actual target inspection first, got: %v",
			r.calls[0],
		)
	}

	if strings.Contains(out.String(), "requires a Btrfs root filesystem") {
		t.Fatalf(
			"gptprovision emitted runtime filesystem decision from canonical metadata: %q",
			out.String(),
		)
	}
}

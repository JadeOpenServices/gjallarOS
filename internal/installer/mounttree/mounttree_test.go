package mounttree

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
)

const (
	espUUID      = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	rootUUID     = "11111111-aaaa-bbbb-cccc-222222222222"
	recoveryUUID = "99999999-8888-7777-6666-555555555555"
)

type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
	errors  map[string]error
}

func key(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func (f *fakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) error {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	return f.errors[key(name, args...)]
}

func (f *fakeRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)

	k := key(name, args...)
	if err := f.errors[k]; err != nil {
		return nil, err
	}
	if out, ok := f.outputs[k]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected output command: %v", call)
}

func plan(withRecovery bool) diskplan.Plan {
	p := diskplan.Plan{
		SchemaVersion: diskplan.SchemaVersion,
		TargetDisk: diskplan.Disk{
			Path:        "/dev/nvme1n1",
			Model:       "Example",
			Serial:      "SERIAL",
			SizeBytes:   1000 * 1024 * 1024 * 1024,
			GPTDiskGUID: "aaaaaaaa-1111-2222-3333-cccccccccccc",
		},
		ESP: diskplan.Partition{
			Role:      diskplan.RoleESP,
			Number:    1,
			Label:     "EFI",
			TypeGUID:  "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
			PARTUUID:  espUUID,
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
				TypeGUID:  "ca7d7ccb-63ed-4c53-861c-1742536059cc",
				PARTUUID:  rootUUID,
				SizeBytes: 900 * 1024 * 1024 * 1024,
				Filesystem: diskplan.Filesystem{
					Type:  "ext4",
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
		p.Recovery = &diskplan.Partition{
			Role:      diskplan.RoleRecovery,
			Number:    3,
			Label:     "GJALLAR-RECOVERY",
			TypeGUID:  "bc13c2ff-59e6-4262-a352-b275fd6f7172",
			PARTUUID:  recoveryUUID,
			SizeBytes: 12 * 1024 * 1024 * 1024,
			Filesystem: diskplan.Filesystem{
				Type:  "vfat",
				Label: "GJALLAR-REC",
			},
			MountPoint: "/recovery",
		}
	}

	return p
}

func partitionJSON(path, uuid string, size uint64) []byte {
	return []byte(fmt.Sprintf(`{
  "blockdevices":[{
    "path":"%s",
    "type":"part",
    "size":%d,
    "fstype":null,
    "partuuid":"%s",
    "mountpoints":[null]
  }]
}`, path, size, uuid))
}

func runner(withRecovery bool) *fakeRunner {
	esp := "/dev/nvme1n1p1"
	recovery := "/dev/nvme1n1p3"

	r := &fakeRunner{
		outputs: map[string][]byte{
			key(
				"findmnt",
				"-nvro",
				"SOURCE,FSTYPE",
				"--mountpoint",
				"/mnt",
			): []byte("/dev/mapper/cryptroot ext4\n"),

			key(
				"readlink",
				"-f",
				"/dev/disk/by-partuuid/"+espUUID,
			): []byte(esp + "\n"),

			key(
				"lsblk",
				"-J",
				"-b",
				"-p",
				"--tree",
				"-o",
				"PATH,TYPE,SIZE,FSTYPE,PARTUUID,MOUNTPOINTS",
				"--",
				esp,
			): partitionJSON(
				esp,
				espUUID,
				1024*1024*1024,
			),

			key(
				"findmnt",
				"-nvro",
				"SOURCE,FSTYPE",
				"--mountpoint",
				"/mnt/boot",
			): []byte(esp + " vfat\n"),
		},
		errors: map[string]error{},
	}

	if withRecovery {
		r.outputs[key(
			"readlink",
			"-f",
			"/dev/disk/by-partuuid/"+recoveryUUID,
		)] = []byte(recovery + "\n")

		r.outputs[key(
			"lsblk",
			"-J",
			"-b",
			"-p",
			"--tree",
			"-o",
			"PATH,TYPE,SIZE,FSTYPE,PARTUUID,MOUNTPOINTS",
			"--",
			recovery,
		)] = partitionJSON(
			recovery,
			recoveryUUID,
			12*1024*1024*1024,
		)

		r.outputs[key(
			"findmnt",
			"-nvro",
			"SOURCE,FSTYPE",
			"--mountpoint",
			"/mnt/recovery",
		)] = []byte(recovery + " vfat\n")
	}

	return r
}

func TestPrepareESPAndRecoveryMountTree(t *testing.T) {
	p := plan(true)
	r := runner(true)
	var out bytes.Buffer

	result, err := prepare(
		context.Background(),
		Input{Plan: p, Out: &out},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.RootMount != "/mnt" {
		t.Fatalf("root mount = %q", result.RootMount)
	}
	if result.ESPMount != "/mnt/boot" {
		t.Fatalf("ESP mount = %q", result.ESPMount)
	}
	if result.RecoveryMount != "/mnt/recovery" {
		t.Fatalf("recovery mount = %q", result.RecoveryMount)
	}

	joined := calls(r.calls)

	for _, required := range []string{
		"sudo mkfs.vfat -F 32 -n EFI /dev/nvme1n1p1",
		"sudo mkdir -p /mnt/boot",
		"sudo mount /dev/nvme1n1p1 /mnt/boot",
		"sudo mkfs.vfat -F 32 -n GJALLAR-REC /dev/nvme1n1p3",
		"sudo mkdir -p /mnt/recovery",
		"sudo mount /dev/nvme1n1p3 /mnt/recovery",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing command %q:\n%s", required, joined)
		}
	}
}

func TestPrepareWithoutRecovery(t *testing.T) {
	p := plan(false)
	r := runner(false)
	var out bytes.Buffer

	result, err := prepare(
		context.Background(),
		Input{Plan: p, Out: &out},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.RecoveryMount != "" {
		t.Fatalf("unexpected recovery mount %q", result.RecoveryMount)
	}
	if strings.Contains(calls(r.calls), "/mnt/recovery") {
		t.Fatalf("recovery touched when disabled:\n%s", calls(r.calls))
	}
}

func TestRequiresExistingEncryptedRootMount(t *testing.T) {
	p := plan(false)
	r := runner(false)

	r.outputs[key(
		"findmnt",
		"-nvro",
		"SOURCE,FSTYPE",
		"--mountpoint",
		"/mnt",
	)] = []byte("/dev/mapper/not-cryptroot ext4\n")

	var out bytes.Buffer
	_, err := prepare(
		context.Background(),
		Input{Plan: p, Out: &out},
		r,
	)

	if err == nil || !strings.Contains(err.Error(), "source mismatch") {
		t.Fatalf("wrong root mount accepted: %v", err)
	}

	if strings.Contains(calls(r.calls), "mkfs.vfat") {
		t.Fatalf("format ran before root verification:\n%s", calls(r.calls))
	}
}

func TestExistingESPSignatureRejectedBeforeFormat(t *testing.T) {
	p := plan(false)
	r := runner(false)

	k := key(
		"lsblk",
		"-J",
		"-b",
		"-p",
		"--tree",
		"-o",
		"PATH,TYPE,SIZE,FSTYPE,PARTUUID,MOUNTPOINTS",
		"--",
		"/dev/nvme1n1p1",
	)

	r.outputs[k] = bytes.Replace(
		r.outputs[k],
		[]byte(`"fstype":null`),
		[]byte(`"fstype":"vfat"`),
		1,
	)

	var out bytes.Buffer
	_, err := prepare(
		context.Background(),
		Input{Plan: p, Out: &out},
		r,
	)

	if err == nil || !strings.Contains(err.Error(), "already contains") {
		t.Fatalf("existing ESP signature accepted: %v", err)
	}
	if strings.Contains(calls(r.calls), "mkfs.vfat") {
		t.Fatalf("format unexpectedly ran:\n%s", calls(r.calls))
	}
}

func TestMountVerificationFailureStopsStage(t *testing.T) {
	p := plan(false)
	r := runner(false)

	r.outputs[key(
		"findmnt",
		"-nvro",
		"SOURCE,FSTYPE",
		"--mountpoint",
		"/mnt/boot",
	)] = []byte("/dev/nvme9n9p9 vfat\n")

	var out bytes.Buffer
	_, err := prepare(
		context.Background(),
		Input{Plan: p, Out: &out},
		r,
	)

	if err == nil || !strings.Contains(err.Error(), "source mismatch") {
		t.Fatalf("bad mount verification accepted: %v", err)
	}
	if !strings.Contains(calls(r.calls), "sudo umount /mnt/boot") {
		t.Fatalf("failed ESP mount was not cleaned up:\n%s", calls(r.calls))
	}
}

func TestMountLayoutDoesNotTouchJODSOrRootCrypto(t *testing.T) {
	p := plan(true)
	r := runner(true)
	var out bytes.Buffer

	_, err := prepare(
		context.Background(),
		Input{Plan: p, Out: &out},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	joined := calls(r.calls)

	for _, forbidden := range []string{
		"cryptsetup",
		"sgdisk",
		"systemd-cryptenroll",
		"nixos-install",
		"gjallar-agent",
		"jods-endpoint",
		"jods enrollment",
		"jods heartbeat",
	} {
		if strings.Contains(strings.ToLower(joined), forbidden) {
			t.Fatalf("out-of-scope operation %q:\n%s", forbidden, joined)
		}
	}

	if !strings.Contains(joined, "mkfs.vfat -F 32 -n GJALLAR-REC") {
		t.Fatalf("expected recovery compatibility filesystem label:\n%s", joined)
	}
}

func calls(calls [][]string) string {
	var lines []string
	for _, call := range calls {
		lines = append(lines, strings.Join(call, " "))
	}
	return strings.Join(lines, "\n")
}

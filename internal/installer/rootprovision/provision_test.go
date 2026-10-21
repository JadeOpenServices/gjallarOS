package rootprovision

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
)

const (
	testPartUUID = "11111111-aaaa-bbbb-cccc-222222222222"
	testLUKSUUID = "abcd1234-1111-2222-3333-abcdefabcdef"
)

type fakeRunner struct {
	calls      [][]string
	inputSizes []int
	outputs    map[string][]byte
	errors     map[string]error
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func (f *fakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) error {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	if err := f.errors[commandKey(name, args...)]; err != nil {
		return err
	}
	return nil
}

func (f *fakeRunner) RunInput(
	_ context.Context,
	input []byte,
	name string,
	args ...string,
) error {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	f.inputSizes = append(f.inputSizes, len(input))
	if err := f.errors[commandKey(name, args...)]; err != nil {
		return err
	}
	return nil
}

func (f *fakeRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	k := commandKey(name, args...)
	if err := f.errors[k]; err != nil {
		return nil, err
	}
	if out, ok := f.outputs[k]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected output command: %v", call)
}

func testPlan(fs string) diskplan.Plan {
	return diskplan.Plan{
		SchemaVersion: diskplan.SchemaVersion,
		TargetDisk: diskplan.Disk{
			Path:        "/dev/nvme1n1",
			Model:       "Example",
			Serial:      "SERIAL",
			SizeBytes:   1000 * 1024 * 1024 * 1024,
			GPTDiskGUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		},
		ESP: diskplan.Partition{
			Role:       diskplan.RoleESP,
			Number:     1,
			Label:      "EFI",
			TypeGUID:   "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
			PARTUUID:   "aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb",
			SizeBytes:  1024 * 1024 * 1024,
			Filesystem: diskplan.Filesystem{Type: "vfat"},
			MountPoint: "/boot",
		},
		Root: diskplan.Root{
			Partition: diskplan.Partition{
				Role:      diskplan.RoleRoot,
				Number:    2,
				Label:     "GJALLAROS",
				TypeGUID:  "ca7d7ccb-63ed-4c53-861c-1742536059cc",
				PARTUUID:  testPartUUID,
				SizeBytes: 900 * 1024 * 1024 * 1024,
				Filesystem: diskplan.Filesystem{
					Type:  fs,
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
}

func runner(fs string) *fakeRunner {
	partition := "/dev/nvme1n1p2"
	stable := "/dev/disk/by-partuuid/" + testPartUUID

	r := &fakeRunner{
		outputs: map[string][]byte{
			commandKey("readlink", "-f", stable): []byte(partition + "\n"),
			commandKey(
				"lsblk", "-J", "-b", "-p", "--tree", "-o",
				"PATH,TYPE,SIZE,FSTYPE,PARTUUID,MOUNTPOINTS",
				"--", partition,
			): []byte(fmt.Sprintf(`{
  "blockdevices":[{
    "path":"%s",
    "type":"part",
    "size":%d,
    "fstype":null,
    "partuuid":"%s",
    "mountpoints":[null]
  }]
}`,
				partition,
				uint64(900*1024*1024*1024),
				testPartUUID,
			)),
			commandKey(
				"sudo", "cryptsetup", "luksUUID", partition,
			): []byte(testLUKSUUID + "\n"),
		},
		errors: map[string]error{
			commandKey(
				"findmnt", "-n", "--mountpoint", "/mnt",
			): exitCodeError{1},
		},
	}
	for _, sub := range Subvolumes {
		r.outputs[commandKey(
			"findmnt", "-nvro", "SOURCE,FSROOT",
			"--mountpoint", filepath.Join("/mnt", sub.Path),
		)] = []byte("/dev/mapper/cryptroot /" + sub.Name + "\n")
	}
	return r
}

type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return "exit status" }
func (e exitCodeError) ExitCode() int { return e.code }
func (e exitCodeError) Unwrap() error { return nil }

func TestProvisionLUKS2BtrfsRoot(t *testing.T) {
	p := testPlan("btrfs")
	r := runner("btrfs")
	var out bytes.Buffer

	result, err := provision(
		context.Background(),
		Input{
			Plan:       p,
			Passphrase: []byte("correct horse battery staple"),
			Out:        &out,
		},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.MountPoint != "/mnt" {
		t.Fatalf("unexpected mount: %q", result.MountPoint)
	}
	if result.StableDevice != "/dev/disk/by-uuid/"+testLUKSUUID {
		t.Fatalf("unexpected stable LUKS device: %q", result.StableDevice)
	}

	joined := callsText(r.calls)

	for _, required := range []string{
		"cryptsetup luksFormat --type luks2 --batch-mode --key-file - /dev/nvme1n1p2",
		"cryptsetup open --type luks2 --key-file - /dev/nvme1n1p2 cryptroot",
		"mkfs.btrfs -f -L GJALLAROS /dev/mapper/cryptroot",
		"mount -o subvolid=5 /dev/mapper/cryptroot /mnt",
		"btrfs subvolume create /mnt/@home",
		"btrfs subvolume create /mnt/@config",
		"umount /mnt",
		"mount -o subvol=@ /dev/mapper/cryptroot /mnt",
		"mount -o subvol=@home /dev/mapper/cryptroot /mnt/home",
		"mount -o subvol=@nix /dev/mapper/cryptroot /mnt/nix",
		"mount -o subvol=@config /dev/mapper/cryptroot /mnt/etc/gjallar/system",
		"mount -o subvol=@snapshots,nosuid,nodev,noexec /dev/mapper/cryptroot /mnt/.snapshots",
		"btrfs subvolume set-default /mnt",
		"chmod 0700 /mnt/.snapshots",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing command %q:\n%s", required, joined)
		}
	}

	if len(r.inputSizes) != 2 {
		t.Fatalf("expected secret on stdin twice, got %v", r.inputSizes)
	}
}

func TestSecretNeverAppearsInArgumentsOrOutput(t *testing.T) {
	secret := "VERY-SECRET-LUKS-PASSPHRASE"
	p := testPlan("btrfs")
	r := runner("btrfs")
	var out bytes.Buffer

	_, err := provision(
		context.Background(),
		Input{
			Plan:       p,
			Passphrase: []byte(secret),
			Out:        &out,
		},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(callsText(r.calls), secret) {
		t.Fatal("secret appeared in command arguments")
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("secret appeared in provisioning output")
	}
}

func TestExistingSignatureIsRejectedBeforeMutation(t *testing.T) {
	p := testPlan("btrfs")
	r := runner("btrfs")

	k := commandKey(
		"lsblk", "-J", "-b", "-p", "--tree", "-o",
		"PATH,TYPE,SIZE,FSTYPE,PARTUUID,MOUNTPOINTS",
		"--", "/dev/nvme1n1p2",
	)
	r.outputs[k] = bytes.Replace(
		r.outputs[k],
		[]byte(`"fstype":null`),
		[]byte(`"fstype":"crypto_LUKS"`),
		1,
	)

	var out bytes.Buffer
	_, err := provision(
		context.Background(),
		Input{
			Plan:       p,
			Passphrase: []byte("secret"),
			Out:        &out,
		},
		r,
	)

	if err == nil || !strings.Contains(err.Error(), "already contains") {
		t.Fatalf("existing signature accepted: %v", err)
	}

	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), "luksFormat") {
			t.Fatalf("destructive mutation ran: %v", call)
		}
	}
}

func TestTPMEnrollmentIsOutsideThisStage(t *testing.T) {
	p := testPlan("btrfs")
	r := runner("btrfs")
	var out bytes.Buffer

	_, err := provision(
		context.Background(),
		Input{
			Plan:       p,
			Passphrase: []byte("secret"),
			Out:        &out,
		},
		r,
	)
	if err != nil {
		t.Fatal(err)
	}

	joined := callsText(r.calls)
	for _, forbidden := range []string{
		"systemd-cryptenroll",
		"--tpm2",
		"luksAddKey",
		"luksKillSlot",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("TPM/key migration leaked into root provisioning: %s", joined)
		}
	}
}

func callsText(calls [][]string) string {
	var lines []string
	for _, call := range calls {
		lines = append(lines, strings.Join(call, " "))
	}
	return strings.Join(lines, "\n")
}

func TestExt4RootRejectedBeforeMutation(t *testing.T) {
	p := testPlan("ext4")
	r := runner("ext4")
	var out bytes.Buffer

	_, err := provision(
		context.Background(),
		Input{
			Plan:       p,
			Passphrase: []byte("test-passphrase"),
			Out:        &out,
		},
		r,
	)
	if err == nil {
		t.Fatal("ext4 fresh root was accepted")
	}
	if !strings.Contains(err.Error(), "requires Btrfs") {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := callsText(r.calls)
	for _, forbidden := range []string{
		"luksFormat",
		"cryptsetup open",
		"mkfs.ext4",
		"mkfs.btrfs",
		"mount ",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf(
				"storage mutation %q ran for unsupported ext4 root:\n%s",
				forbidden,
				joined,
			)
		}
	}
}

func TestWrongSubvolumeStopsAndUnmounts(t *testing.T) {
	r := runner("btrfs")
	r.outputs[commandKey(
		"findmnt", "-nvro", "SOURCE,FSROOT", "--mountpoint", "/mnt/home",
	)] = []byte("/dev/mapper/cryptroot /@\n")
	var out bytes.Buffer

	_, err := provision(
		context.Background(),
		Input{Plan: testPlan("btrfs"), Passphrase: []byte("correct horse battery staple"), Out: &out},
		r,
	)
	if err == nil || !strings.Contains(err.Error(), "/mnt/home mismatch") {
		t.Fatalf("expected subvolume mismatch, got %v", err)
	}
	joined := callsText(r.calls)
	if strings.Contains(joined, "/mnt/nix") {
		t.Fatalf("mounted on after the mismatch:\n%s", joined)
	}
	if !strings.HasSuffix(joined, "sudo umount -R /mnt\nsudo cryptsetup close cryptroot") {
		t.Fatalf("expected cleanup, got:\n%s", joined)
	}
}

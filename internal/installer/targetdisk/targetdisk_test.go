package targetdisk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

type fakeRunner struct {
	output []byte
	err    error
	calls  [][]string
}

func (f *fakeRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.output, f.err
}

func validLSBLK() string {
	return `{
  "blockdevices": [
    {
      "path": "/dev/nvme1n1",
      "type": "disk",
      "model": "Example NVMe",
      "serial": "SERIAL-123",
      "wwn": "eui.0011223344556677",
      "size": 1000204886016,
      "label": null,
      "mountpoints": [null],
      "children": [
        {
          "path": "/dev/nvme1n1p1",
          "type": "part",
          "model": null,
          "serial": null,
          "wwn": null,
          "size": 1073741824,
          "label": null,
          "mountpoints": [null]
        }
      ]
    }
  ]
}`
}

func runValidation(t *testing.T, data string, input Input) (Result, error, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{output: []byte(data)}
	result, err := validate(context.Background(), input, runner)
	return result, err, runner
}

func TestValidateAcceptsSuitableWholeDisk(t *testing.T) {
	result, err, runner := runValidation(
		t,
		validLSBLK(),
		Input{
			Path:         "/dev/nvme1n1",
			MinSizeBytes: 128 * 1024 * 1024 * 1024,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.Path != "/dev/nvme1n1" ||
		result.Model != "Example NVMe" ||
		result.Serial != "SERIAL-123" ||
		result.WWN != "eui.0011223344556677" ||
		result.SizeBytes != 1000204886016 {
		t.Fatalf("unexpected result: %+v", result)
	}

	if len(runner.calls) != 1 || runner.calls[0][0] != "lsblk" {
		t.Fatalf("unexpected command calls: %v", runner.calls)
	}

	for _, forbidden := range []string{
		"sgdisk",
		"parted",
		"fdisk",
		"mkfs",
		"wipefs",
		"cryptsetup",
		"mount",
	} {
		if strings.Contains(fmt.Sprint(runner.calls), forbidden) {
			t.Fatalf("validator invoked destructive command %q: %v", forbidden, runner.calls)
		}
	}
}

func TestValidateRejectsMissingDevice(t *testing.T) {
	runner := &fakeRunner{err: errors.New("lsblk: not a block device")}

	_, err := validate(
		context.Background(),
		Input{Path: "/dev/does-not-exist"},
		runner,
	)
	if err == nil || !strings.Contains(err.Error(), "inspect target disk") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsPartition(t *testing.T) {
	data := `{
	  "blockdevices": [{
	    "path": "/dev/nvme0n1p2",
	    "type": "part",
	    "size": 1000000000,
	    "mountpoints": [null]
	  }]
	}`

	_, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/nvme0n1p2"},
	)
	if err == nil || !strings.Contains(err.Error(), "not a whole block device") {
		t.Fatalf("partition unexpectedly accepted: %v", err)
	}
}

func TestValidateRejectsCurrentRootDisk(t *testing.T) {
	data := `{
	  "blockdevices": [{
	    "path": "/dev/nvme0n1",
	    "type": "disk",
	    "model": "System NVMe",
	    "serial": "ROOT-SERIAL",
	    "size": 1000204886016,
	    "mountpoints": [null],
	    "children": [{
	      "path": "/dev/nvme0n1p2",
	      "type": "part",
	      "size": 999000000000,
	      "mountpoints": [null],
	      "children": [{
	        "path": "/dev/mapper/cryptroot",
	        "type": "crypt",
	        "size": 999000000000,
	        "mountpoints": ["/"]
	      }]
	    }]
	  }]
	}`

	_, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/nvme0n1"},
	)
	if err == nil || !strings.Contains(err.Error(), "currently mounted root filesystem") {
		t.Fatalf("root disk unexpectedly accepted: %v", err)
	}
}

func TestValidateRejectsInstallationMediaByLabel(t *testing.T) {
	// Unmounted, as after copytoram: only the label marks the medium.
	for _, label := range []string{"GJALLAR_STICK", "gjallar-recovery-26.05-x86_64"} {
		data := `{
		  "blockdevices": [{
		    "path": "/dev/sdb",
		    "type": "disk",
		    "model": "USB Stick",
		    "serial": "INSTALLER-USB",
		    "size": 32000000000,
		    "label": "` + label + `",
		    "mountpoints": [null],
		    "children": [{
		      "path": "/dev/sdb2",
		      "type": "part",
		      "size": 3000000,
		      "label": "EFIBOOT",
		      "mountpoints": [null]
		    }]
		  }]
		}`

		_, err, _ := runValidation(
			t,
			data,
			Input{Path: "/dev/sdb"},
		)
		if err == nil || !strings.Contains(err.Error(), "installation media") {
			t.Fatalf("installation medium %s unexpectedly accepted: %v", label, err)
		}
	}
}

func TestValidateAcceptsAStockNixOSStick(t *testing.T) {
	// A stock NixOS stick is not ours; it may be the install target.
	data := `{
	  "blockdevices": [{
	    "path": "/dev/sdb",
	    "type": "disk",
	    "model": "USB Stick",
	    "serial": "OTHER-USB",
	    "size": 32000000000,
	    "label": "nixos-minimal-26.05-x86_64",
	    "mountpoints": [null]
	  }]
	}`

	_, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/sdb"},
	)
	if err != nil && strings.Contains(err.Error(), "installation media") {
		t.Fatalf("stock NixOS stick taken for our boot medium: %v", err)
	}
}

func TestValidateRejectsInstallationMediaByMountpoint(t *testing.T) {
	data := `{
	  "blockdevices": [{
	    "path": "/dev/sdb",
	    "type": "disk",
	    "model": "USB Stick",
	    "serial": "INSTALLER-USB",
	    "size": 32000000000,
	    "mountpoints": [null],
	    "children": [{
	      "path": "/dev/sdb1",
	      "type": "part",
	      "size": 3000000000,
	      "mountpoints": ["/run/initramfs/iso"]
	    }]
	  }]
	}`

	_, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/sdb"},
	)
	if err == nil || !strings.Contains(err.Error(), "installation media") {
		t.Fatalf("installation medium unexpectedly accepted: %v", err)
	}
}

func TestValidateRejectsUndersizedDisk(t *testing.T) {
	_, err, _ := runValidation(
		t,
		validLSBLK(),
		Input{
			Path:         "/dev/nvme1n1",
			MinSizeBytes: 2 * 1024 * 1024 * 1024 * 1024,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "too small") {
		t.Fatalf("undersized disk unexpectedly accepted: %v", err)
	}
}

func TestValidateRequiresStableIdentity(t *testing.T) {
	data := strings.Replace(
		validLSBLK(),
		`"serial": "SERIAL-123",
      "wwn": "eui.0011223344556677"`,
		`"serial": null,
      "wwn": null`,
		1,
	)

	_, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/nvme1n1"},
	)
	if err == nil || !strings.Contains(err.Error(), "no stable serial or WWN identity") {
		t.Fatalf("disk without stable identity unexpectedly accepted: %v", err)
	}
}

func TestValidateAllowsMissingModelWithStableIdentity(t *testing.T) {
	data := strings.Replace(
		validLSBLK(),
		`"model": "Example NVMe"`,
		`"model": null`,
		1,
	)

	result, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/nvme1n1"},
	)
	if err != nil {
		t.Fatalf(
			"disk with stable serial/WWN and missing model was rejected: %v",
			err,
		)
	}

	if result.Path != "/dev/nvme1n1" {
		t.Fatalf("unexpected target path: %q", result.Path)
	}

	if result.Model != "" {
		t.Fatalf("expected empty model, got %q", result.Model)
	}

	if result.Serial != "SERIAL-123" {
		t.Fatalf("stable serial was not preserved: %q", result.Serial)
	}

	if result.WWN != "eui.0011223344556677" {
		t.Fatalf("stable WWN was not preserved: %q", result.WWN)
	}
}

func TestValidateRejectsZeroSize(t *testing.T) {
	data := strings.Replace(
		validLSBLK(),
		`"size": 1000204886016`,
		`"size": 0`,
		1,
	)

	_, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/nvme1n1"},
	)
	if err == nil || !strings.Contains(err.Error(), "reports zero size") {
		t.Fatalf("zero-sized disk unexpectedly accepted: %v", err)
	}
}

func TestValidateRejectsInvalidPathBeforeRunningCommands(t *testing.T) {
	runner := &fakeRunner{}

	for _, path := range []string{
		"",
		"nvme0n1",
		"/tmp/nvme0n1",
		"/",
	} {
		t.Run(path, func(t *testing.T) {
			runner.calls = nil
			_, err := validate(
				context.Background(),
				Input{Path: path},
				runner,
			)
			if err == nil {
				t.Fatalf("invalid path %q accepted", path)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("invalid path caused command execution: %v", runner.calls)
			}
		})
	}
}

func TestValidateRejectsAmbiguousLSBLKResult(t *testing.T) {
	data := `{
	  "blockdevices": [
	    {"path":"/dev/sda","type":"disk"},
	    {"path":"/dev/sdb","type":"disk"}
	  ]
	}`

	_, err, _ := runValidation(
		t,
		data,
		Input{Path: "/dev/sda"},
	)
	if err == nil || !strings.Contains(err.Error(), "expected exactly one") {
		t.Fatalf("ambiguous lsblk result unexpectedly accepted: %v", err)
	}
}

// A partitioned disk must decode as one device with nested children; lsblk
// 2.42 emits a flat list without --tree and every used disk was rejected.
func TestLSBLKArgsNestPartitionsWithRealLsblk(t *testing.T) {
	if _, err := exec.LookPath("lsblk"); err != nil {
		t.Skip("lsblk unavailable")
	}
	disks, err := exec.Command("lsblk", "-dnpo", "PATH,TYPE").Output()
	if err != nil {
		t.Skip("lsblk cannot list disks here")
	}
	for _, line := range strings.Split(string(disks), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != "disk" {
			continue
		}
		raw, err := exec.Command("lsblk", lsblkArgs(fields[0])...).Output()
		if err != nil {
			continue
		}
		var tree lsblkTree
		if err := json.Unmarshal(raw, &tree); err != nil {
			t.Fatal(err)
		}
		if len(tree.BlockDevices[0].Children) == 0 {
			continue
		}
		if len(tree.BlockDevices) != 1 {
			t.Fatalf("%s decoded as %d top-level devices; partitions not nested", fields[0], len(tree.BlockDevices))
		}
		return
	}
	t.Skip("no partitioned disk available")
}

func TestValidateRejectsDiskInUse(t *testing.T) {
	for name, child := range map[string]string{
		"mounted partition": `{"path":"/dev/nvme1n1p1","type":"part","mountpoints":["/mnt/boot"]}`,
		"open LUKS":         `{"path":"/dev/nvme1n1p2","type":"part","mountpoints":[null],"children":[{"path":"/dev/mapper/cryptroot","type":"crypt","mountpoints":[null]}]}`,
		"swap":              `{"path":"/dev/nvme1n1p3","type":"part","mountpoints":["[SWAP]"]}`,
	} {
		data := `{"blockdevices":[{"path":"/dev/nvme1n1","type":"disk","serial":"S","size":1000,"mountpoints":[null],"children":[` + child + `]}]}`
		_, err, _ := runValidation(t, data, Input{Path: "/dev/nvme1n1"})
		if err == nil || !strings.Contains(err.Error(), "is in use") {
			t.Fatalf("%s: accepted busy disk: %v", name, err)
		}
	}
	if _, err, _ := runValidation(t, validLSBLK(), Input{Path: "/dev/nvme1n1"}); err != nil {
		t.Fatalf("idle partitioned disk rejected: %v", err)
	}
}

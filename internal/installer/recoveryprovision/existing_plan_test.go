package recoveryprovision

import (
	"context"
	"errors"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryresize"
)

type existingPlanRunner struct {
	raw []byte
}

func (r existingPlanRunner) Output(
	context.Context,
	string,
	...string,
) ([]byte, error) {
	return r.raw, nil
}

func TestBuildExistingPlanUsesFirstFreeGPTSlot(t *testing.T) {
	top := recoveryresize.Topology{
		DiskPath:               "/dev/nvme0n1",
		DiskGUID:               "11111111-2222-4333-8444-555555555555",
		DiskSizeBytes:          500 * GiB,
		LogicalSectorBytes:     512,
		RootPartitionPath:      "/dev/nvme0n1p2",
		RootPartitionNumber:    2,
		RootPartitionTypeGUID:  "ca7d7ccb-63ed-4c53-861c-1742536059cc",
		RootPARTUUID:           "11111111-aaaa-4bbb-8ccc-222222222222",
		RootStartBytes:         2 * GiB,
		RootEndBytes:           450 * GiB,
		RootSizeBytes:          448 * GiB,
		MappingName:            "cryptroot",
		MappingPath:            "/dev/mapper/cryptroot",
		LUKSUUID:               "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		LUKSPayloadOffsetBytes: 16 * 1024 * 1024,
		RootFilesystem:         recoveryresize.FilesystemBtrfs,
		RootMountpoint:         "/",
		RootMountedRW:          true,
		BtrfsUUID:              "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff",
		BtrfsDeviceID:          1,
		BtrfsDeviceBytes:       448 * GiB,
		BtrfsUsedBytes:         120 * GiB,
	}

	raw := []byte(`{
      "blockdevices": [{
        "path":"/dev/nvme0n1",
        "type":"disk",
        "size":536870912000,
        "model":"Example NVMe",
        "serial":"SERIAL",
        "wwn":"eui.example",
        "pttype":"gpt",
        "ptuuid":"11111111-2222-4333-8444-555555555555",
        "children":[
          {
            "path":"/dev/nvme0n1p1",
            "type":"part",
            "size":1073741824,
            "partn":1,
            "partlabel":"EFI",
            "parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
            "partuuid":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
            "fstype":"vfat"
          },
          {
            "path":"/dev/nvme0n1p2",
            "type":"part",
            "size":481036337152,
            "partn":2,
            "partlabel":"root",
            "parttype":"ca7d7ccb-63ed-4c53-861c-1742536059cc",
            "partuuid":"11111111-aaaa-4bbb-8ccc-222222222222"
          },
          {
            "path":"/dev/nvme0n1p3",
            "type":"part",
            "size":8589934592,
            "partn":3,
            "partlabel":"KEEP-ME",
            "parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4",
            "partuuid":"33333333-aaaa-4bbb-8ccc-222222222222",
            "fstype":"ext4"
          }
        ]
      }]
    }`)

	plan, err := BuildExistingPlan(
		context.Background(),
		existingPlanRunner{raw: raw},
		top,
	)
	if err != nil {
		t.Fatal(err)
	}

	if plan.Recovery == nil {
		t.Fatal("recovery partition missing")
	}

	if plan.Recovery.Number != 4 {
		t.Fatalf(
			"expected first free GPT slot 4, got %d",
			plan.Recovery.Number,
		)
	}

	if plan.Recovery.SizeBytes != RecoveryBytes {
		t.Fatalf(
			"recovery size=%d want=%d",
			plan.Recovery.SizeBytes,
			RecoveryBytes,
		)
	}

	if plan.Root.Partition.PARTUUID != top.RootPARTUUID {
		t.Fatal("root PARTUUID was not preserved")
	}

	if plan.TargetDisk.GPTDiskGUID != top.DiskGUID {
		t.Fatal("GPT disk GUID was not preserved")
	}
}

func TestBuildExistingPlanReportsPresentRecovery(t *testing.T) {
	top := recoveryresize.Topology{
		DiskPath:               "/dev/nvme0n1",
		DiskGUID:               "11111111-2222-4333-8444-555555555555",
		DiskSizeBytes:          500 * GiB,
		LogicalSectorBytes:     512,
		RootPartitionPath:      "/dev/nvme0n1p2",
		RootPartitionNumber:    2,
		RootPartitionTypeGUID:  "ca7d7ccb-63ed-4c53-861c-1742536059cc",
		RootPARTUUID:           "11111111-aaaa-4bbb-8ccc-222222222222",
		RootStartBytes:         2 * GiB,
		RootEndBytes:           450 * GiB,
		RootSizeBytes:          448 * GiB,
		MappingName:            "cryptroot",
		MappingPath:            "/dev/mapper/cryptroot",
		LUKSUUID:               "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		LUKSPayloadOffsetBytes: 16 * 1024 * 1024,
		RootFilesystem:         recoveryresize.FilesystemBtrfs,
		RootMountpoint:         "/",
		RootMountedRW:          true,
		BtrfsUUID:              "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff",
		BtrfsDeviceID:          1,
		BtrfsDeviceBytes:       448 * GiB,
		BtrfsUsedBytes:         120 * GiB,
	}

	raw := []byte(`{
      "blockdevices": [{
        "path":"/dev/nvme0n1",
        "type":"disk",
        "size":536870912000,
        "model":"Example NVMe",
        "serial":"SERIAL",
        "wwn":"eui.example",
        "pttype":"gpt",
        "ptuuid":"11111111-2222-4333-8444-555555555555",
        "children":[
          {
            "path":"/dev/nvme0n1p1",
            "type":"part",
            "size":1073741824,
            "partn":1,
            "partlabel":"EFI",
            "parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
            "partuuid":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
            "fstype":"vfat"
          },
          {
            "path":"/dev/nvme0n1p2",
            "type":"part",
            "size":481036337152,
            "partn":2,
            "partlabel":"root",
            "parttype":"ca7d7ccb-63ed-4c53-861c-1742536059cc",
            "partuuid":"11111111-aaaa-4bbb-8ccc-222222222222"
          },
          {
            "path":"/dev/nvme0n1p3",
            "type":"part",
            "size":8589934592,
            "partn":3,
            "partlabel":"GJALLAR-RECOVERY",
            "parttype":"bc13c2ff-59e6-4262-a352-b275fd6f7172",
            "partuuid":"33333333-aaaa-4bbb-8ccc-222222222222",
            "fstype":"ext4"
          }
        ]
      }]
    }`)

	_, err := BuildExistingPlan(
		context.Background(),
		existingPlanRunner{raw: raw},
		top,
	)
	var present *RecoveryPresentError
	if !errors.As(err, &present) {
		t.Fatalf("err = %v, want RecoveryPresentError", err)
	}

	if present.Partition != "/dev/nvme0n1p3" {
		t.Fatalf("partition = %q", present.Partition)
	}
}

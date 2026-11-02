package diskplan

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	testDiskGUID     = "11111111-2222-3333-4444-555555555555"
	testESPPARTUUID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testRootPARTUUID = "11111111-aaaa-bbbb-cccc-222222222222"
	testRecPARTUUID  = "99999999-8888-7777-6666-555555555555"

	efiSystemPartitionType = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	linuxLUKSType          = "ca7d7ccb-63ed-4c53-861c-1742536059cc"
	xbootldrType           = "bc13c2ff-59e6-4262-a352-b275fd6f7172"
)

func validPlan() Plan {
	recovery := &Partition{
		Role:      RoleRecovery,
		Number:    3,
		Label:     "GJALLAR-RECOVERY",
		TypeGUID:  xbootldrType,
		PARTUUID:  testRecPARTUUID,
		SizeBytes: 12 * 1024 * 1024 * 1024,
		Filesystem: Filesystem{
			Type:  "vfat",
			Label: "GJALLAR-REC",
		},
		MountPoint: "/recovery",
	}

	return Plan{
		SchemaVersion: SchemaVersion,
		TargetDisk: Disk{
			Path:        "/dev/nvme0n1",
			Model:       "Example NVMe",
			Serial:      "EXAMPLE-SERIAL",
			WWN:         "eui.0011223344556677",
			SizeBytes:   1024 * 1024 * 1024 * 1024,
			GPTDiskGUID: testDiskGUID,
		},
		ESP: Partition{
			Role:      RoleESP,
			Number:    1,
			Label:     "EFI",
			TypeGUID:  efiSystemPartitionType,
			PARTUUID:  testESPPARTUUID,
			SizeBytes: 1024 * 1024 * 1024,
			Filesystem: Filesystem{
				Type:  "vfat",
				Label: "EFI",
			},
			MountPoint: "/boot",
		},
		Root: Root{
			Partition: Partition{
				Role:      RoleRoot,
				Number:    2,
				Label:     "GJALLAROS",
				TypeGUID:  linuxLUKSType,
				PARTUUID:  testRootPARTUUID,
				SizeBytes: 900 * 1024 * 1024 * 1024,
				Filesystem: Filesystem{
					Type:  "btrfs",
					Label: "GJALLAROS",
					Options: map[string]string{
						"compress": "zstd",
					},
				},
				MountPoint: "/",
			},
			Encryption: Encryption{
				Type:        EncryptionLUKS2,
				MappingName: "cryptroot",
			},
		},
		Recovery: recovery,
	}
}

func TestPlanValidate(t *testing.T) {
	plan := validPlan()

	if err := plan.Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestPlanWithoutRecoveryIsValid(t *testing.T) {
	plan := validPlan()
	plan.Recovery = nil

	if err := plan.Validate(); err != nil {
		t.Fatalf("plan without recovery rejected: %v", err)
	}
}

func TestPlanRequiresStableDiskIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{
			name: "no identity",
			mutate: func(plan *Plan) {
				plan.TargetDisk.Serial = ""
				plan.TargetDisk.WWN = ""
			},
		},
		{
			name: "serial fallback",
			mutate: func(plan *Plan) {
				plan.TargetDisk.WWN = ""
			},
		},
		{
			name: "wwn without serial",
			mutate: func(plan *Plan) {
				plan.TargetDisk.Serial = ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := validPlan()
			tt.mutate(&plan)

			err := plan.Validate()

			if tt.name == "no identity" {
				if err == nil {
					t.Fatal("plan without serial or WWN was accepted")
				}
				return
			}

			if err != nil {
				t.Fatalf("valid stable identity rejected: %v", err)
			}
		})
	}
}

func TestPlanRejectsInvalidStructure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{
			name: "schema",
			mutate: func(plan *Plan) {
				plan.SchemaVersion = 99
			},
		},
		{
			name: "relative disk",
			mutate: func(plan *Plan) {
				plan.TargetDisk.Path = "nvme0n1"
			},
		},
		{
			name: "bad disk guid",
			mutate: func(plan *Plan) {
				plan.TargetDisk.GPTDiskGUID = "not-a-guid"
			},
		},
		{
			name: "duplicate partition number",
			mutate: func(plan *Plan) {
				plan.Root.Partition.Number = plan.ESP.Number
			},
		},
		{
			name: "duplicate partuuid",
			mutate: func(plan *Plan) {
				plan.Root.Partition.PARTUUID = plan.ESP.PARTUUID
			},
		},
		{
			name: "duplicate mount",
			mutate: func(plan *Plan) {
				plan.ESP.MountPoint = "/"
			},
		},
		{
			name: "wrong root mount",
			mutate: func(plan *Plan) {
				plan.Root.Partition.MountPoint = "/mnt/root"
			},
		},
		{
			name: "root not luks2",
			mutate: func(plan *Plan) {
				plan.Root.Encryption.Type = "luks1"
			},
		},
		{
			name: "empty mapping",
			mutate: func(plan *Plan) {
				plan.Root.Encryption.MappingName = ""
			},
		},
		{
			name: "mapping is path",
			mutate: func(plan *Plan) {
				plan.Root.Encryption.MappingName = "/dev/mapper/cryptroot"
			},
		},
		{
			name: "wrong recovery role",
			mutate: func(plan *Plan) {
				plan.Recovery.Role = RoleRoot
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := validPlan()
			tt.mutate(&plan)

			if err := plan.Validate(); err == nil {
				t.Fatal("invalid plan was accepted")
			}
		})
	}
}

func TestPlanJSONRoundTripAndContainsNoSecretFields(t *testing.T) {
	plan := validPlan()

	data, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)

	for _, forbidden := range []string{
		"passphrase",
		"password",
		"private_key",
		"recovery_key",
		"luks_key",
		"secret",
	} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("serialized plan contains forbidden secret field %q:\n%s", forbidden, text)
		}
	}

	var decoded Plan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped plan rejected: %v", err)
	}

	if decoded.TargetDisk.GPTDiskGUID != plan.TargetDisk.GPTDiskGUID {
		t.Fatalf(
			"disk GUID changed during round trip: %q",
			decoded.TargetDisk.GPTDiskGUID,
		)
	}

	if decoded.Recovery == nil ||
		decoded.Recovery.PARTUUID != plan.Recovery.PARTUUID {
		t.Fatalf("recovery partition did not round trip: %#v", decoded.Recovery)
	}
}

func TestPlanJSONIsDeterministic(t *testing.T) {
	plan := validPlan()

	first, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}

	second, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Fatalf(
			"same plan serialized differently:\nFIRST:\n%s\nSECOND:\n%s",
			first,
			second,
		)
	}
}

func TestRecoveryIdentityFieldsMatchExistingContractNeeds(t *testing.T) {
	plan := validPlan()

	data, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)

	for _, required := range []string{
		`"gpt_disk_guid"`,
		`"serial"`,
		`"wwn"`,
		`"partuuid"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf(
				"serialized plan is missing recovery-compatible identity field %s:\n%s",
				required,
				text,
			)
		}
	}
}

func TestPlanAllowsMissingTargetModel(t *testing.T) {
	plan := validPlan()
	plan.TargetDisk.Model = ""

	if err := plan.Validate(); err != nil {
		t.Fatalf(
			"plan with stable serial/WWN and missing display model was rejected: %v",
			err,
		)
	}
}

package secureboot

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func enrollmentTestSnapshot() FirmwarePolicySnapshot {
	return FirmwarePolicySnapshot{
		Schema:                  2,
		ModelID:                 "model/framework/laptop-13-amd-ryzen-7040",
		SourceEntity:            "vendor/framework",
		FirmwareName:            "Framework UEFI",
		SetupModeStrategy:       "clear-platform-key",
		EnrollmentBackend:       "sbctl",
		RequiredPresent:         []string{"KEK", "db", "dbx"},
		RequiredAbsent:          []string{"PK"},
		PreserveFirmwareBuiltin: []string{"KEK", "db"},
		Untouched:               []string{"dbx"},
		FactoryOwnershipProof:   "pk-equals-pkdefault",
		Instructions:            []string{"Delete only the Platform Key."},
	}
}

func frameworkPolicySnapshotForEnrollmentTest() FirmwarePolicySnapshot {
	return FirmwarePolicySnapshot{
		Schema:                  2,
		ModelID:                 "model/framework/laptop-13-amd-ryzen-7040",
		SourceEntity:            "vendor/framework",
		FirmwareName:            "Framework UEFI",
		SetupModeStrategy:       "clear-platform-key",
		EnrollmentBackend:       "sbctl",
		RequiredPresent:         []string{"KEK", "db", "dbx"},
		RequiredAbsent:          []string{"PK"},
		PreserveFirmwareBuiltin: []string{"KEK", "db"},
		Untouched:               []string{"dbx"},
		FactoryOwnershipProof:   "pk-equals-pkdefault",
		Instructions:            []string{"Delete only PK."},
	}
}

func TestEnrollFirmwareWaitsOutsideSetupMode(t *testing.T) {
	ops := enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			return Inspection{
				State:       StatePendingEnrollment,
				SetupMode:   false,
				Description: "factory PK still active",
			}, nil
		},
	}

	result, err := enrollFirmware(
		context.Background(),
		enrollmentTestSnapshot(),
		ops,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != EnrollmentWaitingForSetupMode {
		t.Fatalf("result = %q", result)
	}
}

func TestEnrollFirmwareRefusesUnknownSetupModeOwnership(t *testing.T) {
	ops := enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			return Inspection{
				State:       StateSetupModeUnknown,
				SetupMode:   true,
				Description: "no trusted pending ownership record",
			}, nil
		},
	}

	_, err := enrollFirmware(
		context.Background(),
		enrollmentTestSnapshot(),
		ops,
	)
	if err == nil {
		t.Fatal("accepted Setup Mode without trusted pending ownership")
	}
}

func TestEnrollFirmwareFailsClosedWhenRequiredVariableMissing(t *testing.T) {
	ops := enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			return Inspection{
				State:     StatePendingEnrollment,
				SetupMode: true,
			}, nil
		},
		present: func(name string) (bool, error) {
			return name != "db", nil
		},
		payload: func(name string) ([]byte, bool, error) {
			return []byte("stable-" + name), true, nil
		},
	}

	_, err := enrollFirmware(
		context.Background(),
		enrollmentTestSnapshot(),
		ops,
	)
	if err == nil {
		t.Fatal("accepted missing required EFI variable")
	}
}

func TestEnrollFirmwareUsesPolicyForFirmwareBuiltinEnrollment(t *testing.T) {
	var commands [][]string
	inspection := 0

	ops := enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			inspection++
			if inspection == 1 {
				return Inspection{
					State:     StatePendingEnrollment,
					SetupMode: true,
				}, nil
			}
			return Inspection{
				State:     StateGjallarManaged,
				SetupMode: false,
			}, nil
		},
		present: func(name string) (bool, error) {
			return name != "PK", nil
		},
		payload: func(name string) ([]byte, bool, error) {
			return []byte("stable-" + name), true, nil
		},
		glob: func(pattern string) ([]string, error) {
			return []string{"/efi/" + pattern}, nil
		},
		command: func(_ context.Context, name string, args ...string) error {
			call := append([]string{name}, args...)
			commands = append(commands, call)
			return nil
		},
		verifyArtifacts: func(context.Context) error {
			return nil
		},
		recordOwnership: func(_ context.Context, stage string) error {
			if stage != "enrolled" {
				return fmt.Errorf("unexpected stage %q", stage)
			}
			return nil
		},
	}

	result, err := enrollFirmware(
		context.Background(),
		enrollmentTestSnapshot(),
		ops,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != EnrollmentCompleted {
		t.Fatalf("result = %q", result)
	}

	want := []string{
		"sbctl",
		"enroll-keys",
		"--firmware-builtin=KEK,db",
		"--yes-this-might-brick-my-machine",
	}

	found := false
	for _, command := range commands {
		if reflect.DeepEqual(command, want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("policy-derived sbctl invocation missing; commands = %#v", commands)
	}
}

func TestEnrollFirmwareNeverMutatesUntouchedDBX(t *testing.T) {
	var touched []string
	inspection := 0

	ops := enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			inspection++
			if inspection == 1 {
				return Inspection{State: StatePendingEnrollment, SetupMode: true}, nil
			}
			return Inspection{State: StateGjallarManaged}, nil
		},
		present: func(name string) (bool, error) {
			return name != "PK", nil
		},
		payload: func(name string) ([]byte, bool, error) {
			return []byte("stable-" + name), true, nil
		},
		glob: func(pattern string) ([]string, error) {
			touched = append(touched, pattern)
			return []string{"/efi/" + pattern}, nil
		},
		command:         func(context.Context, string, ...string) error { return nil },
		verifyArtifacts: func(context.Context) error { return nil },
		recordOwnership: func(context.Context, string) error { return nil },
	}

	if _, err := enrollFirmware(
		context.Background(),
		enrollmentTestSnapshot(),
		ops,
	); err != nil {
		t.Fatal(err)
	}

	for _, value := range touched {
		if value == "/sys/firmware/efi/efivars/dbx-*" {
			t.Fatal("untouched dbx entered mutation path")
		}
	}
}

func TestEnrollFirmwareRejectsModifiedUntouchedVariable(t *testing.T) {
	snapshot := frameworkPolicySnapshotForEnrollmentTest()

	enrolled := false

	ops := enrollmentOps{
		present: func(name string) (bool, error) {
			return name != "PK", nil
		},
		payload: func(name string) ([]byte, bool, error) {
			if name != "dbx" {
				return []byte("stable"), true, nil
			}

			if !enrolled {
				return []byte("factory-dbx"), true, nil
			}

			return []byte("modified-dbx"), true, nil
		},
		inspect: func(context.Context) (Inspection, error) {
			if !enrolled {
				return Inspection{
					State:     StatePendingEnrollment,
					SetupMode: true,
				}, nil
			}

			return Inspection{
				State:     StateGjallarManaged,
				SetupMode: false,
			}, nil
		},
		command: func(_ context.Context, name string, _ ...string) error {
			enrolled = enrolled || name == "sbctl"
			return nil
		},
		verifyArtifacts: func(context.Context) error {
			return nil
		},
		recordOwnership: func(context.Context, string) error {
			return nil
		},
		glob: func(pattern string) ([]string, error) {
			return []string{"/sys/firmware/efi/efivars/KEK-test"}, nil
		},
	}

	_, err := enrollFirmware(context.Background(), snapshot, ops)
	if err == nil {
		t.Fatal("accepted mutation of untouched EFI variable")
	}

	if !strings.Contains(err.Error(), "dbx changed during enrollment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Without the built-in db nothing vouches for option ROM signers, so sbctl's
// option ROM check must stay active.
func TestEnrollKeysArgsKeepsOptionROMCheckWithoutBuiltinDB(t *testing.T) {
	snapshot := enrollmentTestSnapshot()
	snapshot.PreserveFirmwareBuiltin = []string{"KEK"}
	got := enrollKeysArgs(snapshot, false)
	want := []string{"enroll-keys", "--firmware-builtin=KEK"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestEnrollFirmwareFinishesAfterKeysAlreadyEnrolled(t *testing.T) {
	var calls []string
	ops := enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			return Inspection{State: StateGjallarManaged, SetupMode: false}, nil
		},
		armed: func() (bool, error) { return true, nil },
		command: func(_ context.Context, name string, args ...string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil
		},
		verifyArtifacts: func(context.Context) error { calls = append(calls, "verify"); return nil },
		recordOwnership: func(_ context.Context, stage string) error {
			calls = append(calls, "record "+stage)
			return nil
		},
	}

	result, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops)
	if err != nil {
		t.Fatal(err)
	}
	if result != EnrollmentCompleted {
		t.Fatalf("result = %q, want completed", result)
	}
	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "sbctl") {
		t.Fatalf("resumed enrollment wrote keys again:\n%s", joined)
	}
	for _, want := range []string{"verify", "record enrolled", FinalMarkerPath, EnrollmentMarkerPath} {
		if !strings.Contains(joined, want) {
			t.Fatalf("resumed enrollment skipped %q:\n%s", want, joined)
		}
	}
}

func TestEnrollFirmwareLeavesCompletedOwnershipAlone(t *testing.T) {
	ops := enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			return Inspection{State: StateGjallarManaged, SetupMode: false}, nil
		},
		armed: func() (bool, error) { return false, nil },
	}
	result, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops)
	if err != nil || result != EnrollmentWaitingForSetupMode {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func dbxRestoreOps(dbx []byte, dbxDefault []byte, readBack func([]byte) []byte, commands *[]string) enrollmentOps {
	inspection := 0
	return enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			inspection++
			if inspection == 1 {
				return Inspection{State: StatePendingEnrollment, SetupMode: true}, nil
			}
			return Inspection{State: StateGjallarManaged}, nil
		},
		present: func(name string) (bool, error) {
			if name == "dbx" {
				return dbx != nil, nil
			}
			return name != "PK", nil
		},
		payload: func(name string) ([]byte, bool, error) {
			switch name {
			case "dbx":
				return dbx, dbx != nil, nil
			case "dbxDefault":
				return dbxDefault, dbxDefault != nil, nil
			}
			return []byte("stable-" + name), true, nil
		},
		writeDBX: func(_ context.Context, esl []byte) error {
			*commands = append(*commands, "write-dbx")
			dbx = readBack(esl)
			return nil
		},
		glob: func(pattern string) ([]string, error) {
			return []string{"/efi/" + pattern}, nil
		},
		command: func(_ context.Context, name string, _ ...string) error {
			*commands = append(*commands, name)
			return nil
		},
		verifyArtifacts: func(context.Context) error { return nil },
		recordOwnership: func(context.Context, string) error { return nil },
	}
}

func TestEnrollFirmwareRestoresEmptyDBXBeforeEnrolling(t *testing.T) {
	var commands []string
	same := func(esl []byte) []byte { return append([]byte(nil), esl...) }
	ops := dbxRestoreOps(nil, []byte("default-dbx"), same, &commands)

	if _, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops); err != nil {
		t.Fatal(err)
	}
	if len(commands) == 0 || commands[0] != "write-dbx" {
		t.Fatalf("dbx was not restored first: %v", commands)
	}
	if !reflect.DeepEqual(commands[len(commands)-4:len(commands)-2], []string{"chattr", "sbctl"}) {
		t.Fatalf("enrollment did not follow the restore: %v", commands)
	}
}

func TestEnrollFirmwareStopsWhenRestoredDBXDiffers(t *testing.T) {
	var commands []string
	changed := func([]byte) []byte { return []byte("something-else") }
	ops := dbxRestoreOps([]byte{}, []byte("default-dbx"), changed, &commands)

	_, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("got %v, want a read-back mismatch", err)
	}
	if !reflect.DeepEqual(commands, []string{"write-dbx"}) {
		t.Fatalf("enrollment continued after a bad restore: %v", commands)
	}
}

func TestEnrollFirmwareRefusesEmptyDBXWithoutDefault(t *testing.T) {
	var commands []string
	same := func(esl []byte) []byte { return esl }
	ops := dbxRestoreOps(nil, nil, same, &commands)

	_, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops)
	if err == nil || !strings.Contains(err.Error(), "no default dbx") {
		t.Fatalf("got %v, want a refusal", err)
	}
	if len(commands) != 0 {
		t.Fatalf("firmware was changed: %v", commands)
	}
}

func TestEnrollFirmwareKeepsPopulatedDBX(t *testing.T) {
	var commands []string
	same := func(esl []byte) []byte { return esl }
	ops := dbxRestoreOps([]byte("current-dbx"), []byte("default-dbx"), same, &commands)

	if _, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops); err != nil {
		t.Fatal(err)
	}
	for _, name := range commands {
		if name == "write-dbx" {
			t.Fatal("a populated dbx was replaced")
		}
	}
}

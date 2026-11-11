package secureboot

import (
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

// The Framework ODDC policy: the user clears only PK, the firmware's KEK and
// db stay trusted beside the gjallarOS keys, dbx is never written.
func TestOwnershipPlanDescribesEachKeyDatabaseFromPolicy(t *testing.T) {
	policy := oddc.SecureBootFirmwarePolicy{
		Supported:               true,
		FirmwareName:            "Framework UEFI",
		RequiredAbsent:          []string{"PK"},
		PreserveFirmwareBuiltin: []string{"KEK", "db"},
		Untouched:               []string{"dbx"},
	}
	plan := OwnershipPlan(policy, true)
	for _, want := range []string{
		"Firmware: Framework UEFI",
		"- PK: you remove the manufacturer PK in firmware setup; gjallarOS enrolls its own PK in its place.",
		"- KEK: the firmware's built-in manufacturer and Microsoft certificates stay trusted",
		"- db: the firmware's built-in manufacturer and Microsoft certificates stay trusted",
		"- dbx: not changed.",
		"disk (LUKS) passphrase",
		"supervisor password",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "replaced by the gjallarOS key only") {
		t.Errorf("plan claims a firmware database is dropped:\n%s", plan)
	}

	policy.PreserveFirmwareBuiltin = nil
	plan = OwnershipPlan(policy, false)
	if !strings.Contains(plan, "- db: replaced by the gjallarOS key only.") {
		t.Errorf("plan hides that db loses the firmware certificates:\n%s", plan)
	}
	if strings.Contains(plan, "LUKS") {
		t.Errorf("plan mentions TPM disk unlock that is not enabled:\n%s", plan)
	}
}

func TestFirmwareLockStepsOnlyWhenChosen(t *testing.T) {
	if steps := FirmwareLockSteps(false); steps != nil {
		t.Fatalf("declined lock still adds steps: %q", steps)
	}
	steps := strings.Join(FirmwareLockSteps(true), "\n")
	if !strings.Contains(steps, "supervisor") || !strings.Contains(steps, "not the disk passphrase") {
		t.Fatalf("lock steps: %q", steps)
	}
}

package app

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func validationFixture(t *testing.T) oddc.Resolved {
	t.Helper()
	original := localValidationMatches
	localValidationMatches = func(context.Context, oddc.ValidationTarget) (bool, error) { return false, nil }
	t.Cleanup(func() { localValidationMatches = original })
	const modelID = "model/framework/laptop-13-amd-ryzen-7040"

	return oddc.Resolved{
		ModelID: modelID,
		Validations: []oddc.Validation{
			{
				LastValidatedNixOS:             "26.05",
				LastValidatedGjallarOSRevision: "git:gjallar",
				LastValidatedDeviceID:          modelID,
				LastValidatedODDCRevision:      "git:oddc",
				LastValidatedAt:                "2026-09-11T14:00:00Z",
			},
		},
		Source: oddc.SourceMetadata{
			Revision: "git:oddc",
		},
	}
}

func validationTestUI(input string) (prompt.UI, *bytes.Buffer) {
	var out bytes.Buffer

	return prompt.UI{
		Reader: bufio.NewReader(strings.NewReader(input)),
		Out:    &out,
	}, &out
}

func TestDeviceValidationGateAcceptsValidatedProfile(t *testing.T) {
	ui, out := validationTestUI("")

	err := enforceDeviceValidation(
		context.Background(),
		ui,
		config.User{},
		validationFixture(t),
		"26.05",
		"git:gjallar",
	)
	if err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Fatalf("validated profile unexpectedly prompted: %q", out.String())
	}
}

func TestDeviceValidationGateRequiresInteractiveConfirmation(t *testing.T) {
	resolved := validationFixture(t)
	resolved.Validations[0].LastValidatedNixOS = "25.11"

	ui, out := validationTestUI("yes\n")

	err := enforceDeviceValidation(
		context.Background(),
		ui,
		config.User{},
		resolved,
		"26.05",
		"git:gjallar",
	)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "WARNING:") {
		t.Fatalf("missing validation warning: %q", out.String())
	}
}

func TestDeviceValidationGateRejectsUnattendedProfile(t *testing.T) {
	resolved := validationFixture(t)
	resolved.Validations[0].LastValidatedNixOS = "25.11"

	ui, _ := validationTestUI("")

	err := enforceDeviceValidation(
		context.Background(),
		ui,
		config.User{UnattendedInstall: true},
		resolved,
		"26.05",
		"git:gjallar",
	)
	if err == nil {
		t.Fatal("unvalidated unattended profile was accepted")
	}

	if !strings.Contains(err.Error(), "allowUnvalidatedODDCModel=true") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeviceValidationGateAllowsExplicitUnattendedOverride(t *testing.T) {
	resolved := validationFixture(t)
	resolved.Validations[0].LastValidatedNixOS = "25.11"

	ui, _ := validationTestUI("")

	err := enforceDeviceValidation(
		context.Background(),
		ui,
		config.User{
			UnattendedInstall:         true,
			AllowUnvalidatedODDCModel: true,
		},
		resolved,
		"26.05",
		"git:gjallar",
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeviceValidationGateRejectsUnvalidatedManagedUnattendedProfile(t *testing.T) {
	resolved := validationFixture(t)
	resolved.Validations[0].LastValidatedNixOS = "25.11"

	ui, _ := validationTestUI("")

	err := enforceDeviceValidation(
		context.Background(),
		ui,
		config.User{
			Management: config.ManagementIntent{
				Enable: true,
			},
			UnattendedInstall: true,
		},
		resolved,
		"26.05",
		"git:gjallar",
	)
	if err == nil {
		t.Fatal("unvalidated managed unattended profile was accepted")
	}

	if !strings.Contains(err.Error(), "allowUnvalidatedODDCModel=true") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeviceValidationGateAllowsExplicitManagedOverride(t *testing.T) {
	resolved := validationFixture(t)
	resolved.Validations[0].LastValidatedNixOS = "25.11"

	ui, _ := validationTestUI("")

	err := enforceDeviceValidation(
		context.Background(),
		ui,
		config.User{
			Management: config.ManagementIntent{
				Enable: true,
			},
			UnattendedInstall:         true,
			AllowUnvalidatedODDCModel: true,
		},
		resolved,
		"26.05",
		"git:gjallar",
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeviceValidationGateAcceptsMatchingLocalRecord(t *testing.T) {
	resolved := validationFixture(t)
	resolved.Validations[0].LastValidatedNixOS = "25.11"

	original := localValidationMatches
	localValidationMatches = func(
		context.Context,
		oddc.ValidationTarget,
	) (bool, error) {
		return true, nil
	}
	defer func() {
		localValidationMatches = original
	}()

	ui, out := validationTestUI("")

	err := enforceDeviceValidation(
		context.Background(),
		ui,
		config.User{},
		resolved,
		"26.05",
		"git:gjallar",
	)
	if err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Fatalf("matching local validation unexpectedly prompted: %q", out.String())
	}
}

func TestDeviceValidationGateCarriesIntoMaintenanceContinuation(t *testing.T) {
	resolved := validationFixture(t)
	resolved.Validations[0].LastValidatedNixOS = "25.11"

	t.Setenv(resumedDeviceGateEnv, deviceGate(resolved, "26.05", "git:gjallar"))
	ui, out := validationTestUI("")
	if err := enforceDeviceValidation(context.Background(), ui, config.User{}, resolved, "26.05", "git:gjallar"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("passed gate prompted again: %q", out.String())
	}

	// A repository that moved on since arming is a new target: ask again.
	ui, out = validationTestUI("")
	if err := enforceDeviceValidation(context.Background(), ui, config.User{}, resolved, "26.05", "git:newer"); err == nil {
		t.Fatal("changed revision reused the earlier approval")
	}
	if !strings.Contains(out.String(), "WARNING:") {
		t.Fatalf("changed revision did not prompt: %q", out.String())
	}
}

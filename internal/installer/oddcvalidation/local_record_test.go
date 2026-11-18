package oddcvalidation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

func validTestValidation() oddc.Validation {
	return oddc.Validation{
		LastValidatedNixOS:             "26.05",
		LastValidatedGjallarOSRevision: "git:gjallar123",
		LastValidatedDeviceID:          "laptop/framework/laptop-13-amd-ryzen-7040",
		LastValidatedODDCRevision:      "git:oddc456",
		LastValidatedAt:                "2026-09-11T20:30:00Z",
	}
}

func TestWriteLocalValidationUsesProtectedPermissions(t *testing.T) {
	var calls [][]string

	run := func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return nil, nil
	}

	var written []byte
	prev := writeRootFile
	t.Cleanup(func() { writeRootFile = prev })
	writeRootFile = func(_ context.Context, data []byte, mode, dst string) error {
		calls = append(calls, []string{"rootfile", mode, dst})
		written = data
		return nil
	}

	err := writeLocalValidation(
		context.Background(),
		validTestValidation(),
		run,
		"/var/lib/gjallarOS/oddc/validation.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "git:oddc456") {
		t.Fatalf("record not passed to root: %q", written)
	}

	joined := make([]string, 0, len(calls))
	for _, call := range calls {
		joined = append(joined, strings.Join(call, " "))
	}
	all := strings.Join(joined, "\n")

	if !strings.Contains(
		all,
		"sudo install -d -m 0700 /var/lib/gjallarOS/oddc",
	) {
		t.Fatalf("missing protected directory creation:\n%s", all)
	}

	// L54: the record reaches root from memory, never from a user file.
	if !strings.Contains(all, "rootfile 0600 /var/lib/gjallarOS/oddc/.validation.json.tmp") ||
		strings.Contains(all, "sudo install -m") {
		t.Fatalf("missing protected file installation:\n%s", all)
	}

	if !strings.Contains(
		all,
		"sudo mv -f -- /var/lib/gjallarOS/oddc/.validation.json.tmp /var/lib/gjallarOS/oddc/validation.json",
	) {
		t.Fatalf("missing atomic final replacement:\n%s", all)
	}
}

func TestWriteLocalValidationRejectsIncompleteMetadata(t *testing.T) {
	called := false

	run := func(
		context.Context,
		string,
		...string,
	) ([]byte, error) {
		called = true
		return nil, nil
	}

	err := writeLocalValidation(
		context.Background(),
		oddc.Validation{},
		run,
		"/var/lib/gjallarOS/oddc/validation.json",
	)

	if err == nil {
		t.Fatal("incomplete metadata was accepted")
	}
	if called {
		t.Fatal("privileged command ran for incomplete metadata")
	}
}

func TestWriteLocalValidationDoesNotMoveAfterInstallFailure(t *testing.T) {
	moved := false
	prev := writeRootFile
	t.Cleanup(func() { writeRootFile = prev })
	writeRootFile = func(context.Context, []byte, string, string) error {
		return errors.New("install failed")
	}

	run := func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := strings.Join(append([]string{name}, args...), " ")

		if strings.Contains(call, " mv ") {
			moved = true
		}

		return nil, nil
	}

	err := writeLocalValidation(
		context.Background(),
		validTestValidation(),
		run,
		"/var/lib/gjallarOS/oddc/validation.json",
	)

	if err == nil {
		t.Fatal("temporary install failure was accepted")
	}
	if moved {
		t.Fatal("final validation record moved after failed install")
	}
}

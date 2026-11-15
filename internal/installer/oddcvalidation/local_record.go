package oddcvalidation

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/rootfile"
)

const LocalValidationPath = "/var/lib/gjallarOS/oddc/validation.json"

type LocalValidationRecord struct {
	Schema     int             `json:"schema"`
	Validation oddc.Validation `json:"validation"`
}

type StateCommand func(
	context.Context,
	string,
	...string,
) ([]byte, error)

func WriteLocalValidation(
	ctx context.Context,
	validation oddc.Validation,
) error {
	return writeLocalValidation(
		ctx,
		validation,
		defaultStateCommand,
		LocalValidationPath,
	)
}

func writeLocalValidation(
	ctx context.Context,
	validation oddc.Validation,
	run StateCommand,
	path string,
) error {
	if strings.TrimSpace(validation.LastValidatedNixOS) == "" ||
		strings.TrimSpace(validation.LastValidatedGjallarOSRevision) == "" ||
		strings.TrimSpace(validation.LastValidatedDeviceID) == "" ||
		strings.TrimSpace(validation.LastValidatedODDCRevision) == "" ||
		strings.TrimSpace(validation.LastValidatedAt) == "" {
		return fmt.Errorf("refusing to persist incomplete ODDC validation metadata")
	}

	record := LocalValidationRecord{
		Schema:     1,
		Validation: validation,
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode local ODDC validation record: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	protectedTmp := filepath.Join(
		dir,
		".validation.json.tmp",
	)

	if _, err := run(
		ctx,
		"sudo", "install", "-d", "-m", "0700", dir,
	); err != nil {
		return fmt.Errorf("create ODDC state directory: %w", err)
	}

	// The record goes to root through a pipe, never through a file (L54).
	if err := writeRootFile(ctx, data, "0600", protectedTmp); err != nil {
		return fmt.Errorf("install temporary ODDC validation record: %w", err)
	}

	if _, err := run(
		ctx,
		"sudo", "mv", "-f", "--",
		protectedTmp, path,
	); err != nil {
		_, _ = run(ctx, "sudo", "rm", "-f", "--", protectedTmp)
		return fmt.Errorf("replace local ODDC validation record: %w", err)
	}

	return nil
}

// writeRootFile writes a root-only file from memory. Tests replace it.
var writeRootFile = rootfile.Write

func defaultStateCommand(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return defaultCommandRunner(ctx, "", name, args...)
}

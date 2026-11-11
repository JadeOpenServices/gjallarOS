package oddc

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type canonicalEvidence struct {
	SchemaVersion string         `json:"schemaVersion"`
	DeviceID      string         `json:"deviceId"`
	ObservedAt    string         `json:"observedAt"`
	Status        string         `json:"status"`
	Environment   map[string]any `json:"environment"`
}

func (source EmbeddedSource) loadCanonicalValidations(
	modelID string,
) ([]Validation, error) {
	root := filepath.Join(source.Root, "evidence")

	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf(
			"inspect ODDC evidence root: %w",
			err,
		)
	}

	var validations []Validation

	err := filepath.WalkDir(
		root,
		func(
			path string,
			entry fs.DirEntry,
			walkErr error,
		) error {
			if walkErr != nil {
				return walkErr
			}

			if entry.IsDir() ||
				filepath.Ext(path) != ".json" {
				return nil
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			var evidence canonicalEvidence
			if err := json.Unmarshal(
				raw,
				&evidence,
			); err != nil {
				return fmt.Errorf(
					"decode canonical evidence %s: %w",
					path,
					err,
				)
			}

			if evidence.SchemaVersion != "2.0.0" ||
				evidence.DeviceID != modelID ||
				evidence.Status != "hardware-validated" {
				return nil
			}

			text := func(key string) string {
				value, _ :=
					evidence.Environment[key].(string)
				return strings.TrimSpace(value)
			}

			validation := Validation{
				LastValidatedNixOS: text("nixOSRelease"),

				LastValidatedGjallarOSRevision: text("gjallarOSRevision"),

				LastValidatedDeviceID: evidence.DeviceID,

				LastValidatedODDCRevision: text("oddcRevision"),

				LastValidatedAt: text("validatedAt"),
			}

			// General hardware evidence can be hardware-validated without
			// representing a complete gjallarOS release-validation tuple.
			// Only complete tuples participate in the install gate.
			if validation.LastValidatedNixOS == "" ||
				validation.LastValidatedGjallarOSRevision == "" ||
				validation.LastValidatedODDCRevision == "" ||
				validation.LastValidatedAt == "" {
				return nil
			}

			validations = append(
				validations,
				validation,
			)

			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"scan canonical ODDC evidence: %w",
			err,
		)
	}

	return validations, nil
}

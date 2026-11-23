// Package oddcdraft drafts an ODDC model for a machine ODDC does not
// match, the way `oddc scaffold` does, so the user can refine, test and
// contribute it. The draft is never deployed: it stays out of oddc.device
// and /etc/oddc.
package oddcdraft

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"
)

// Catalog is the full ODDC source the installer was built with (the
// flake's oddc input), set with -ldflags. Empty when the build had none.
var Catalog string

// ErrMatched means ODDC's own classification matches the machine, so
// there is nothing to draft.
var ErrMatched = errors.New("ODDC matches this machine; nothing to draft")

const readme = `Draft ODDC model for this machine. ODDC has no model for it, so
gjallarOS installed its hardware-neutral baseline ("unsupported by ODDC"):
no ODDC device module, no device policy, no Secure Boot enrollment.

This draft is NOT deployed. It is the output of ODDC's scaffold for this
machine: model.json is the drafted entity, facts.json what the machine
reported, notes.txt what the draft leaves out.

To refine and contribute it:

    oddc workspace
    oddc scaffold
    oddc evidence record
    oddc contribute
`

// ErrNoCatalog says this installer was built without the ODDC catalog
// (the recovery tools): it can neither draft a model nor list known parts.
var ErrNoCatalog = errors.New("this installer has no ODDC catalog")

// Write drafts a model from facts (portable.ReadFacts("/sys") on the
// machine) and writes model.json, facts.json, notes.txt and README.txt
// into dir. It returns the drafted model ID.
func Write(catalog string, facts portable.Facts, dir string) (string, error) {
	if catalog == "" {
		return "", ErrNoCatalog
	}

	registry, err := portable.LoadRegistry(catalog)
	if err != nil {
		return "", fmt.Errorf("load ODDC catalog: %w", err)
	}

	classification, err := registry.Classify(facts)
	if err != nil {
		return "", fmt.Errorf("classify machine: %w", err)
	}
	if classification.Result != portable.ResultNone {
		return "", ErrMatched
	}

	entity, notes, err := registry.DraftModel("", facts)
	if err != nil {
		return "", fmt.Errorf("draft ODDC model: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for name, document := range map[string]any{"model.json": entity, "facts.json": facts} {
		data, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(data, '\n'), 0o644); err != nil {
			return "", err
		}
	}
	text := strings.Join(notes, "\n")
	if text != "" {
		text += "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(text), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte(readme), 0o644); err != nil {
		return "", err
	}

	return entity.Metadata.ID, nil
}

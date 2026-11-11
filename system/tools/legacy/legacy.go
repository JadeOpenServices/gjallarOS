// Package legacy reads the rename table (renames.json, Jade Q166) for
// messages about old names. It never makes an old name work: callers still
// refuse it and only say where it went (wiki 752 R39).
package legacy

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed renames.json
var shipped []byte

// Row is one rename. table.nix checks the same rows at build time.
type Row struct {
	Kind string `json:"kind"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

var rows = sync.OnceValues(func() ([]Row, error) {
	var table struct {
		Renames []Row `json:"renames"`
	}
	if err := json.Unmarshal(shipped, &table); err != nil {
		return nil, fmt.Errorf("legacy/renames.json: %w", err)
	}
	return table.Renames, nil
})

// ConfigField returns the new name of a retired user.config.json field.
func ConfigField(old string) (string, bool) {
	all, err := rows()
	if err != nil {
		return "", false
	}
	for _, row := range all {
		if row.Kind == "config-field" && row.Old == old {
			return row.New, true
		}
	}
	return "", false
}

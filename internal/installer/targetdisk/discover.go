package targetdisk

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"unicode"
)

// Rejected describes a whole block device that was discovered but did not pass
// the existing target-disk safety validator.
type Rejected struct {
	Path   string
	Reason string
}

type discoveryDevice struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type discoveryOutput struct {
	BlockDevices []discoveryDevice `json:"blockdevices"`
}

// Discover enumerates whole block devices and returns only candidates that pass
// the existing GJAL-29 target-disk validator.
//
// Discovery itself grants no installation authority. Validate remains the
// fail-closed safety boundary for root/install-media/identity/size checks.
func Discover(
	ctx context.Context,
	minSizeBytes uint64,
) ([]Result, []Rejected, error) {
	raw, err := exec.CommandContext(
		ctx,
		"lsblk",
		"--json",
		"--bytes",
		"--paths",
		"--output",
		"PATH,TYPE",
	).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("discover target disks with lsblk: %w", err)
	}

	var decoded discoveryOutput
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, nil, fmt.Errorf("parse target disk discovery: %w", err)
	}

	var eligible []Result
	var rejected []Rejected

	for _, device := range decoded.BlockDevices {
		if strings.TrimSpace(device.Type) != "disk" {
			continue
		}

		path := strings.TrimSpace(device.Path)
		if path == "" {
			continue
		}

		result, err := Validate(
			ctx,
			Input{
				Path:         path,
				MinSizeBytes: minSizeBytes,
			},
		)
		if err != nil {
			rejected = append(
				rejected,
				Rejected{
					Path:   path,
					Reason: err.Error(),
				},
			)
			continue
		}

		eligible = append(eligible, result)
	}

	sort.Slice(
		eligible,
		func(i, j int) bool {
			return eligible[i].Path < eligible[j].Path
		},
	)

	sort.Slice(
		rejected,
		func(i, j int) bool {
			return rejected[i].Path < rejected[j].Path
		},
	)

	return eligible, rejected, nil
}

// Text is the reason as the disk list shows it, with control characters
// from disk-reported names removed.
func (r Rejected) Text() string {
	return strings.Map(func(c rune) rune {
		if unicode.IsPrint(c) {
			return c
		}
		return -1
	}, r.Path+" — "+r.Reason)
}

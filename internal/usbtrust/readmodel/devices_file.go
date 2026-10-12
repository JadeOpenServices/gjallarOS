package readmodel

import (
	"context"
	"fmt"
	"os"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/oddcsource"
)

// DevicesSource returns ODDC's per-part USB facts for this model.
type DevicesSource interface {
	Devices(context.Context) (oddcsource.Devices, error)
}

// DevicesFile reads `oddc devices --json` output made at build time for
// the selected model.
type DevicesFile struct {
	Path  string
	Model string
}

func (file DevicesFile) Devices(ctx context.Context) (oddcsource.Devices, error) {
	if err := ctx.Err(); err != nil {
		return oddcsource.Devices{}, err
	}
	data, err := os.ReadFile(file.Path)
	if err != nil {
		return oddcsource.Devices{}, err
	}
	devices, err := oddcsource.ParseDevices(data)
	if err != nil {
		return oddcsource.Devices{}, err
	}
	if devices.Model != file.Model {
		return oddcsource.Devices{}, fmt.Errorf(
			"ODDC devices file is for %q, this machine is %q",
			devices.Model,
			file.Model,
		)
	}
	return devices, nil
}

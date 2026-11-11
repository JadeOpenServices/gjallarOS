package oddcvalidation

import (
	"fmt"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deviceprofile"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/sourcerevision"
)

type DeviceContext struct {
	Hostname string
	Hardware discovery.Hardware
	Resolved oddc.Resolved
	Revision string
}

type ConfigLoader func(path string) (config.User, error)
type RevisionResolver func(repo string, recovery bool) (string, error)
type HardwareDetector func(sysRoot string) discovery.Hardware

func LoadDeviceContext(repo string) (DeviceContext, error) {
	return loadDeviceContext(
		repo,
		config.Load,
		sourcerevision.Resolve,
		discovery.DetectHardware,
	)
}

func loadDeviceContext(
	repo string,
	loadConfig ConfigLoader,
	resolveRevision RevisionResolver,
	detectHardware HardwareDetector,
) (DeviceContext, error) {
	user, err := loadConfig(repo + "/user.config.json")
	if err != nil {
		return DeviceContext{}, fmt.Errorf(
			"load device validation configuration: %w",
			err,
		)
	}

	revision, err := resolveRevision(repo, false)
	if err != nil {
		return DeviceContext{}, fmt.Errorf(
			"resolve gjallarOS source revision: %w",
			err,
		)
	}

	hardware := detectHardware("/sys")

	source, err := deviceprofile.CurrentSource(repo)
	if err != nil {
		return DeviceContext{}, err
	}

	resolved, err := deviceprofile.Resolve(source, hardware)
	if err != nil {
		return DeviceContext{}, err
	}

	return DeviceContext{
		Hostname: user.Hostname,
		Hardware: hardware,
		Resolved: resolved,
		Revision: revision,
	}, nil
}

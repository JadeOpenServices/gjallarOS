package app

import (
	"context"
	"fmt"
	"os"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddcvalidation"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

var localValidationMatches = oddcvalidation.LocalValidationMatches

// resumedDeviceGateEnv carries the gate the run that armed a maintenance
// continuation passed. The continuation has no terminal and failed on this
// prompt with EOF (e2e-full, 2026-10-05).
const resumedDeviceGateEnv = "GJALLAR_INSTALLER_DEVICE_GATE"

// deviceGateKey names one validation target; a changed model, release or
// revision asks again.
func deviceGateKey(target oddc.ValidationTarget) string {
	return fmt.Sprintf(
		"%s|%s|%s|%s",
		target.DeviceID,
		target.NixOSRelease,
		target.GjallarOSRevision,
		target.ODDCRevision,
	)
}

func deviceGate(
	resolved oddc.Resolved,
	nixOSRelease string,
	gjallarOSRevision string,
) string {
	return deviceGateKey(oddc.ValidationTarget{
		NixOSRelease:      nixOSRelease,
		GjallarOSRevision: gjallarOSRevision,
		DeviceID:          resolved.ModelID,
		ODDCRevision:      resolved.Source.Revision,
	})
}

func enforceDeviceValidation(
	ctx context.Context,
	ui prompt.UI,
	user config.User,
	resolved oddc.Resolved,
	nixOSRelease string,
	gjallarOSRevision string,
) error {
	modelID := resolved.ModelID
	if modelID == "" {
		return nil
	}

	target := oddc.ValidationTarget{
		NixOSRelease:      nixOSRelease,
		GjallarOSRevision: gjallarOSRevision,
		DeviceID:          modelID,
		ODDCRevision:      resolved.Source.Revision,
	}

	for _, validation := range resolved.Validations {
		if validation.Matches(target) {
			return nil
		}
	}

	localMatch, err := localValidationMatches(
		ctx,
		target,
	)
	if err != nil {
		return err
	}
	if localMatch {
		return nil
	}

	if user.AllowUnvalidatedODDCModel {
		return nil
	}

	if passed := os.Getenv(resumedDeviceGateEnv); passed != "" && passed == deviceGateKey(target) {
		return nil
	}

	if user.UnattendedInstall {
		return fmt.Errorf(
			"ODDC model %q is not validated for NixOS %q, gjallarOS %q, and ODDC revision %q; unattended installation requires allowUnvalidatedODDCModel=true",
			modelID,
			nixOSRelease,
			gjallarOSRevision,
			resolved.Source.Revision,
		)
	}

	approved, err := ui.Confirm(
		ctx,
		fmt.Sprintf(
			"WARNING: ODDC model %q has not completed the full real-device validation gate for NixOS %s and the current gjallarOS/ODDC revisions. Continuing may cause hardware, graphics, tablet/sensor, or Secure Boot problems. Continue with this unvalidated device?",
			modelID,
			nixOSRelease,
		),
		false,
	)
	if err != nil {
		return err
	}

	if !approved {
		return fmt.Errorf(
			"installation cancelled because ODDC model %q is unvalidated for the current release",
			modelID,
		)
	}

	return nil
}

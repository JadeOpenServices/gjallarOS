package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

// tpm12Notice is printed when the only TPM is a TPM 1.2.
const tpm12Notice = `This machine has a TPM 1.2. GjallarOS cannot use Secure Boot or TPM disk unlock on it:
TPM 1.2 only supports SHA-1 measurements, and the boot policy needs TPM 2.0.
Some vendors ship a firmware update that turns it into a TPM 2.0; check the vendor's support page.
`

// Choices when the firmware enforces Secure Boot on a machine ODDC has no
// Secure Boot setup for.
const (
	secureBootOffReboot = "Reboot into firmware setup now to turn Secure Boot off"
	secureBootOffLater  = "Continue; I turn Secure Boot off before the first reboot"
	secureBootOffCancel = "Cancel the installation"
)

// Rerun on an installed system: one question instead of "force a clean
// redeploy?" followed by "update in place?".
const (
	existingUpdate   = "Update in place, keeping passwords, disk keys and hardware configuration"
	existingRedeploy = "Clean redeploy: reinstall the system configuration, keeping disk layout, credentials, disk keys and user data"
	existingLeave    = "Leave the installation unchanged"
)

func mayOfferSecureBootFallback(
	secureBootEnabled bool,
	managed bool,
	unattended bool,
) bool {
	return secureBootEnabled && !managed && !unattended
}

func tpm2AllowedForSecureBoot(secureBootEnabled bool) bool {
	return secureBootEnabled
}

// tpm2FollowsRequest reports whether the rendered TPM2 unlock setting is the
// user's request instead of a probe of the running system. Managed endpoints
// are TPM2-bound by contract, and a fresh install's LUKS2 root is created by
// rootprovision after this decision, so the live media's mappings say nothing
// about it (e2e-target, 2026-09-29: rendered false, TPM2 never enrolled).
func tpm2FollowsRequest(endpointManaged, persistentInstalledHost bool) bool {
	return endpointManaged || !persistentInstalledHost
}

// noSecureBootPolicyNotice tells the user why GjallarOS leaves Secure Boot
// alone on this machine. A generic "Secure Boot is disabled" read like a
// misdetection on the real HP, where nobody had said that ODDC simply has no
// Secure Boot setup for the model (2026-10-09).
func noSecureBootPolicyNotice(modelID string) string {
	lines := []string{"Secure Boot setup:"}
	if modelID == "" {
		lines = append(lines, "  ODDC has no model for this machine, so it has no Secure Boot setup for it either.")
	} else {
		lines = append(lines, "  ODDC has no Secure Boot setup for "+modelID+" yet.")
	}
	lines = append(lines,
		"  GjallarOS will not install its own Secure Boot keys here, and TPM2 disk unlock stays off because it depends on them.",
	)
	return strings.Join(lines, "\n") + "\n"
}

// firmwareSecureBootNotice reports the firmware's own Secure Boot state when
// GjallarOS installs without its Secure Boot keys. It used to print only
// inside the no-ODDC-setup branch, which needs a TPM and secureBootPrompt, so
// other paths installed an unbootable system without a word. Firmware
// without a SecureBoot variable has no Secure Boot and boots anything.
func firmwareSecureBootNotice(firmwareOn bool, firmwareErr error) string {
	switch {
	case firmwareErr != nil:
		return "Firmware Secure Boot state could not be read (" + firmwareErr.Error() + ").\n"
	case firmwareOn:
		return strings.Join([]string{
			"Firmware Secure Boot is ON, but GjallarOS Secure Boot is not set up for this install.",
			"The installed boot loader is not signed, so this firmware will refuse to start it.",
			"Secure Boot has to be off in firmware setup before the installed system boots.",
		}, "\n") + "\n"
	}
	return "Firmware Secure Boot is off; the machine boots as before.\n"
}

// Startup unlock choices. Both keep the recovery key as the fallback.
const (
	unlockAutomatic = "Automatic"
	unlockPIN       = "PIN at startup"
)

const startupUnlockText = `How should the disk unlock at startup?

  Automatic (recommended for most): nothing to type at startup; the security
  chip unlocks the disk only when the boot files are unchanged. This protects
  a stolen, switched-off device against reading the disk directly.

  PIN at startup (more security): you type a PIN of 6 or more characters at
  every startup; the security chip checks it and the boot files, and blocks
  guessing after too many wrong tries. The disk stays locked even when someone
  starts the device, until the PIN is typed.

You set the PIN at the first startup. Either way you get a recovery key
there, for when the chip cannot unlock the disk.
`

// keyboardOnlyAtStartup reports a tablet or detachable (SMBIOS chassis 30 or
// 32) where no on-screen keyboard can show at the disk prompt. Convertibles
// (31) keep their keyboard. With a touchscreen, unl0kr shows one.
func keyboardOnlyAtStartup(chassisType string, touchscreen bool) bool {
	return !touchscreen && (chassisType == "30" || chassisType == "32")
}

// collectStartupUnlock asks how the disk unlocks once GjallarOS Secure Boot
// is chosen. Both choices use the security chip.
func collectStartupUnlock(ctx context.Context, ui prompt.UI, chassisType string, touchscreen bool, u *config.User) error {
	label := "Startup unlock"
	if ui.GTK {
		label = startupUnlockText
	} else {
		fmt.Fprint(ui.Out, startupUnlockText)
	}
	def := unlockAutomatic
	if u.LUKSTPM2Enable && u.LUKSTPM2PIN {
		def = unlockPIN
	}
	choice, err := ui.Choice(ctx, label, def, []string{unlockAutomatic, unlockPIN})
	if err != nil {
		return err
	}
	u.LUKSTPM2Enable = true
	u.LUKSTPM2PIN = choice == unlockPIN
	if keyboardOnlyAtStartup(chassisType, touchscreen) {
		fmt.Fprintln(ui.Out, "This device has no touchscreen GjallarOS can use at startup: typing the PIN or the recovery key needs a keyboard attached.")
	}
	return nil
}

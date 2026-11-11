package diskcrypto

// Startup unlock choices, for the installer and gjallarctl luks chip. Both
// use the security chip and keep the recovery key as the fallback.
const (
	StartupUnlockAutomatic = "Automatic"
	StartupUnlockPIN       = "PIN at startup"
)

// StartupUnlockChoices lists both choices, the default first.
func StartupUnlockChoices() []string {
	return []string{StartupUnlockAutomatic, StartupUnlockPIN}
}

// DefaultStartupUnlock is the PIN on a separate or unconfirmed TPM, or when
// the PIN was chosen before; Automatic otherwise.
func DefaultStartupUnlock(kind TPMKind, pinBefore bool) string {
	if kind.PINRecommended() || pinBefore {
		return StartupUnlockPIN
	}
	return StartupUnlockAutomatic
}

// StartupUnlockText explains both choices. On a separate or unconfirmed
// TPM, only a PIN keeps the disk key in the chip against someone who has the
// device (ROADMAP section 19), so the PIN is the recommended choice there.
func StartupUnlockText(kind TPMKind) string {
	automatic, pin := " (recommended for most)", " (more security)"
	note := ""
	if kind.PINRecommended() {
		automatic, pin = "", " (recommended on this device)"
		note = `
  The security chip in this device is a separate part on the board, or
  GjallarOS could not confirm that it sits inside the processor. Without a
  PIN, someone who has the device and special tools can get the disk key
  out of such a chip.
`
	}
	return `How should the disk unlock at startup?

  Automatic` + automatic + `: nothing to type at startup; the security
  chip unlocks the disk only when the boot files are unchanged. This protects
  a stolen, switched-off device against reading the disk directly.
` + note + `
  PIN at startup` + pin + `: you type a PIN of 6 or more characters at
  every startup; the security chip checks it and the boot files, and blocks
  guessing after too many wrong tries. The disk stays locked even when someone
  starts the device, until the PIN is typed.
`
}

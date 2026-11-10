package secureboot

import (
	"fmt"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

// OwnershipPlan explains, before anything is generated or changed, what the
// Secure Boot ownership transfer does to each firmware key database and what
// the user must keep to unlock and administer the machine afterwards. The
// per-database lines come from the same ODDC policy that enrollment enforces.
func OwnershipPlan(policy oddc.SecureBootFirmwarePolicy, luksTPM2 bool) string {
	firmwareName := strings.TrimSpace(policy.FirmwareName)
	if firmwareName == "" {
		firmwareName = "system firmware"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `GJALLAROS SECURE BOOT - WHAT WILL CHANGE

Firmware: %s

`, firmwareName)

	for _, name := range []string{"PK", "KEK", "db", "dbx"} {
		switch {
		case slices.Contains(policy.Untouched, name):
			fmt.Fprintf(&b, "- %s: not changed.\n", name)
		case slices.Contains(policy.PreserveFirmwareBuiltin, name):
			fmt.Fprintf(&b, "- %s: the firmware's built-in manufacturer and Microsoft certificates stay trusted; GjallarOS adds its own key next to them.\n", name)
		case slices.Contains(policy.RequiredAbsent, name):
			fmt.Fprintf(&b, "- %s: you remove the manufacturer %s in firmware setup; GjallarOS enrolls its own %s in its place.\n", name, name, name)
		default:
			fmt.Fprintf(&b, "- %s: replaced by the GjallarOS key only.\n", name)
		}
	}

	b.WriteString(`
Afterwards only the GjallarOS Platform Key can change Secure Boot keys from
the running system. Boot loaders signed by GjallarOS or by a certificate kept
above still boot.

Firmware visits:
1. The installer reboots into firmware setup. Follow the device instructions
   shown before that reboot, save, and boot GjallarOS.
2. GjallarOS enrolls its keys and reboots into firmware setup once more.
   Enable Secure Boot, save, and boot GjallarOS.

What you need:
- access to firmware setup (know its password if one is already set)
- the Secure Boot recovery archive and passphrase shown next; store both
  offline, they restore the GjallarOS signing keys
`)
	if luksTPM2 {
		b.WriteString(`- your disk (LUKS) passphrase: the TPM opens the disk automatically only while
  Secure Boot and the signed boot chain are unchanged; otherwise it asks for
  the passphrase
`)
	}
	b.WriteString(`
Firmware setup itself stays open unless it has a supervisor password: anyone
at the machine could then turn Secure Boot off. GjallarOS can remind you to
set one during the second firmware visit. Use a new password, not the disk
passphrase, and store it offline; firmware setup, including Secure Boot, stays
locked without it.
`)
	return b.String()
}

// FirmwareLockSteps are the extra firmware-setup actions for the visit where
// Secure Boot gets enabled, when the user chose firmwarePasswordLock. The
// installer cannot set firmware passwords itself.
func FirmwareLockSteps(firmwareLock bool) []string {
	if !firmwareLock {
		return nil
	}
	return []string{
		"Set a firmware supervisor (administrator) password. Use a new password, not the disk passphrase.",
		"Store that password offline. Changing firmware settings, including Secure Boot, requires it from now on.",
	}
}

// LockedShimPlan explains the install-time shim path on locked firmware
// (Plane 293): Secure Boot is already on, the firmware keys cannot change,
// and shim from another system already starts this one.
func LockedShimPlan(luksTPM2 bool) string {
	var b strings.Builder
	b.WriteString(`GJALLAROS SECURE BOOT THROUGH THE SHIM THAT STARTS THIS SYSTEM - WHAT WILL CHANGE

- PK, KEK, db, dbx and firmware settings: not changed.
- shim already starts this system. The GjallarOS key goes into shim's list
  (MOK); if it is not there yet, you confirm it once in MokManager.
- Secure Boot stays on the whole time. Until shim lists the GjallarOS key,
  the installer does not change how this system starts.

Steps:
1. The installer makes the GjallarOS key and checks shim's list.
2. If the key is missing, it queues the key and stops. Restart; a blue screen
   (MokManager) asks you to confirm it with a one-time password you choose.
   Then run the installer again; it keeps your answers.
3. The installer builds GjallarOS with shim and restarts once more.

What you need:
- the Secure Boot recovery archive and passphrase, shown after the build and
  before the restart; store both
  offline, they restore the GjallarOS signing keys
`)
	if luksTPM2 {
		b.WriteString(`- your disk recovery key: the TPM opens the disk automatically only while
  the signed boot chain is unchanged; a later shim or key change may ask for
  the recovery key once
`)
	}
	b.WriteString(`
No recovery partition is set up on this path yet.
`)
	return b.String()
}

// ShimPlan explains the shim path before anything changes: the firmware
// keeps its maker's keys, shim (signed by the maker's CA) starts first and
// trusts the GjallarOS key once the user confirms it in MokManager.
func ShimPlan(firmwareName string, luksTPM2 bool) string {
	firmwareName = strings.TrimSpace(firmwareName)
	if firmwareName == "" {
		firmwareName = "system firmware"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `GJALLAROS SECURE BOOT - WHAT WILL CHANGE

Firmware: %s

- PK, KEK, db, dbx: not changed. The maker's keys stay.
- shim, signed by a certificate the firmware already trusts, starts first and
  starts GjallarOS only when it is signed with the GjallarOS key.
- The GjallarOS key goes into shim's list (MOK) after you confirm it once.

Steps:
1. GjallarOS makes its key and rebuilds with shim. Restart.
2. Run gjallarctl secure-boot setup: it queues the key. Restart; a blue
   screen (MokManager) asks you to confirm it with a one-time password you
   choose.
3. Run gjallarctl secure-boot setup again: restart into firmware setup, turn
   Secure Boot on, save.

What you need:
- access to firmware setup (know its password if one is already set)
- the Secure Boot recovery archive and passphrase shown next; store both
  offline, they restore the GjallarOS signing keys
`, firmwareName)
	if luksTPM2 {
		b.WriteString(`- your disk recovery key: the TPM opens the disk automatically only while
  Secure Boot and the signed boot chain are unchanged; after the switch it
  may ask for the recovery key once
`)
	}
	b.WriteString(`
Firmware setup itself stays open unless it has a supervisor password: anyone
at the machine could then turn Secure Boot off. Use a new password, not a
disk key, and store it offline.
`)
	return b.String()
}

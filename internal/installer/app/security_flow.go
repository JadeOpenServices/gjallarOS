package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// tpm12Notice is printed when the only TPM is a TPM 1.2.
const tpm12Notice = `This machine has a TPM 1.2. gjallarOS cannot use Secure Boot or TPM disk unlock on it:
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

// noLUKSMapping reports whether hardware.nix maps no encrypted system disk,
// so TPM2 unlock can never work. An unreadable file says nothing.
func noLUKSMapping(hardwarePath string) bool {
	mapping, err := diskcrypto.MappingDetails(hardwarePath)
	return err == nil && mapping.Name == ""
}

// keptTPM2 is what a kept hardware.nix does with a TPM2 unlock request.
type keptTPM2 int

const (
	// keptTPM2AsRequested renders the request unchanged.
	keptTPM2AsRequested keptTPM2 = iota
	// keptTPM2NoLUKS turns the request off: no encrypted system disk.
	keptTPM2NoLUKS
	// keptTPM2NoSecureBoot turns the request off: gjallarOS Secure Boot is
	// not set up, and the build refuses TPM2 unlock without it
	// (measured-boot.nix). A token left on the disk is removed by
	// gjallar-tpm2-clear (L47); the login card brings chip unlock back once
	// Secure Boot is set up (wiki 752 Q224).
	keptTPM2NoSecureBoot
)

// keptHardwareTPM2 decides a TPM2 request on a kept hardware.nix. Only
// read-only checks run here: installer tpm2 also writes TPM2 settings into
// hardware.nix, which a kept file must not get. Before this, a kept file
// rendered the request as is (e2e-shim, 2026-11-10: in place on an
// unencrypted disk, luksTpm2Enable = true and every rebuild stopped at
// "requires exactly one LUKS device").
func keptHardwareTPM2(
	requested, endpointManaged, persistentInstalledHost, secureBoot bool,
	hardwarePath string,
) keptTPM2 {
	if !requested || tpm2FollowsRequest(endpointManaged, persistentInstalledHost) {
		return keptTPM2AsRequested
	}
	if noLUKSMapping(hardwarePath) {
		return keptTPM2NoLUKS
	}
	if !tpm2AllowedForSecureBoot(secureBoot) {
		return keptTPM2NoSecureBoot
	}
	return keptTPM2AsRequested
}

// saveTPM2Off turns TPM2 unlock off in user.config.json too. The file is
// saved before the TPM2 decision; left at true, the next gjallarctl rebuild
// rendered TPM2 unlock again and failed its assertions (e2e-shim run 9,
// 2026-11-10: in place on an unencrypted disk, every rebuild stopped at
// "requires exactly one LUKS device").
func saveTPM2Off(user *config.User, presetPath string, out io.Writer) error {
	if !user.LUKSTPM2Enable {
		return nil
	}
	user.LUKSTPM2Enable = false
	if err := config.WriteAtomic(presetPath, *user); err != nil {
		return fmt.Errorf("save luksTpm2Enable=false to user.config.json: %w", err)
	}
	fmt.Fprintln(out, "Updated user.config.json: luksTpm2Enable=false, as installed.")
	return nil
}

// installerTPM2Args checks the running system for an installed host's TPM2
// request: its LUKS mapping, the TPM and the disk behind it. The request is
// the Startup unlock answer (or the saved one); the check asks nothing.
func installerTPM2Args(root, hardwarePath string, requested bool) []string {
	return []string{"installer", "tpm2", "--repo", root, "--hardware", hardwarePath, "--requested=" + strconv.FormatBool(requested)}
}

// noSecureBootPolicyNotice tells the user why gjallarOS leaves Secure Boot
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
		"  gjallarOS will not install its own Secure Boot keys here, and TPM2 disk unlock stays off because it depends on them.",
	)
	return strings.Join(lines, "\n") + "\n"
}

// secureBootDeclinedNotice says what an install without gjallarOS Secure
// Boot does not have, in words a non-technical owner can follow. encrypted
// says whether the root ends up encrypted: an in-place install over an
// unencrypted root has no passphrase to record or type (e2e-cp4,
// 2026-11-15). later says how to get it afterwards.
func secureBootDeclinedNotice(encrypted bool, later string) string {
	lines := []string{"Without gjallarOS Secure Boot:"}
	if encrypted {
		lines = append(lines,
			"  - Nothing checks the startup files before they run. Someone who gets the machine",
			"    alone for a while could change them to record the disk passphrase.",
			"  - The disk does not unlock on its own; the passphrase is typed at every startup.",
		)
	} else {
		lines = append(lines,
			"  - Nothing checks the startup files before they run. Someone who gets the machine",
			"    alone for a while could change them.",
		)
	}
	lines = append(lines,
		"  - No recovery partition. Repairs start from a gjallarOS USB stick.",
		"  - JODS cannot manage this machine.",
		"  "+later,
	)
	return strings.Join(lines, "\n") + "\n"
}

// firmwareSecureBootNotice reports the firmware's own Secure Boot state when
// gjallarOS installs without its Secure Boot keys. It used to print only
// inside the no-ODDC-setup branch, which needs a TPM and secureBootPrompt, so
// other paths installed an unbootable system without a word. Firmware
// without a SecureBoot variable has no Secure Boot and boots anything.
func firmwareSecureBootNotice(firmwareOn bool, firmwareErr error) string {
	switch {
	case firmwareErr != nil:
		return "Firmware Secure Boot state could not be read (" + firmwareErr.Error() + ").\n"
	case firmwareOn:
		return strings.Join([]string{
			"Firmware Secure Boot is ON, but gjallarOS Secure Boot is not set up for this install.",
			"The installed boot loader is not signed, so this firmware will refuse to start it.",
			"Secure Boot has to be off in firmware setup before the installed system boots.",
		}, "\n") + "\n"
	}
	return "Firmware Secure Boot is off; the machine boots as before.\n"
}

// startupUnlockText is the shared explanation of both choices and where
// the installer's PIN and recovery key come from.
func startupUnlockText(kind diskcrypto.TPMKind) string {
	return diskcrypto.StartupUnlockText(kind) + `
You set the PIN at the first startup. Either way you get a
recovery key there, for when the chip cannot unlock the disk.
`
}

// keyboardOnlyNotice is printed after the startup unlock choice on a
// device that cannot show an on-screen keyboard at the disk prompt.
const keyboardOnlyNotice = `This device has no touchscreen gjallarOS can use at startup:
typing the PIN or the recovery key needs a keyboard attached.`

// keyboardOnlyAtStartup reports a tablet or detachable (SMBIOS chassis 30 or
// 32) where no on-screen keyboard can show at the disk prompt. Convertibles
// (31) keep their keyboard. With a touchscreen, unl0kr shows one.
func keyboardOnlyAtStartup(chassisType string, touchscreen bool) bool {
	return !touchscreen && (chassisType == "30" || chassisType == "32")
}

// collectStartupUnlock asks how the disk unlocks once gjallarOS Secure Boot
// is chosen. Both choices use the security chip.
func collectStartupUnlock(ctx context.Context, ui prompt.UI, kind diskcrypto.TPMKind, chassisType string, touchscreen bool, u *config.User) error {
	text := startupUnlockText(kind)
	label := "Startup unlock"
	if ui.GTK {
		label = text
	} else {
		fmt.Fprint(ui.Out, text)
	}
	def := diskcrypto.DefaultStartupUnlock(kind, u.LUKSTPM2Enable && u.LUKSTPM2PIN)
	choice, err := ui.Choice(ctx, label, def, diskcrypto.StartupUnlockChoices())
	if err != nil {
		return err
	}
	u.LUKSTPM2Enable = true
	u.LUKSTPM2PIN = choice == diskcrypto.StartupUnlockPIN
	if keyboardOnlyAtStartup(chassisType, touchscreen) {
		fmt.Fprintln(ui.Out, keyboardOnlyNotice)
	}
	return nil
}

// firmwareTrustReport is what the firmware trusts, read before any Secure
// Boot question so the user sees it whatever they choose (Plane 293).
func firmwareTrustReport(t secureboot.FirmwareTrust) string {
	var b strings.Builder
	b.WriteString("What this firmware trusts:\n")
	for _, line := range t.Lines() {
		b.WriteString("  " + line + "\n")
	}
	for _, f := range t.Findings() {
		mark := "  Note: "
		if f.Critical {
			mark = "  WARNING: "
		}
		b.WriteString(mark + f.Text + "\n")
	}
	return b.String()
}

// lockedFirmwareStop ends the install on locked firmware where shim cannot
// help: Secure Boot cannot be turned off and own keys cannot go in.
func lockedFirmwareStop(why string) string {
	return "The firmware setup is locked, so Secure Boot cannot be turned off and gjallarOS cannot install its own keys here.\n" +
		why + "\n" +
		"Installation stopped before anything was installed.\n"
}

// chooseLockedShim decides an install on locked firmware (Plane 293). With
// Secure Boot on and the firmware keys fixed, gjallarOS can start only
// through shim, and only if a shim already starts this system: its MOK list
// is where the gjallarOS key goes. ok=false means the install stopped and
// the reason is printed.
func chooseLockedShim(ctx context.Context, ui prompt.UI, out io.Writer, root string, trust secureboot.FirmwareTrust, luksTPM2 bool) (secureboot.ShimFacts, bool, error) {
	started, err := secureboot.ShimStarted()
	if err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	if !started {
		fmt.Fprint(out, lockedFirmwareStop("shim does not start this system, so there is no list gjallarOS could add its key to."))
		return secureboot.ShimFacts{}, false, nil
	}
	if trust.ShimValidationOff {
		fmt.Fprint(out, lockedFirmwareStop("shim's own check is switched off in MokManager; turn it back on there and run the installer again."))
		return secureboot.ShimFacts{}, false, nil
	}
	fmt.Fprintln(out, "shim starts this system. Building the pinned gjallarOS shim to check it against the firmware...")
	facts, err := buildShimFacts(ctx, root)
	if err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	choice, err := secureboot.ChoosePath(trust, facts, nil)
	if err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	if choice.Path != secureboot.PathShim {
		fmt.Fprint(out, lockedFirmwareStop("The gjallarOS shim cannot start here: "+choice.Reason+"."))
		return secureboot.ShimFacts{}, false, nil
	}
	if err := ui.SecureBootOwnershipPlan(ctx, secureboot.LockedShimPlan(luksTPM2)); err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	yes, err := ui.Confirm(ctx, "Install gjallarOS with Secure Boot through shim?", true)
	if err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	if !yes {
		fmt.Fprintln(out, "Installation stopped before anything was installed.")
		return secureboot.ShimFacts{}, false, nil
	}
	return facts, true, nil
}

// offerUnknownModelShim offers Secure Boot through shim on a model ODDC has
// no Secure Boot setup for (Jade Q200): best effort, default no. shim needs
// nothing model-specific, only a firmware that keeps its maker's keys and
// trusts the pinned shim. With Secure Boot on, shim must already start this
// system: the gjallarOS key has to be in its MOK list before the boot chain
// changes. ok=false means the install goes on without Secure Boot.
// In Setup Mode there is no offer, only the reason and the way back
// (Jade Q220, Q223).
func offerUnknownModelShim(ctx context.Context, ui prompt.UI, out io.Writer, root string, trust secureboot.FirmwareTrust, luksTPM2 bool) (secureboot.ShimFacts, bool, error) {
	if trust.SetupMode == secureboot.FlagOn {
		fmt.Fprint(out, setupModeNotice)
		return secureboot.ShimFacts{}, false, nil
	}
	if trust.SecureBoot == secureboot.FlagOn {
		started, err := secureboot.ShimStarted()
		if err != nil {
			return secureboot.ShimFacts{}, false, err
		}
		if !started || trust.ShimValidationOff {
			return secureboot.ShimFacts{}, false, nil
		}
	}
	try, err := ui.Confirm(ctx, "Try Secure Boot through shim anyway? Nobody has tested it on this model yet; the check builds shim first, which takes a few minutes.", false)
	if err != nil || !try {
		return secureboot.ShimFacts{}, false, err
	}
	facts, err := buildShimFacts(ctx, root)
	if err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	choice, err := secureboot.ChoosePath(trust, facts, nil)
	if err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	if choice.Path != secureboot.PathShim {
		fmt.Fprintf(out, "Secure Boot through shim does not work here: %s. Installing without Secure Boot.\n", choice.Reason)
		return secureboot.ShimFacts{}, false, nil
	}
	plan := secureboot.ShimPlan("", luksTPM2)
	if trust.SecureBoot == secureboot.FlagOn {
		plan = secureboot.LockedShimPlan(luksTPM2)
	}
	if err := ui.SecureBootOwnershipPlan(ctx, plan); err != nil {
		return secureboot.ShimFacts{}, false, err
	}
	yes, err := ui.Confirm(ctx, "Install gjallarOS with Secure Boot through shim?", false)
	if err != nil || !yes {
		return secureboot.ShimFacts{}, false, err
	}
	return facts, true, nil
}

// setupModeNotice tells why a firmware in Setup Mode gets no shim offer.
// Own gjallarOS keys stay limited to models with ODDC facts (Q223).
const setupModeNotice = `The firmware is in Setup Mode: its Secure Boot keys are cleared, so it
would not check the maker-signed shim.
  - To use Secure Boot through shim, restore the factory keys in the firmware
    setup (Secure Boot menu, often "Restore Factory Keys" or "Install default
    keys") and leave Secure Boot off. After the install, run
    gjallarctl secure-boot setup.
  - gjallarOS's own keys need tested firmware steps for this model, which
    gjallarOS does not have yet.
`

// unknownPKOwnerQuestion is the Plane 388 question on the shim path when the
// firmware exposes no PKDefault efivar, so its PK cannot be confirmed as the
// factory key by comparison, and ODDC has no factory list for the model.
const unknownPKOwnerQuestion = "Unknown PK owner: this firmware does not say whether its Secure Boot owner key is the factory key (no PKDefault to compare against), and ODDC does not know this model yet.\n" +
	"gjallarOS only adds its own key to shim's MOK list and leaves the firmware keys untouched.\n" +
	"If this computer belongs to a company, or someone else set its keys, say no.\n" +
	"Yes: install now with the gjallarOS shim. No: stop before anything is written and wait until ODDC knows this model.\n" +
	"Continue with the gjallarOS shim now?"

// notFactoryPKQuestion (Plane 391) is asked when ODDC knows this model's
// factory Platform Keys and the active one is not among them.
const notFactoryPKQuestion = "Not the factory key: ODDC knows the factory Secure Boot owner keys of this model, and the key in this firmware is NOT one of them. Someone replaced it, for example a company, a previous owner or you.\n" +
	"gjallarOS only adds its own key to shim's MOK list and leaves the firmware keys untouched.\n" +
	"If this computer belongs to a company, or you did not set these keys yourself, say no.\n" +
	"Yes: install now with the gjallarOS shim. No: stop before anything is written.\n" +
	"Continue with the gjallarOS shim now?"

// untestedShimQuestion (Plane 391) is asked when ODDC proves the factory
// key but marks the shim path on this model as experimental.
const untestedShimQuestion = "ODDC confirms this is the factory key, but installing through the gjallarOS shim has not been tested on this model yet.\n" +
	"The firmware keys are not touched.\n" +
	"Continue with the gjallarOS shim now?"

// plane388Classify runs the ownership classification on the shim path before
// any disk work. On firmware without a PKDefault efivar it returns the facts
// PrepareShim checks later:
//   - ODDC factory list names the active PK, ownership verified: no question.
//   - same, ownership experimental: one untested-model question.
//   - ODDC factory list known, active PK not on it: the not-factory question.
//   - no usable ODDC entry: the unknown-owner question.
//
// Every refusal comes here, before anything is written. The answer comes only
// from this root-owned installer flow, never from an answers file; unattended
// runs get no answer, which is N.
func plane388Classify(ctx context.Context, ui prompt.UI, out io.Writer, insp secureboot.Inspection, own oddc.SecureBootOwnership, shimStarted, unattended bool) (secureboot.ShimOwnerFacts, error) {
	if insp.State != secureboot.StateForeignManaged || insp.PKDefaultPresent {
		return secureboot.ShimOwnerFacts{}, nil
	}
	const noDefault = "the firmware exposes no PKDefault, so its Platform Key cannot be confirmed as the factory key"
	if insp.PKSHA256 == "" {
		return secureboot.ShimOwnerFacts{}, errors.New(noDefault + ", and its Platform Key could not be read; refusing the shim path before anything is written")
	}
	if insp.SecureBoot && !shimStarted {
		return secureboot.ShimOwnerFacts{}, errors.New(noDefault + "; Secure Boot is on, but this system does not boot through shim; refusing the shim path before anything is written")
	}
	if own.Note != "" {
		fmt.Fprintln(out, own.Note)
	}
	if own.MatchesFactoryPK(insp.PKSHA256, insp.PKDefaultPresent) {
		factory := secureboot.ShimOwnerFacts{FactoryPKFingerprints: own.FactoryPKFingerprints}
		if own.Verified() {
			fmt.Fprintln(out, "ODDC confirms the active Platform Key is this model's factory key, and the gjallarOS shim is tested on this model.")
			return factory, nil
		}
		if unattended {
			return secureboot.ShimOwnerFacts{}, errors.New("ODDC confirms the factory key, but the gjallarOS shim is untested on this model, which needs an explicit yes, and this run is unattended; refusing before anything is written")
		}
		ok, err := ui.Confirm(ctx, untestedShimQuestion, false)
		if err != nil {
			return secureboot.ShimOwnerFacts{}, err
		}
		if !ok {
			return secureboot.ShimOwnerFacts{}, errors.New("the gjallarOS shim is untested on this model; stopped before anything was written")
		}
		fmt.Fprintln(out, "Recorded for this run: factory key confirmed by ODDC, untested model accepted. The firmware keys are not touched; the gjallarOS key only goes into shim's MOK list.")
		return factory, nil
	}
	notFactory := own.Present()
	reason := noDefault
	question := unknownPKOwnerQuestion
	if notFactory {
		reason = "the active Platform Key is not one of the factory keys ODDC knows for this model"
		question = notFactoryPKQuestion
	}
	if !insp.SecureBoot {
		return secureboot.ShimOwnerFacts{}, errors.New(reason + "; with Secure Boot off, shim cannot show that the firmware trusts it; turn Secure Boot on, start the stick through shim and run this again")
	}
	if unattended {
		return secureboot.ShimOwnerFacts{}, errors.New(reason + "; the shim path needs an explicit yes and this run is unattended; refusing before anything is written")
	}
	ok, err := ui.Confirm(ctx, question, false)
	if err != nil {
		return secureboot.ShimOwnerFacts{}, err
	}
	if !ok {
		if notFactory {
			return secureboot.ShimOwnerFacts{}, errors.New(reason + "; stopped before anything was written")
		}
		return secureboot.ShimOwnerFacts{}, errors.New(reason + "; stopped before anything was written; run the installer again once ODDC knows this model's factory key")
	}
	if notFactory {
		fmt.Fprintln(out, "Recorded for this run: a Platform Key that is not the factory key accepted. The firmware keys are not touched; the gjallarOS key only goes into shim's MOK list.")
	} else {
		fmt.Fprintln(out, "Recorded for this run: unknown PK owner accepted. The firmware keys are not touched; the gjallarOS key only goes into shim's MOK list.")
	}
	return secureboot.ShimOwnerFacts{AcceptedUnknownPKSHA256: insp.PKSHA256}, nil
}

// askFirmwareLocked asks whether the firmware setup is locked, when the
// firmware enforces Secure Boot outside Setup Mode and its setup may have a
// password. A password the kernel reports set makes "locked" the default;
// the user may still know it.
func askFirmwareLocked(ctx context.Context, ui prompt.UI, t secureboot.FirmwareTrust) (bool, error) {
	if t.SecureBoot != secureboot.FlagOn || !t.MayBeLocked() {
		return false, nil
	}
	question := "Is the firmware setup (BIOS) locked with a password you do not know?"
	if t.AdminPassword == secureboot.PasswordSet {
		question = "The firmware setup (BIOS) has a password. Is it locked for you, because you do not know that password?"
	}
	return ui.Confirm(ctx, question, t.AdminPassword == secureboot.PasswordSet)
}

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/deploy"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

// runLUKSChip switches security chip unlock back on (L47). It runs as the
// user: the settings go through the settings service like any other
// change, and the next start enrolls the chip with the recovery key.
func runLUKSChip(ctx context.Context, in io.Reader, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	record, err := readLUKSRecord(luksRecordPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fail(errors.New("this system keeps no record of its disk protection; see: gjallarctl luks status"))
	case err != nil:
		return fail(err)
	case !record.Encrypted:
		return fail(errors.New("the system disk is not encrypted. Nothing was changed"))
	case record.TPM2 > 0 && !record.ChipNoPolicy:
		fmt.Fprintln(stdout, "Security chip unlock is already on.")
		return 0
	}

	repo, err := installercheck.DiscoverRepository("")
	if err != nil {
		return fail(fmt.Errorf("resolve gjallarOS repository: %w", err))
	}
	configPath := filepath.Join(repo, "user.config.json")
	user, err := config.Load(configPath)
	if err != nil {
		return fail(err)
	}
	restart := func(pin bool) string {
		if pin {
			return "restart: the next start asks for your recovery key once and for a new startup PIN, and sets up chip unlock."
		}
		return "restart: the next start asks for your recovery key once and sets up chip unlock."
	}
	switch {
	case !user.SecureBootEnable:
		return fail(errors.New("Secure Boot is off, and the security chip unlocks the disk only with Secure Boot on. Set it up with: gjallarctl secure-boot setup"))
	case user.LUKSTPM2Enable:
		fmt.Fprintln(stdout, "Security chip unlock is on; "+restart(user.LUKSTPM2PIN))
		return 0
	case !diskcrypto.TPMAvailable():
		return fail(errors.New("this device has no usable TPM 2.0 security chip (see: systemd-analyze has-tpm2). Nothing was changed"))
	}

	kind := diskcrypto.ModelTPMKind()
	fmt.Fprint(stdout, diskcrypto.StartupUnlockText(kind))
	fmt.Fprintln(stdout, "\n  Either way the recovery key keeps working, for when the chip cannot unlock the disk.")
	ui := prompt.UI{Reader: bufio.NewReader(in), Out: stdout}
	// A PIN chosen before chip unlock was switched off is the default again.
	choice, err := ui.Choice(ctx, "Startup unlock", diskcrypto.DefaultStartupUnlock(kind, user.LUKSTPM2PIN), diskcrypto.StartupUnlockChoices())
	if err != nil {
		return fail(err)
	}
	pin := choice == diskcrypto.StartupUnlockPIN

	if onSystemCheckout(configPath) {
		// Root writes the system checkout's settings; both are critical, so
		// the change may wait before it applies.
		if code := askSystemSettings(map[string]any{"luksTpm2Enable": true, "luksTpm2Pin": pin}, stdout, stderr); code != 0 {
			return code
		}
		if user, err = config.Load(configPath); err != nil {
			return fail(err)
		}
		if !user.LUKSTPM2Enable {
			fmt.Fprintln(stdout, "Once the change is applied and the rebuild is done, "+restart(pin))
			return 0
		}
		fmt.Fprintln(stdout, "Chip unlock is on. Now "+restart(pin))
		return 0
	}

	// A development checkout is written directly and rebuilt.
	for _, set := range []struct {
		key   string
		value bool
	}{{"luksTpm2Pin", pin}, {"luksTpm2Enable", true}} {
		if err := config.SetBool(configPath, set.key, set.value); err != nil {
			return fail(err)
		}
	}
	if err := deploy.Apply(ctx, repo, user.Hostname); err != nil {
		// The running system and its config stay in step.
		for _, set := range []struct {
			key   string
			value bool
		}{{"luksTpm2Enable", false}, {"luksTpm2Pin", user.LUKSTPM2PIN}} {
			if revert := config.SetBool(configPath, set.key, set.value); revert != nil {
				fmt.Fprintf(stderr, "ERROR: set %s back: %v\n", set.key, revert)
			}
		}
		return fail(err)
	}
	fmt.Fprintln(stdout, "Chip unlock is on. Now "+restart(pin))
	return 0
}

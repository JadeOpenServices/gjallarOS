package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

const (
	pcrlockPolicyPath = "/var/lib/systemd/pcrlock.json"
	// Written by the first-boot enrollment; kept in step when it exists.
	tpm2KeyslotRecordPath = "/var/lib/gjallarOS/luks/keyslots.json"
)

// runLUKSPIN sets, changes or removes the startup PIN. The security chip
// gets a new token next to the old one; the old one goes only after the
// new one opened the disk, so a failure leaves the disk as it was. The
// exception is a PIN change under the same policy: systemd-cryptenroll
// then drops the old slot right after enrolling the new one.
func runLUKSPIN(ctx context.Context, device string, before diskcrypto.Keyslots, remove bool, stdout, stderr io.Writer) int {
	if len(before.TPM2) == 0 {
		fmt.Fprintln(stderr, "The security chip does not unlock this disk, so there is no startup PIN to change.")
		return 1
	}
	if remove && !before.TPM2PIN {
		fmt.Fprintln(stdout, "There is no startup PIN.")
		return 0
	}
	if !diskcrypto.TPMAvailable() {
		fmt.Fprintln(stderr, "ERROR: the security chip is not available.")
		return 1
	}
	if _, err := os.Stat(pcrlockPolicyPath); err != nil {
		fmt.Fprintf(stderr, "ERROR: no security chip policy at %s. Run sudo gjallarctl tpm2 reenroll first.\n", pcrlockPolicyPath)
		return 1
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: changing the startup PIN needs a terminal: %v\n", err)
		return 1
	}
	defer tty.Close()
	s := &luksSession{ctx: ctx, device: device, tty: tty, reader: bufio.NewReader(tty), signals: make(chan os.Signal, 1)}
	signal.Notify(s.signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(s.signals)

	pin, err := s.choosePIN(remove)
	if errors.Is(err, errCancelled) || errors.Is(err, credential.ErrInterrupted) {
		fmt.Fprintln(tty, "Cancelled. Nothing was changed.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\nNothing was changed.\n", err)
		return 1
	}
	withPIN := "no"
	if !remove {
		withPIN = "yes"
	}
	_, err = s.unlocked(before, []string{"NEWPIN=" + pin},
		"--tpm2-device=auto", "--tpm2-pcrlock="+pcrlockPolicyPath, "--tpm2-with-pin="+withPIN)
	if errors.Is(err, errCancelled) || errors.Is(err, credential.ErrInterrupted) {
		fmt.Fprintln(tty, "Cancelled. Nothing was changed.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\nNothing was changed.\n", err)
		return 1
	}

	after, err := diskcrypto.ReadKeyslots(ctx, device)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	var added []int
	for _, n := range after.TPM2 {
		if !containsInt(before.TPM2, n) {
			added = append(added, n)
		}
	}
	if len(added) != 1 {
		fmt.Fprintf(stderr, "ERROR: expected one new security chip keyslot, found %v\n", added)
		return 1
	}
	slot := added[0]
	// systemd-cryptenroll wipes the old slot itself when only the PIN
	// changes under the same policy, so only the ones still there are ours.
	var old []string
	for _, n := range before.TPM2 {
		if containsInt(after.TPM2, n) {
			old = append(old, strconv.Itoa(n))
		}
	}
	err = diskcrypto.TestTPM2Token(ctx, device, after.TPM2Token[slot], pin)
	pin = ""
	if err != nil && len(old) == 0 {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		fmt.Fprintln(stderr, "The new PIN is set, but the check of it failed. After several wrong PINs the security chip refuses PINs for a while. If the next startup does not take the new PIN, use the recovery key.")
		return 1
	}
	if err != nil {
		if _, werr := s.cryptenroll(nil, "--wipe-slot="+strconv.Itoa(slot)); werr != nil {
			fmt.Fprintf(stderr, "ERROR: %v\nRemoving the new keyslot %d failed: %v\n", err, slot, werr)
			return 1
		}
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		fmt.Fprintln(stderr, "The new setting did not open the disk, so the old one stays. After several wrong PINs the security chip refuses PINs for a while; try again later.")
		return 1
	}

	if len(old) > 0 {
		if _, err := s.cryptenroll(nil, "--wipe-slot="+strings.Join(old, ",")); err != nil {
			fmt.Fprintf(stderr, "ERROR: removing the old security chip keyslot failed: %v\nBoth work for now; run this command again.\n", err)
			return 1
		}
	}
	if _, err := os.Stat(tpm2KeyslotRecordPath); err == nil {
		if err := diskcrypto.RefreshTPM2KeyslotRecord(ctx, device, tpm2KeyslotRecordPath, pcrlockPolicyPath); err != nil {
			fmt.Fprintf(stderr, "WARNING: %v\n", err)
		}
	}
	switch {
	case remove:
		fmt.Fprintln(tty, "Startup PIN removed. From the next startup the disk unlocks without it.")
	case before.TPM2PIN:
		fmt.Fprintln(tty, "Startup PIN changed. The next startup asks for the new one.")
	default:
		fmt.Fprintln(tty, "Startup PIN set. The next startup asks for it.")
	}
	return 0
}

// choosePIN asks for the new PIN, or for consent to drop it.
func (s *luksSession) choosePIN(remove bool) (string, error) {
	if !remove {
		return askNewPIN(s.tty, s.reader)
	}
	fmt.Fprintln(s.tty, s.wrap("Without a startup PIN, the security chip unlocks the disk by itself whenever this device starts unchanged. Whoever holds the device then gets to the login screen, and the disk is only as safe as your login."))
	answer, err := s.readLine("Remove the startup PIN? Type yes: ")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(answer) != "yes" {
		return "", errCancelled
	}
	return "", nil
}

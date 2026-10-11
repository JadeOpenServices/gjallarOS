package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

const luksUsage = "Usage: gjallarctl luks {status|recovery-key|setup}"

// runLUKSLifecycle manages how the root disk unlocks. Reading or changing
// the LUKS header needs root, so it runs itself again through sudo.
func runLUKSLifecycle(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "status" && args[0] != "recovery-key" && args[0] != "setup" {
		fmt.Fprintln(stderr, luksUsage)
		return 2
	}
	if os.Geteuid() != 0 {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		sudo, err := exec.LookPath("sudo")
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		// -k: ask now and leave no timestamp behind.
		err = syscall.Exec(sudo, []string{"sudo", "-k", self, "luks", args[0]}, os.Environ())
		fmt.Fprintf(stderr, "ERROR: sudo: %v\n", err)
		return 1
	}
	ctx := context.Background()
	device, err := diskcrypto.RootDevice(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if device == "" {
		fmt.Fprintln(stdout, "The system disk is not encrypted.")
		return 0
	}
	slots, err := diskcrypto.ReadKeyslots(ctx, device)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	switch args[0] {
	case "status":
		printLUKSStatus(stdout, device, slots)
		return 0
	case "setup":
		return runLUKSSetup(ctx, device, slots, stderr)
	}
	return runRecoveryKey(ctx, device, slots, stderr)
}

func printLUKSStatus(out io.Writer, device string, slots diskcrypto.Keyslots) {
	chip := "off"
	if len(slots.TPM2) > 0 {
		chip = "on"
		if slots.TPM2PIN {
			chip = "on, with startup PIN"
		}
	}
	recovery := "not set up"
	if len(slots.Recovery) > 0 {
		recovery = "set up"
	}
	fmt.Fprintf(out, "System disk:   %s\n", device)
	fmt.Fprintf(out, "Security chip: %s\n", chip)
	fmt.Fprintf(out, "Recovery key:  %s\n", recovery)
	fmt.Fprintf(out, "Passphrases:   %d\n", len(slots.Passphrase))
	switch {
	case len(slots.Recovery) == 0:
		fmt.Fprintln(out, "\nCreate a recovery key: gjallarctl luks recovery-key")
	case len(slots.TPM2) > 0 && len(slots.Passphrase) > 0:
		fmt.Fprintln(out, "\nRemove the install passphrase: gjallarctl luks setup")
	}
}

// runLUKSSetup finishes disk protection once the security chip unlocks the
// disk: it creates a recovery key when there is none, then removes the
// passphrase typed at install, so the recovery key is the only fallback.
func runLUKSSetup(ctx context.Context, device string, slots diskcrypto.Keyslots, stderr io.Writer) int {
	if len(slots.TPM2) == 0 {
		fmt.Fprintln(stderr, "The security chip does not unlock this disk; the passphrase stays.")
		return 1
	}
	if len(slots.Recovery) == 0 {
		if code := runRecoveryKey(ctx, device, slots, stderr); code != 0 {
			return code
		}
		var err error
		if slots, err = diskcrypto.ReadKeyslots(ctx, device); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
	}
	if len(slots.Passphrase) == 0 {
		return 0
	}
	if len(slots.Recovery) == 0 {
		fmt.Fprintln(stderr, "ERROR: no recovery key; the passphrase stays.")
		return 1
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: removing the passphrase needs a terminal: %v\n", err)
		return 1
	}
	defer tty.Close()
	s := &luksSession{ctx: ctx, device: device, tty: tty, reader: bufio.NewReader(tty), signals: make(chan os.Signal, 1)}
	signal.Notify(s.signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(s.signals)

	// Only a chip that opens the disk right now may become the way in.
	if err := diskcrypto.TestTPM2(ctx, device, tty); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\nThe passphrase stays until the security chip unlocks the disk.\n", err)
		return 1
	}
	old := make([]string, len(slots.Passphrase))
	for i, n := range slots.Passphrase {
		old[i] = strconv.Itoa(n)
	}
	if _, err := s.cryptenroll(nil, "--wipe-slot="+strings.Join(old, ",")); err != nil {
		fmt.Fprintf(stderr, "ERROR: removing the passphrase failed: %v\n", err)
		return 1
	}
	after, err := diskcrypto.ReadKeyslots(ctx, device)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if len(after.Passphrase) > 0 || len(after.TPM2) == 0 || len(after.Recovery) == 0 {
		fmt.Fprintf(stderr, "ERROR: unexpected keyslots after removing the passphrase: %+v\n", after)
		return 1
	}
	fmt.Fprintln(tty, s.wrap("The install passphrase was removed. The security chip unlocks the disk at startup. If it ever refuses, for example after a firmware update, type your recovery key instead."))
	fmt.Fprintln(tty)
	_, _ = s.readLine("Press Enter to continue. ")
	return 0
}

var errCancelled = errors.New("cancelled")

type luksSession struct {
	ctx     context.Context
	device  string
	tty     *os.File
	reader  *bufio.Reader
	signals chan os.Signal
}

// readLine reads one visible line; a signal or EOF cancels.
func (s *luksSession) readLine(prompt string) (string, error) {
	fmt.Fprint(s.tty, prompt)
	done := make(chan string, 1)
	go func() {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			close(done)
			return
		}
		done <- line
	}()
	select {
	case line, ok := <-done:
		if !ok {
			return "", errCancelled
		}
		return strings.TrimSpace(line), nil
	case <-s.signals:
		fmt.Fprintln(s.tty)
		return "", errCancelled
	}
}

func (s *luksSession) interrupted() bool {
	select {
	case <-s.signals:
		return true
	default:
		return false
	}
}

// wrap breaks text at spaces to fit the terminal.
func (s *luksSession) wrap(text string) string {
	return wrapWords(text, terminalWidth(s.tty)-1)
}

// clear wipes the screen and its scrollback, so the key does not stay
// readable above the prompt.
func (s *luksSession) clear() {
	fmt.Fprint(s.tty, "\033[H\033[2J\033[3J")
}

// cryptenroll runs systemd-cryptenroll in its own process group, so
// Ctrl-C reaches only this program and never stops a header write halfway.
func (s *luksSession) cryptenroll(env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(s.ctx, "systemd-cryptenroll", append(args, s.device)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("systemd-cryptenroll: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func runRecoveryKey(ctx context.Context, device string, before diskcrypto.Keyslots, stderr io.Writer) int {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: a recovery key needs a terminal: %v\n", err)
		return 1
	}
	defer tty.Close()
	s := &luksSession{ctx: ctx, device: device, tty: tty, reader: bufio.NewReader(tty), signals: make(chan os.Signal, 1)}
	signal.Notify(s.signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(s.signals)

	key, slot, err := s.enroll(before)
	if errors.Is(err, errCancelled) || errors.Is(err, credential.ErrInterrupted) {
		fmt.Fprintln(tty, "Cancelled. Nothing was changed.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\nNothing was changed.\n", err)
		return 1
	}

	err = s.confirm(key)
	key = ""
	if err != nil {
		s.clear()
		if _, werr := s.cryptenroll(nil, "--wipe-slot="+strconv.Itoa(slot)); werr != nil {
			fmt.Fprintf(stderr, "ERROR: removing the unconfirmed key failed: %v\nRun gjallarctl luks recovery-key again; it replaces it.\n", werr)
			return 1
		}
		fmt.Fprintln(tty, "Cancelled. The new key was removed; your old unlock methods still work.")
		return 1
	}

	s.clear()
	if len(before.Recovery) > 0 {
		old := make([]string, len(before.Recovery))
		for i, n := range before.Recovery {
			old[i] = strconv.Itoa(n)
		}
		if _, err := s.cryptenroll(nil, "--wipe-slot="+strings.Join(old, ",")); err != nil {
			fmt.Fprintf(stderr, "ERROR: the new recovery key works, but removing the old one failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(tty, "Recovery key confirmed. The old recovery key no longer works.")
		return 0
	}
	fmt.Fprintln(tty, "Recovery key confirmed.")
	return 0
}

// enroll adds a systemd recovery key and returns it with its keyslot.
// The security chip unlocks the header when it can; otherwise the user
// types a passphrase or the current recovery key.
func (s *luksSession) enroll(before diskcrypto.Keyslots) (string, int, error) {
	var out string
	var err error
	if len(before.TPM2) > 0 && diskcrypto.TPMAvailable() {
		var env []string
		if before.TPM2PIN {
			pin, perr := credential.ReadSecret(s.tty, s.reader, s.tty, "Startup PIN: ")
			if perr != nil {
				return "", 0, perr
			}
			env = []string{"PIN=" + pin}
		}
		out, err = s.cryptenroll(env, "--unlock-tpm2-device=auto", "--recovery-key")
		if err != nil {
			fmt.Fprintln(s.tty, s.wrap("The security chip did not unlock the disk."))
		}
	}
	for attempt := 1; out == ""; attempt++ {
		secret, rerr := credential.ReadSecret(s.tty, s.reader, s.tty, "Disk passphrase or current recovery key: ")
		if rerr != nil {
			return "", 0, rerr
		}
		if secret == "" {
			return "", 0, errCancelled
		}
		path, cleanup, kerr := diskcrypto.SecretFile(secret)
		secret = ""
		if kerr != nil {
			return "", 0, kerr
		}
		out, err = s.cryptenroll(nil, "--unlock-key-file="+path, "--recovery-key")
		cleanup()
		if err == nil {
			break
		}
		if attempt == 3 {
			return "", 0, fmt.Errorf("that passphrase or key does not unlock %s", s.device)
		}
		fmt.Fprintln(s.tty, "That does not unlock the disk. Try again.")
	}

	after, err := diskcrypto.ReadKeyslots(s.ctx, s.device)
	if err != nil {
		return "", 0, err
	}
	var added []int
	for _, n := range after.Recovery {
		if !containsInt(before.Recovery, n) {
			added = append(added, n)
		}
	}
	if len(added) != 1 {
		return "", 0, fmt.Errorf("expected one new recovery keyslot, found %v", added)
	}
	key := strings.TrimSpace(out)
	if _, err := diskcrypto.RecoveryKeyBlocks(key); err != nil {
		_, _ = s.cryptenroll(nil, "--wipe-slot="+strconv.Itoa(added[0]))
		return "", 0, err
	}
	if err := diskcrypto.TestKeyslot(s.ctx, s.device, added[0], key); err != nil {
		_, _ = s.cryptenroll(nil, "--wipe-slot="+strconv.Itoa(added[0]))
		return "", 0, err
	}
	if s.interrupted() {
		_, _ = s.cryptenroll(nil, "--wipe-slot="+strconv.Itoa(added[0]))
		return "", 0, errCancelled
	}
	return key, added[0], nil
}

// confirm shows the key, hides it, and has the user type back three
// random blocks, so a key that was not written down never replaces the
// old one.
func (s *luksSession) confirm(key string) error {
	blocks, err := diskcrypto.RecoveryKeyBlocks(key)
	if err != nil {
		return err
	}
	for {
		s.clear()
		fmt.Fprintf(s.tty, "Your new recovery key\n\n    %s\n    %s\n\n",
			strings.Join(blocks[:4], "-"), strings.Join(blocks[4:], "-"))
		fmt.Fprintln(s.tty, s.wrap("It unlocks this disk when nothing else can, for example after a firmware change. Write it on paper or keep it in a password manager on another device. GjallarOS does not keep a copy."))
		fmt.Fprintln(s.tty)
		if _, err := s.readLine(s.wrap("Press Enter when it is written down.") + " "); err != nil {
			return err
		}
		s.clear()
		ok, err := s.checkBlocks(blocks)
		if err != nil || ok {
			return err
		}
	}
}

// checkBlocks returns false when the user wants to see the key again.
func (s *luksSession) checkBlocks(blocks []string) (bool, error) {
	picks, err := diskcrypto.Challenge()
	if err != nil {
		return false, err
	}
	fmt.Fprintln(s.tty, s.wrap("Check your copy. Type the block marked with ^ from your written key, in small letters."))
	for _, i := range picks {
		for {
			mask := make([]string, len(blocks))
			for j := range blocks {
				mask[j] = "........"
				if j == i {
					mask[j] = "^^^^^^^^"
				}
			}
			fmt.Fprintf(s.tty, "\n    %s\n    %s\n", strings.Join(mask[:4], "-"), strings.Join(mask[4:], "-"))
			typed, err := s.readLine(fmt.Sprintf("Block %d of %d: ", i+1, len(blocks)))
			if err != nil {
				return false, err
			}
			if diskcrypto.SameBlock(blocks[i], typed) {
				break
			}
			answer, err := s.readLine(s.wrap("That does not match. Try again, show the key again, or cancel? [t/s/c]") + " ")
			if err != nil {
				return false, err
			}
			switch strings.ToLower(answer) {
			case "s":
				return false, nil
			case "c":
				return false, errCancelled
			}
		}
	}
	return true, nil
}

func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

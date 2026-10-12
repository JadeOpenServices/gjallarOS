package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddcvalidation"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

// luksRecordPath holds how many keyslots of each kind the system disk has,
// written by root (gjallar-luks-record.service and every keyslot change
// through gjallarctl luks), so the reminder needs no root. Counts only:
// nothing in it helps to unlock the disk.
const luksRecordPath = "/run/gjallar-luks/keyslots"

type luksRecord struct {
	Encrypted                  bool
	TPM2, Recovery, Passphrase int
}

func newLUKSRecord(s diskcrypto.Keyslots) luksRecord {
	return luksRecord{Encrypted: true, TPM2: len(s.TPM2), Recovery: len(s.Recovery), Passphrase: len(s.Passphrase)}
}

func writeLUKSRecord(path string, r luksRecord) error {
	encrypted := "no"
	if r.Encrypted {
		encrypted = "yes"
	}
	data := fmt.Sprintf("encrypted=%s\ntpm2=%d\nrecovery=%d\npassphrase=%d\n", encrypted, r.TPM2, r.Recovery, r.Passphrase)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keyslots-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readLUKSRecord(path string) (luksRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return luksRecord{}, err
	}
	var r luksRecord
	seen := 0
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "encrypted" {
			r.Encrypted = value == "yes"
			seen++
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return luksRecord{}, fmt.Errorf("%s: bad %s", path, key)
		}
		switch key {
		case "tpm2":
			r.TPM2 = n
		case "recovery":
			r.Recovery = n
		case "passphrase":
			r.Passphrase = n
		default:
			continue
		}
		seen++
	}
	if seen != 4 {
		return luksRecord{}, fmt.Errorf("%s is incomplete", path)
	}
	return r, nil
}

// Disk protection states, also the words the bar widget reads.
const (
	diskProtectionDone        = "done"
	diskProtectionSetup       = "setup"
	diskProtectionSecureBoot  = "secure-boot"
	diskProtectionRecoveryKey = "recovery-key"
	diskProtectionUnencrypted = "unencrypted"
)

// diskProtectionState says what is missing. chipPossible reports whether
// the security chip may unlock this disk once Secure Boot is on; it is only
// asked when the chip does not unlock it yet.
func diskProtectionState(r luksRecord, chipPossible func() bool) string {
	switch {
	case !r.Encrypted:
		return diskProtectionUnencrypted
	case r.TPM2 > 0:
		if r.Recovery > 0 && r.Passphrase == 0 {
			return diskProtectionDone
		}
		return diskProtectionSetup
	case chipPossible():
		return diskProtectionSecureBoot
	case r.Recovery > 0:
		// Models without a trusted Secure Boot setup keep the passphrase
		// for startup; the recovery key is the way back when it is lost.
		return diskProtectionDone
	}
	return diskProtectionRecoveryKey
}

// chipUnlockPossible: the chip unlocks the disk only with Secure Boot on
// and on models whose firmware ODDC knows (gjallarctl secure-boot setup
// asks the same). Anything unknown counts as no, so a generic model is
// never sent into a setup that refuses it.
func chipUnlockPossible() bool {
	repo, err := installercheck.DiscoverRepository("")
	if err != nil {
		return false
	}
	user, err := config.Load(filepath.Join(repo, "user.config.json"))
	if err != nil || !user.LUKSTPM2Enable {
		return false
	}
	if user.SecureBootEnable {
		return true
	}
	device, err := oddcvalidation.LoadDeviceContext(repo)
	if err != nil {
		return false
	}
	firmware, err := oddc.ResolveSecureBootFirmwarePolicy(device.Resolved)
	return err == nil && device.Resolved.ModelID != "" && firmware.SourceEntity != "" && firmware.Policy.Supported
}

type diskProtectionText struct {
	summary, body, risk string
	command             []string
}

func diskProtectionTexts(state string, r luksRecord) diskProtectionText {
	switch state {
	case diskProtectionSetup:
		t := diskProtectionText{
			summary: "Disk protection not finished",
			risk:    "the passphrase typed at install keeps working, and without a recovery key a firmware update can lock you out of your files",
			command: []string{"luks", "setup"},
		}
		switch {
		case r.Recovery == 0 && r.Passphrase > 0:
			t.body = "Your install passphrase still works and there is no recovery key yet."
		case r.Recovery == 0:
			t.body = "No recovery key. After a firmware update it can be the only way in."
			t.risk = "without a recovery key a firmware update can lock you out of your files"
		default:
			t.body = "The passphrase typed at install still unlocks your disk."
			t.risk = "anyone who learns the install passphrase can unlock your disk"
		}
		return t
	case diskProtectionSecureBoot:
		return diskProtectionText{
			summary: "Disk protection not set up",
			body:    "Secure Boot is off, so the security chip cannot guard your disk.",
			risk:    "the security chip does not check the startup files, so changed boot files go unnoticed",
			command: []string{"secure-boot", "setup"},
		}
	case diskProtectionRecoveryKey:
		return diskProtectionText{
			summary: "No recovery key",
			body:    "Forget your passphrase and your files are lost.",
			risk:    "a forgotten passphrase means your files are lost",
			command: []string{"luks", "recovery-key"},
		}
	case diskProtectionUnencrypted:
		return diskProtectionText{
			summary: "System disk not encrypted",
			body:    "Anyone with this device can read your files. Only a new install fixes it.",
			risk:    "anyone who takes this device can read your files",
		}
	}
	return diskProtectionText{}
}

// runLUKSRemind is the login reminder (user unit disk-protection-reminder).
// It writes the state for the bar widget and, until protection is done,
// shows a card with Set up, Later and, on standalone installs, Don't ask.
// The labels stay short: Noctalia puts a button that does not fit onto its
// own full-width row, where it looks like the main choice. --open runs the
// setup right away (the bar widget's click); --refresh only updates the
// state (after a keyslot change).
func runLUKSRemind(args []string, stderr io.Writer) int {
	mode := ""
	if len(args) > 1 || len(args) == 1 && args[0] != "--open" && args[0] != "--refresh" {
		fmt.Fprintln(stderr, luksUsage)
		return 2
	}
	if len(args) == 1 {
		mode = args[0]
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "ERROR: gjallarctl luks remind runs as the desktop user.")
		return 1
	}
	record, err := readLUKSRecord(luksRecordPath)
	if errors.Is(err, os.ErrNotExist) {
		// Systems without gjallar-luks-record.service: nothing to report.
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	state := diskProtectionState(record, chipUnlockPossible)
	if err := writeDiskProtectionState(state); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	text := diskProtectionTexts(state, record)
	switch {
	case mode == "--refresh" || state == diskProtectionDone:
		return 0
	case mode == "--open":
		if text.command == nil {
			return 0
		}
		return runDiskProtectionSetup(text.command, stderr)
	}

	dismissed := dismissedPath()
	if previous, err := os.ReadFile(dismissed); err == nil && strings.TrimSpace(string(previous)) == state {
		return 0
	}
	actions := []noticeAction{}
	if text.command != nil {
		actions = append(actions, noticeAction{"open", "Set up"})
	}
	actions = append(actions, noticeAction{"later", "Later"})
	if os.Getenv("GJALLAR_MANAGED_DEVICE") != "1" {
		actions = append(actions, noticeAction{"never", "Don't ask"})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	choice, err := showNotice(ctx, desktopNotice{
		App:     "GjallarOS",
		Icon:    "security-medium",
		Summary: text.summary,
		Body:    text.body,
		Actions: actions,
	})
	if err != nil {
		if ctx.Err() != nil {
			return 0
		}
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	switch choice {
	case "open":
		return runDiskProtectionSetup(text.command, stderr)
	case "never":
		if os.Getenv("GJALLAR_MANAGED_DEVICE") == "1" {
			return 0
		}
		question := "Stop asking at login?\n\nWithout it, " + text.risk + "."
		if text.command != nil {
			question += "\n\nThe shield in the bar stays until setup is done, and a click on it starts setup."
		}
		err := exec.CommandContext(ctx, "zenity", "--question", "--icon=dialog-warning",
			"--title=Disk protection", "--width=420", "--no-markup", "--text="+question,
			"--ok-label=Stop asking", "--cancel-label=Keep asking").Run()
		if err != nil {
			return 0
		}
		if err := os.MkdirAll(filepath.Dir(dismissed), 0o700); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		// The state is kept with it: a different gap asks again.
		if err := os.WriteFile(dismissed, []byte(state+"\n"), 0o600); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
	}
	return 0
}

// runDiskProtectionSetup opens a terminal with the setup command, waits for
// it, then updates the state, so the bar follows at once.
func runDiskProtectionSetup(command []string, stderr io.Writer) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	// The command's output (next steps, errors) stays readable until Enter.
	script := `"$@"; printf '\nPress Enter to close. '; read -r _`
	args := append([]string{"--title=GjallarOS disk protection", "-e", "sh", "-c", script, "sh", self}, command...)
	if err := exec.Command("ghostty", args...).Run(); err != nil {
		fmt.Fprintf(stderr, "ERROR: terminal: %v\n", err)
		return 1
	}
	return runLUKSRemind([]string{"--refresh"}, stderr)
}

func runtimeStatePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(dir, "gjallar", "disk-protection")
}

func dismissedPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "gjallar", "disk-protection-dismissed")
}

// writeDiskProtectionState leaves one word for the bar widget
// (user/wm/shells/noctalia/plugins/disk-protection).
func writeDiskProtectionState(state string) error {
	path := runtimeStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(state+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

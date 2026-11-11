package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

// A developer channel or developer mode needs the startup PIN or the
// recovery key once, on top of the password (Plane 283). The command reads
// it at the terminal, never from arguments or the environment, and hands
// it to the settings service in the call itself.

// settingsReadProof asks for the proof; tests replace it.
var settingsReadProof = readDeveloperProof

// needsDeveloperProof reports whether values turn on a developer channel or
// developer mode. Values the service cannot read are left to it to refuse.
func needsDeveloperProof(values map[string]json.RawMessage) bool {
	if raw, ok := values["releaseChannel"]; ok {
		var channel string
		if json.Unmarshal(raw, &channel) == nil && devgate.Needs(channel, false).Channels {
			return true
		}
	}
	if raw, ok := values["developerMode"]; ok {
		var on bool
		if json.Unmarshal(raw, &on) == nil && on {
			return true
		}
	}
	return false
}

// settingsDiskEncrypted reports whether the system disk is encrypted;
// tests replace it.
var settingsDiskEncrypted = func(ctx context.Context) (bool, error) {
	device, err := diskcrypto.RootDevice(ctx)
	return device != "", err
}

// unencryptedDisk reports a system disk that is known not to be
// encrypted. Such a disk has no startup PIN or recovery key, so asking for
// one would only end in a refusal (Q217). When the disk cannot be read the
// proof is asked as before, and the service decides.
func unencryptedDisk(ctx context.Context) bool {
	encrypted, err := settingsDiskEncrypted(ctx)
	return err == nil && !encrypted
}

// settingsLUKSRecord reads the keyslot counts root leaves in /run; tests
// replace it.
var settingsLUKSRecord = func() (luksRecord, error) { return readLUKSRecord(luksRecordPath) }

// noProofYet is the refusal for an encrypted disk that has neither a
// recovery key nor a startup PIN (Plane 331, wiki 752 R42).
const noProofYet = "this device has no recovery key yet; run gjallarctl luks recovery-key, then run this again"

// lacksDeveloperProof reports an encrypted disk with nothing that could
// answer the proof prompt: no recovery key, and no chip keyslot with a
// startup PIN. The disk passphrase is not a proof. Without a readable
// record the prompt is asked as before and the service decides; this only
// saves a prompt that cannot be answered.
func lacksDeveloperProof() bool {
	r, err := settingsLUKSRecord()
	if err != nil || !r.Encrypted || r.Recovery > 0 {
		return false
	}
	return r.TPM2 == 0 || r.PINKnown && !r.PIN
}

// refuseUnencrypted says why a developer channel or developer mode is
// refused and, on a device that still runs one, offers stable with
// developer mode off together with the other values asked for. A device
// left on a developer channel from before the refusal could otherwise
// change no setting at all.
func refuseUnencrypted(ctx context.Context, bus settingsBus, values map[string]json.RawMessage, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "ERROR: %s.\n", devgate.Unencrypted)
	running, err := runsDeveloperSetup(ctx, bus)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if !running {
		fmt.Fprintln(stdout, "Nothing changed.")
		return 1
	}
	ui := prompt.UI{Reader: bufio.NewReader(settingsStdin), Out: stdout}
	ok, err := ui.Confirm(ctx, "This device still runs a developer channel or developer mode. Switch to stable with developer mode off?", false)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "Nothing changed. Changes and rebuilds stay refused until the device is on stable.")
		return 1
	}
	stable := maps.Clone(values)
	if stable == nil {
		stable = map[string]json.RawMessage{}
	}
	stable["releaseChannel"] = json.RawMessage(`"stable"`)
	stable["developerMode"] = json.RawMessage(`false`)
	return settingsRequest(ctx, bus, stable, stdout, stderr)
}

// runsDeveloperSetup reports whether the configuration has a developer
// channel or developer mode on.
func runsDeveloperSetup(ctx context.Context, bus settingsBus) (bool, error) {
	var channel string
	var mode bool
	for name, into := range map[string]any{"releaseChannel": &channel, "developerMode": &mode} {
		key, err := settingsKey(ctx, bus, name)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(key.Value, into); err != nil {
			return false, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return !devgate.Needs(channel, mode).Empty(), nil
}

// readDeveloperProof asks for the startup PIN, or the recovery key when
// the PIN is left empty.
func readDeveloperProof(stderr io.Writer) (string, []byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", nil, fmt.Errorf("the startup PIN or the recovery key is typed at a terminal: %w", err)
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)
	fmt.Fprintln(stderr, "A developer channel or developer mode needs the startup PIN or the recovery key of this device.")
	pin, err := credential.ReadSecret(tty, reader, tty, "Startup PIN (Enter to use the recovery key): ")
	if err != nil {
		return "", nil, err
	}
	if pin != "" {
		return devgate.ProofPIN, []byte(pin), nil
	}
	key, err := credential.ReadSecret(tty, reader, tty, "Recovery key: ")
	if err != nil {
		return "", nil, err
	}
	normal, err := normalRecoveryKey(key)
	if err != nil {
		return "", nil, err
	}
	return devgate.ProofRecovery, []byte(normal), nil
}

// normalRecoveryKey takes the recovery key as people type it: any case,
// with spaces or without the dashes, and returns the form the disk
// stores. A key in the wrong form is refused here, so it costs no wait.
func normalRecoveryKey(key string) (string, error) {
	key = strings.ToLower(strings.Join(strings.Fields(key), ""))
	if !strings.Contains(key, "-") && len(key) == 64 {
		var blocks []string
		for i := 0; i < len(key); i += 8 {
			blocks = append(blocks, key[i:i+8])
		}
		key = strings.Join(blocks, "-")
	}
	if _, err := diskcrypto.RecoveryKeyBlocks(key); err != nil {
		return "", errors.New("that is not a recovery key: it has 8 groups of 8 letters, like the one shown when the device was set up")
	}
	return key, nil
}

// settingsConfirmDeveloper writes the grant for a device set to a
// developer channel or developer mode before the gate came.
func settingsConfirmDeveloper(ctx context.Context, bus settingsBus, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, settingsUsage)
		return 2
	}
	if unencryptedDisk(ctx) {
		return refuseUnencrypted(ctx, bus, nil, stdout, stderr)
	}
	if lacksDeveloperProof() {
		fmt.Fprintf(stderr, "ERROR: %s.\n", noProofYet)
		return 1
	}
	kind, secret, err := settingsReadProof(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	defer clear(secret)
	if _, err := bus.Call(ctx, "ConfirmDeveloper", kind, secret); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Confirmed: this device keeps its developer channel or developer mode, and changes and rebuilds work again.")
	return 0
}

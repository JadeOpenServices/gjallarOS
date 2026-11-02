package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/credential"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
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

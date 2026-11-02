package diskcrypto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// keyslotMeasureMessageID is SD_MESSAGE_TPM_NVPCR_EXTEND (systemd
// sd-messages.h). systemd-cryptsetup logs it for every volume it opens
// with tpm2-measure-keyslot-nvpcr= set, after it extended the
// "cryptsetup" NvPCR with which keyslot and which kind of key opened it.
const keyslotMeasureMessageID = "4c2e46d266a747c6ac1460aa54484fa7"

// Unlock methods of this boot, as systemd-cryptsetup measured them.
const (
	UnlockUnknown = ""
	// The TPM2 token opened the disk (with or without startup PIN).
	UnlockChip = "tpm2"
	// A typed key opened it: recovery key or passphrase. systemd measures
	// no mechanism name for those.
	UnlockTyped = "typed"
)

// BootUnlockMethod reports how this boot opened the LUKS volume with
// luksUUID. It reads the journal records systemd-cryptsetup wrote in the
// initrd. No record (NvPCR not anchored, TPM without NV space, kernel image
// not measured, an older systemd) means UnlockUnknown: the caller then says
// nothing rather than guess.
func BootUnlockMethod(ctx context.Context, luksUUID string) (string, error) {
	// _UID and _TRANSPORT are set by journald and cannot be faked by the
	// sender: only root's structured journal records count.
	out, err := exec.CommandContext(ctx, "journalctl", "-b", "0", "-o", "json", "--no-pager", "-q",
		"--output-fields=MEASURING",
		"MESSAGE_ID="+keyslotMeasureMessageID, "NVPCR=cryptsetup", "_UID=0", "_TRANSPORT=journal").Output()
	if err != nil {
		return UnlockUnknown, fmt.Errorf("journalctl: %w", err)
	}
	return ParseBootUnlockMethod(out, luksUUID)
}

// ParseBootUnlockMethod reads journalctl -o json lines. MEASURING is
// "cryptsetup-keyslot:<volume>:<LUKS UUID>:<mechanism>:<keyslot>", the
// volume name escaped so it holds no colon (systemd cryptsetup.c
// measure_keyslot). The last record for the volume wins.
func ParseBootUnlockMethod(journal []byte, luksUUID string) (string, error) {
	method := UnlockUnknown
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record struct {
			Measuring json.RawMessage `json:"MEASURING"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return UnlockUnknown, fmt.Errorf("parse journal record: %w", err)
		}
		var measuring string
		// journalctl prints fields that are not valid UTF-8 as byte
		// arrays; those are not ours.
		if json.Unmarshal(record.Measuring, &measuring) != nil {
			continue
		}
		rest, ok := strings.CutPrefix(measuring, "cryptsetup-keyslot:")
		if !ok {
			continue
		}
		parts := strings.Split(rest, ":")
		if len(parts) != 4 || !strings.EqualFold(parts[1], luksUUID) || parts[3] == "" {
			continue
		}
		switch parts[2] {
		case "tpm2":
			method = UnlockChip
		case "":
			method = UnlockTyped
		default:
			// fido2 or pkcs11: not set up by GjallarOS, and not the chip.
			method = UnlockTyped
		}
	}
	if err := scanner.Err(); err != nil {
		return UnlockUnknown, err
	}
	return method, nil
}

// LUKSUUID returns the UUID in the LUKS header of device.
func LUKSUUID(ctx context.Context, device string) (string, error) {
	out, err := exec.CommandContext(ctx, "cryptsetup", "luksUUID", device).Output()
	if err != nil {
		return "", fmt.Errorf("cryptsetup luksUUID %s: %w", device, err)
	}
	uuid := strings.TrimSpace(string(out))
	if uuid == "" {
		return "", fmt.Errorf("cryptsetup luksUUID %s: empty", device)
	}
	return uuid, nil
}

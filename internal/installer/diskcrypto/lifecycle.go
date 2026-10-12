package diskcrypto

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Unlock methods of one LUKS2 volume, read from its JSON header.
type Keyslots struct {
	All     []int
	TPM2    []int
	TPM2PIN bool
	// Token id of each TPM2 slot.
	TPM2Token map[int]string
	Recovery  []int
	// Slots no token claims: passphrases typed at boot.
	Passphrase []int
}

func ParseKeyslots(raw []byte) (Keyslots, error) {
	var header struct {
		Keyslots map[string]json.RawMessage `json:"keyslots"`
		Tokens   map[string]struct {
			Type     string   `json:"type"`
			Keyslots []string `json:"keyslots"`
			TPM2PIN  bool     `json:"tpm2-pin"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return Keyslots{}, fmt.Errorf("parse LUKS metadata: %w", err)
	}
	var k Keyslots
	claimed := map[int]bool{}
	for id := range header.Keyslots {
		slot, err := strconv.Atoi(id)
		if err != nil {
			return Keyslots{}, fmt.Errorf("parse LUKS metadata: keyslot %q", id)
		}
		k.All = append(k.All, slot)
	}
	for tokenID, token := range header.Tokens {
		for _, id := range token.Keyslots {
			slot, err := strconv.Atoi(id)
			if err != nil {
				return Keyslots{}, fmt.Errorf("parse LUKS metadata: token keyslot %q", id)
			}
			claimed[slot] = true
			switch token.Type {
			case "systemd-tpm2":
				k.TPM2 = append(k.TPM2, slot)
				if k.TPM2Token == nil {
					k.TPM2Token = map[int]string{}
				}
				k.TPM2Token[slot] = tokenID
				k.TPM2PIN = k.TPM2PIN || token.TPM2PIN
			case "systemd-recovery":
				k.Recovery = append(k.Recovery, slot)
			}
		}
	}
	for _, slot := range k.All {
		if !claimed[slot] {
			k.Passphrase = append(k.Passphrase, slot)
		}
	}
	for _, s := range [][]int{k.All, k.TPM2, k.Recovery, k.Passphrase} {
		sort.Ints(s)
	}
	return k, nil
}

func ReadKeyslots(ctx context.Context, device string) (Keyslots, error) {
	out, err := exec.CommandContext(ctx, "cryptsetup", "luksDump", "--dump-json-metadata", device).Output()
	if err != nil {
		return Keyslots{}, fmt.Errorf("cryptsetup luksDump %s: %w", device, err)
	}
	return ParseKeyslots(out)
}

// RootDevice returns the LUKS partition under the root file system,
// through LVM or any other mapping in between.
func RootDevice(ctx context.Context) (string, error) {
	src, err := exec.CommandContext(ctx, "findmnt", "-nvo", "SOURCE", "/").Output()
	if err != nil {
		return "", fmt.Errorf("findmnt /: %w", err)
	}
	out, err := exec.CommandContext(ctx, "lsblk", "-spnro", "NAME,FSTYPE", strings.TrimSpace(string(src))).Output()
	if err != nil {
		return "", fmt.Errorf("lsblk: %w", err)
	}
	devices := parseLUKSDevices(string(out))
	switch len(devices) {
	case 0:
		return "", nil
	case 1:
		return devices[0], nil
	}
	return "", fmt.Errorf("root sits on several LUKS devices (%s)", strings.Join(devices, ", "))
}

// systemd recovery keys: 8 blocks of 8 modhex characters.
const (
	RecoveryBlocks   = 8
	recoveryBlockLen = 8
	recoveryAlphabet = "cbdefghijklnrtuv"
	ChallengeBlocks  = 3
)

func RecoveryKeyBlocks(key string) ([]string, error) {
	blocks := strings.Split(strings.TrimSpace(key), "-")
	if len(blocks) != RecoveryBlocks {
		return nil, fmt.Errorf("recovery key has %d blocks, want %d", len(blocks), RecoveryBlocks)
	}
	for _, b := range blocks {
		if len(b) != recoveryBlockLen || strings.Trim(b, recoveryAlphabet) != "" {
			return nil, fmt.Errorf("recovery key block %q is not 8 modhex characters", b)
		}
	}
	return blocks, nil
}

// Challenge picks which blocks (0-based, ascending) the user types back.
func Challenge() ([]int, error) {
	picked := map[int]bool{}
	for len(picked) < ChallengeBlocks {
		n, err := rand.Int(rand.Reader, big.NewInt(RecoveryBlocks))
		if err != nil {
			return nil, err
		}
		picked[int(n.Int64())] = true
	}
	out := make([]int, 0, ChallengeBlocks)
	for i := range picked {
		out = append(out, i)
	}
	sort.Ints(out)
	return out, nil
}

// SameBlock compares a typed block the way the boot prompt does: small
// letters only, no spaces inside, so the check never passes a spelling that
// cannot unlock the disk.
func SameBlock(want, typed string) bool {
	return strings.TrimSpace(typed) == want
}

// TestKeyslot checks that key opens exactly keyslot slot.
func TestKeyslot(ctx context.Context, device string, slot int, key string) error {
	path, cleanup, err := SecretFile(key)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := exec.CommandContext(ctx, "cryptsetup", "open", "--test-passphrase", "--key-slot", strconv.Itoa(slot), "--key-file", path, device).Run(); err != nil {
		return fmt.Errorf("new key does not open keyslot %d: %w", slot, err)
	}
	return nil
}

// systemd ships the TPM2 token plugin, and NixOS cryptsetup dlopens it by
// bare name, ignoring its external tokens path (e2e-target, 2026-09-29).
const tokenPluginDir = "/run/current-system/systemd/lib/cryptsetup"

// TestTPM2 checks that the security chip opens device now. A token with a
// startup PIN asks for it on tty.
func TestTPM2(ctx context.Context, device string, tty *os.File) error {
	cmd := exec.CommandContext(ctx, "cryptsetup", "open", "--test-passphrase", "--token-only", "--token-type", "systemd-tpm2", device)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+tokenPluginDir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security chip unlock test: %w", err)
	}
	return nil
}

// TestTPM2Token checks that one TPM2 token opens device now, with pin when
// the token wants one. A wrong PIN counts towards the TPM lockout.
func TestTPM2Token(ctx context.Context, device, tokenID, pin string) error {
	cmd := exec.CommandContext(ctx, "cryptsetup", "open", "--test-passphrase", "--token-only", "--token-id", tokenID, device)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+tokenPluginDir)
	// With a startup PIN, cryptsetup reads it from stdin.
	cmd.Stdin = strings.NewReader(pin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("security chip unlock test: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RefreshTPM2KeyslotRecord rewrites the record the first-boot enrollment
// left at target from the current header of device.
func RefreshTPM2KeyslotRecord(ctx context.Context, device, target, policy string) error {
	raw, err := exec.CommandContext(ctx, "cryptsetup", "luksDump", "--dump-json-metadata", device).Output()
	if err != nil {
		return fmt.Errorf("read LUKS metadata: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".metadata-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(raw)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return WriteTPM2KeyslotRecord(tmp.Name(), target, device, policy)
}

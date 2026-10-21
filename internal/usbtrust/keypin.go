package usbtrust

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeyPinFile holds the fingerprint of the USB trust signing key, written
// when the key is set up. The public key comes back from the TPM on every
// daemon start; the pin makes a different key at the handle an error
// instead of a new trust root (ROADMAP section 19, Plane 248).
const KeyPinFile = "tpm-key.sha256"

func keyFingerprint(key *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// readKeyPin returns the pinned fingerprint, or "" when nothing is pinned.
func readKeyPin(path string) (string, error) {
	if present, err := pathPresent(path); err != nil || !present {
		return "", err
	}
	if err := ValidateSecureStateFile(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read USB trust key pin: %w", err)
	}
	pin := strings.TrimSpace(string(data))
	if !strings.HasPrefix(pin, "sha256:") {
		return "", fmt.Errorf("USB trust key pin %s is malformed", path)
	}
	return pin, nil
}

// PinKey writes the fingerprint of the signer's key into dir. Callers pin
// only a key they just provisioned or one that verified the signed state
// already on disk.
func PinKey(ctx context.Context, dir string, signer *TPMSigner) error {
	key, err := signer.publicKey(ctx)
	if err != nil {
		return err
	}
	pin, err := keyFingerprint(key)
	if err != nil {
		return err
	}
	if err := EnsureSecureStateDirectory(dir); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, KeyPinFile), []byte(pin+"\n"), StateFileMode)
}

// PinExistingKey pins the key of an install set up before the pin existed.
// The signed state on disk must verify with the key first, so the pin
// names the key that signed it and not whatever the TPM answers now.
func PinExistingKey(ctx context.Context, dir string, signer *TPMSigner) (bool, error) {
	if pin, err := readKeyPin(filepath.Join(dir, KeyPinFile)); err != nil || pin != "" {
		return false, err
	}
	if _, err := ReadVerified(ctx, dir, signer); err != nil {
		return false, fmt.Errorf("signed USB trust state does not verify, key not pinned: %w", err)
	}
	if err := PinKey(ctx, dir, signer); err != nil {
		return false, err
	}
	return true, nil
}

var errKeyPinMismatch = errors.New("the TPM key at the USB trust handle is not the key this system set up")

func checkKeyPin(path string, key *ecdsa.PublicKey) error {
	pin, err := readKeyPin(path)
	if err != nil || pin == "" {
		return err
	}
	got, err := keyFingerprint(key)
	if err != nil {
		return err
	}
	if got != pin {
		return errKeyPinMismatch
	}
	return nil
}

package usbtrust

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// softTPMSigner signs like the TPM key at handle, with a software key, and
// verifies through the real TPMSigner code.
type softTPMSigner struct {
	*TPMSigner
	private *ecdsa.PrivateKey
}

func newSoftTPMSigner(t *testing.T, pinFile string) *softTPMSigner {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &softTPMSigner{
		TPMSigner: &TPMSigner{
			Handle:  "0x81000042",
			PinFile: pinFile,
			readPublic: func(context.Context, string) ([]byte, error) {
				return publicKeyPEM(t, &private.PublicKey), nil
			},
		},
		private: private,
	}
}

func (s *softTPMSigner) Sign(_ context.Context, payload []byte) (Signature, error) {
	digest := sha256.Sum256(payload)
	raw, err := ecdsa.SignASN1(rand.Reader, s.private, digest[:])
	if err != nil {
		return Signature{}, err
	}
	return Signature{
		Schema:       1,
		Algorithm:    TPMAlgorithm,
		KeyReference: s.Handle,
		DigestSHA256: hex.EncodeToString(digest[:]),
		Value:        base64.StdEncoding.EncodeToString(raw),
	}, nil
}

func keyPinDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trust")
	if err := EnsureSecureStateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestKeyPinRejectsOtherKey(t *testing.T) {
	ctx := context.Background()
	dir := keyPinDir(t)
	pinFile := filepath.Join(dir, KeyPinFile)
	set := newSoftTPMSigner(t, pinFile)
	if err := PinKey(ctx, dir, set.TPMSigner); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pinFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != StateFileMode {
		t.Fatalf("pin mode %v", info.Mode().Perm())
	}
	if err := ValidateSigner(ctx, newSoftTPMSigner(t, pinFile)); !errors.Is(err, errKeyPinMismatch) {
		t.Fatalf("other key at the handle: %v", err)
	}
	if err := ValidateSigner(ctx, newSoftTPMSigner(t, pinFile)); err == nil {
		t.Fatal("other key accepted on the second try")
	}
	again := &TPMSigner{Handle: set.Handle, PinFile: pinFile, readPublic: set.readPublic}
	if _, err := again.publicKey(ctx); err != nil {
		t.Fatalf("pinned key refused: %v", err)
	}
}

func TestKeyPinRefusesMalformedFile(t *testing.T) {
	dir := keyPinDir(t)
	pinFile := filepath.Join(dir, KeyPinFile)
	if err := os.WriteFile(pinFile, []byte("not a fingerprint\n"), StateFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := newSoftTPMSigner(t, pinFile).publicKey(context.Background()); err == nil {
		t.Fatal("malformed pin accepted")
	}
}

// An install from before the pin gets the key that verifies its signed
// state pinned, never another key.
func TestPinExistingKeyNeedsVerifiedState(t *testing.T) {
	ctx := context.Background()
	dir := keyPinDir(t)
	pinFile := filepath.Join(dir, KeyPinFile)
	owner := newSoftTPMSigner(t, pinFile)
	if err := CommitSigned(ctx, dir, sampleDocument(), owner); err != nil {
		t.Fatal(err)
	}
	if pinned, err := PinExistingKey(ctx, dir, newSoftTPMSigner(t, pinFile).TPMSigner); err == nil || pinned {
		t.Fatalf("key that did not sign the state pinned: %t %v", pinned, err)
	}
	if _, err := os.Lstat(pinFile); !os.IsNotExist(err) {
		t.Fatalf("pin file after refused pin: %v", err)
	}
	if pinned, err := PinExistingKey(ctx, dir, owner.TPMSigner); err != nil || !pinned {
		t.Fatalf("signing key not pinned: %t %v", pinned, err)
	}
	if pinned, err := PinExistingKey(ctx, dir, owner.TPMSigner); err != nil || pinned {
		t.Fatalf("second run: %t %v", pinned, err)
	}
	if err := ValidateSigner(ctx, newSoftTPMSigner(t, pinFile)); !errors.Is(err, errKeyPinMismatch) {
		t.Fatalf("other key after pin: %v", err)
	}
}

package usbtrust

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
)

const (
	TPMAlgorithm = "tpm2-ecdsa-p256-sha256"
)

var persistentHandlePattern = regexp.MustCompile(
	`^0x810[0-9a-fA-F]{5}$`,
)

type publicKeyReader func(
	context.Context,
	string,
) ([]byte, error)

type TPMSigner struct {
	Handle string

	readPublic publicKeyReader

	// PinFile, when set, holds the fingerprint the key must match
	// (KeyPinFile). Without the file every key is accepted.
	PinFile string

	// The persistent key never changes under a running daemon, so one
	// successful read serves every later verification. Reading it per
	// request put a TPM round-trip in every 2s policy poll, which timed out
	// whenever a rebuild held the TPM (2026-10-08).
	keyMu sync.Mutex
	key   *ecdsa.PublicKey
}

func NewTPMSigner(handle string) (*TPMSigner, error) {
	if !persistentHandlePattern.MatchString(handle) {
		return nil, fmt.Errorf(
			"invalid USB trust TPM handle %q; require reserved owner handle 0x81000000..0x810fffff",
			handle,
		)
	}

	return &TPMSigner{
		Handle: handle,
	}, nil
}

// ValidateSigner proves that the configured signing key is usable for the
// exact sign/verify operation required by persistent USB trust. It is safe to
// run at daemon startup and after provisioning; it does not mutate trust state.
func ValidateSigner(
	ctx context.Context,
	signer Signer,
) error {
	if signer == nil {
		return fmt.Errorf("USB trust signer is not configured")
	}

	payload := []byte("USB trust signing-key self-test")
	signature, err := signer.Sign(ctx, payload)
	if err != nil {
		return fmt.Errorf("USB trust signing self-test failed: %w", err)
	}

	if err := signer.Verify(ctx, payload, signature); err != nil {
		return fmt.Errorf("USB trust verification self-test failed: %w", err)
	}

	return nil
}

func (s *TPMSigner) Sign(
	ctx context.Context,
	payload []byte,
) (Signature, error) {
	var result Signature

	if _, err := s.publicKey(ctx); err != nil {
		return result, fmt.Errorf(
			"validate TPM USB trust signing key: %w",
			err,
		)
	}

	dir, err := os.MkdirTemp(
		"",
		"gjallar-usbtrust-sign-*",
	)
	if err != nil {
		return result, err
	}

	defer os.RemoveAll(dir)

	message := filepath.Join(dir, "message")
	signature := filepath.Join(dir, "signature")

	if err := os.WriteFile(
		message,
		payload,
		0600,
	); err != nil {
		return result, err
	}

	cmd := exec.CommandContext(
		ctx,
		"tpm2_sign",
		"-Q",
		"-c", s.Handle,
		"-g", "sha256",
		"-s", "ecdsa",
		"-f", "plain",
		"-o", signature,
		message,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return result, fmt.Errorf(
			"TPM sign failed: %w: %s",
			err,
			string(output),
		)
	}

	rawSignature, err := os.ReadFile(signature)
	if err != nil {
		return result, err
	}

	digest := sha256.Sum256(payload)

	return Signature{
		Schema:       1,
		Algorithm:    TPMAlgorithm,
		KeyReference: s.Handle,
		DigestSHA256: hex.EncodeToString(
			digest[:],
		),
		Value: base64.StdEncoding.EncodeToString(
			rawSignature,
		),
	}, nil
}

func (s *TPMSigner) Verify(
	ctx context.Context,
	payload []byte,
	signature Signature,
) error {
	if signature.Schema != 1 {
		return fmt.Errorf(
			"unsupported signature schema %d",
			signature.Schema,
		)
	}

	if signature.Algorithm != TPMAlgorithm {
		return fmt.Errorf(
			"unexpected signature algorithm %q",
			signature.Algorithm,
		)
	}

	if signature.KeyReference != s.Handle {
		return fmt.Errorf(
			"signature key %q does not match configured TPM handle %q",
			signature.KeyReference,
			s.Handle,
		)
	}

	digest := sha256.Sum256(payload)
	expectedDigest := hex.EncodeToString(
		digest[:],
	)

	if signature.DigestSHA256 != expectedDigest {
		return fmt.Errorf(
			"USB trust document digest mismatch",
		)
	}

	rawSignature, err := base64.StdEncoding.DecodeString(
		signature.Value,
	)
	if err != nil {
		return fmt.Errorf(
			"decode TPM signature: %w",
			err,
		)
	}

	publicKey, err := s.publicKey(ctx)
	if err != nil {
		return fmt.Errorf(
			"read TPM USB trust public key: %w",
			err,
		)
	}

	if !ecdsa.VerifyASN1(
		publicKey,
		digest[:],
		rawSignature,
	) {
		return fmt.Errorf(
			"TPM USB trust signature invalid",
		)
	}

	return nil
}

func (s *TPMSigner) publicKey(
	ctx context.Context,
) (*ecdsa.PublicKey, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	key, err := s.readPublicKey(ctx)
	if err != nil {
		return nil, err
	}
	if s.PinFile != "" {
		if err := checkKeyPin(s.PinFile, key); err != nil {
			return nil, err
		}
	}
	s.key = key
	return key, nil
}

func (s *TPMSigner) readPublicKey(
	ctx context.Context,
) (*ecdsa.PublicKey, error) {
	var (
		data []byte
		err  error
	)

	if s.readPublic != nil {
		data, err = s.readPublic(
			ctx,
			s.Handle,
		)
	} else {
		data, err = readTPMPublicKey(
			ctx,
			s.Handle,
		)
	}

	if err != nil {
		return nil, err
	}

	return parseTPMPublicKeyPEM(data)
}

func readTPMPublicKey(
	ctx context.Context,
	handle string,
) ([]byte, error) {
	dir, err := os.MkdirTemp(
		"",
		"gjallar-usbtrust-public-*",
	)
	if err != nil {
		return nil, err
	}

	defer os.RemoveAll(dir)

	publicKey := filepath.Join(
		dir,
		"public.pem",
	)

	cmd := exec.CommandContext(
		ctx,
		"tpm2_readpublic",
		"-Q",
		"-c", handle,
		"-f", "pem",
		"-o", publicKey,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf(
			"TPM public-key read failed: %w: %s",
			err,
			string(output),
		)
	}

	data, err := os.ReadFile(publicKey)
	if err != nil {
		return nil, fmt.Errorf(
			"read TPM public-key PEM: %w",
			err,
		)
	}

	return data, nil
}

func parseTPMPublicKeyPEM(
	data []byte,
) (*ecdsa.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf(
			"TPM public key is not valid PEM",
		)
	}

	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf(
			"TPM public key contains trailing data",
		)
	}

	parsed, err := x509.ParsePKIXPublicKey(
		block.Bytes,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"parse TPM public key: %w",
			err,
		)
	}

	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf(
			"TPM USB trust key is not ECDSA",
		)
	}

	curve := publicKey.Curve.Params()
	p256 := elliptic.P256().Params()

	if curve == nil ||
		curve.Name != p256.Name ||
		curve.BitSize != p256.BitSize {
		return nil, fmt.Errorf(
			"TPM USB trust key uses unsupported curve %q/%d; require P-256",
			curveName(curve),
			curveBits(curve),
		)
	}

	return publicKey, nil
}

func curveName(
	params *elliptic.CurveParams,
) string {
	if params == nil {
		return "<unknown>"
	}

	return params.Name
}

func curveBits(
	params *elliptic.CurveParams,
) int {
	if params == nil {
		return 0
	}

	return params.BitSize
}

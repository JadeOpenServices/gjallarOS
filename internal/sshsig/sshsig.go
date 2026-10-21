// Package sshsig checks OpenSSH signatures (PROTOCOL.sshsig) of settings
// files signed with a user's own key (ROADMAP "Pairing and own keys"). The
// signature itself is checked by OpenSSH's ssh-keygen -Y verify; this
// package only parses public keys strictly, writes the allowed-signers line
// from a parsed key, and runs ssh-keygen with fixed arguments.
package sshsig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Keygen is the ssh-keygen the package runs, a Nix store path set at build
// time. Empty means this build cannot check own-key signatures.
var Keygen string

const (
	// Ed25519 and SKEd25519 are the key types an own key may have: a key
	// file, or a FIDO hardware key.
	Ed25519   = "ssh-ed25519"
	SKEd25519 = "sk-ssh-ed25519@openssh.com"

	// MaxSignatureBytes bounds a signature file; an Ed25519 one is about
	// 300 bytes, a FIDO one a little more.
	MaxSignatureBytes = 4 << 10
	// MaxKeyLineBytes bounds a public key line with its comment.
	MaxKeyLineBytes = 1 << 10

	verifyTimeout = 10 * time.Second
	// principal names the one key in the allowed-signers file.
	principal = "owner"
	maxOutput = 4 << 10
	// maxApplication bounds the application string of a FIDO key.
	maxApplication = 128
)

var (
	ErrBadKey       = errors.New("this is not an OpenSSH ssh-ed25519 or sk-ssh-ed25519@openssh.com public key")
	ErrBadSignature = errors.New("the signature does not match this file and key")
	ErrNoKeygen     = errors.New("this build cannot check OpenSSH signatures")
)

// Key is a parsed public key: its type and the wire-format blob.
type Key struct {
	Type string
	blob []byte
}

// String is the key as OpenSSH writes it, without a comment.
func (k Key) String() string {
	return k.Type + " " + base64.StdEncoding.EncodeToString(k.blob)
}

// Fingerprint is the key's SHA256 fingerprint, as ssh-keygen -l shows it.
func (k Key) Fingerprint() string {
	sum := sha256.Sum256(k.blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Equal reports whether both are the same key.
func (k Key) Equal(other Key) bool {
	return k.Type == other.Type && bytes.Equal(k.blob, other.blob)
}

// ParseKey reads one public key line, "TYPE BASE64 [COMMENT]". The comment
// is dropped; authorized_keys options, other key types and blobs with
// anything beyond the key are refused.
func ParseKey(line string) (Key, error) {
	if len(line) > MaxKeyLineBytes || strings.ContainsAny(line, "\r\n\x00") {
		return Key{}, ErrBadKey
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != Ed25519 && fields[0] != SKEd25519 {
		return Key{}, ErrBadKey
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil {
		return Key{}, ErrBadKey
	}
	rest := blob
	kind, rest, ok := wireString(rest)
	if !ok || string(kind) != fields[0] {
		return Key{}, ErrBadKey
	}
	public, rest, ok := wireString(rest)
	if !ok || len(public) != 32 {
		return Key{}, ErrBadKey
	}
	if fields[0] == SKEd25519 {
		var app []byte
		app, rest, ok = wireString(rest)
		if !ok || len(app) > maxApplication || !bytes.HasPrefix(app, []byte("ssh:")) || !printable(app) {
			return Key{}, ErrBadKey
		}
	}
	if len(rest) != 0 {
		return Key{}, ErrBadKey
	}
	return Key{Type: fields[0], blob: blob}, nil
}

func wireString(data []byte) (value, rest []byte, ok bool) {
	if len(data) < 4 {
		return nil, nil, false
	}
	size := binary.BigEndian.Uint32(data)
	if uint64(size) > uint64(len(data)-4) {
		return nil, nil, false
	}
	return data[4 : 4+size], data[4+size:], true
}

func printable(data []byte) bool {
	for _, b := range data {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

// allowedSigner is the one line of the allowed-signers file. It never
// carries no-touch-required, so a FIDO signature without the user's touch
// is refused.
func allowedSigner(key Key, namespace string) string {
	return fmt.Sprintf("%s namespaces=%q %s\n", principal, namespace, key)
}

// Verify checks that signature is key's signature of message under
// namespace. The namespace must be plain lower-case words with - between.
func Verify(ctx context.Context, key Key, namespace string, message, signature []byte) error {
	if Keygen == "" {
		return ErrNoKeygen
	}
	if !plainNamespace(namespace) {
		return fmt.Errorf("namespace %q", namespace)
	}
	if len(signature) == 0 || len(signature) > MaxSignatureBytes || !armored(signature) {
		return ErrBadSignature
	}
	dir, err := os.MkdirTemp("", "sshsig-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	signers := filepath.Join(dir, "allowed_signers")
	sigPath := filepath.Join(dir, "message.sig")
	if err := os.WriteFile(signers, []byte(allowedSigner(key, namespace)), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(sigPath, signature, 0o600); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, Keygen, "-Y", "verify", "-f", signers, "-I", principal, "-n", namespace, "-s", sigPath)
	cmd.Env = []string{"HOME=" + dir}
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(message)
	var out limited
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ssh-keygen did not answer within %s", verifyTimeout)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("%w (ssh-keygen: %s)", ErrBadSignature, out.firstLine())
		}
		return err
	}
	if !strings.HasPrefix(out.String(), fmt.Sprintf("Good %q signature for %s with ", namespace, principal)) {
		return fmt.Errorf("%w (ssh-keygen said: %s)", ErrBadSignature, out.firstLine())
	}
	return nil
}

func plainNamespace(namespace string) bool {
	if namespace == "" || strings.HasPrefix(namespace, "-") || strings.HasSuffix(namespace, "-") {
		return false
	}
	for _, r := range namespace {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}

// armored reports whether signature is an OpenSSH armored signature:
// printable ASCII lines between the BEGIN and END lines, so ssh-keygen
// never sees anything else.
func armored(signature []byte) bool {
	text := strings.TrimRight(string(signature), "\n")
	if !strings.HasPrefix(text, "-----BEGIN SSH SIGNATURE-----\n") || !strings.HasSuffix(text, "\n-----END SSH SIGNATURE-----") {
		return false
	}
	for _, b := range []byte(text) {
		if b != '\n' && (b < 0x20 || b > 0x7e) {
			return false
		}
	}
	return true
}

// limited keeps the first maxOutput bytes written to it.
type limited struct{ bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := maxOutput - l.Len(); room > 0 {
		l.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (l *limited) firstLine() string {
	line, _, _ := strings.Cut(strings.TrimSpace(l.String()), "\n")
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, line)
}

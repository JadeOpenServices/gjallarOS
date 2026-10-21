package sshsig

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// keygen points Keygen at the ssh-keygen on PATH for the test.
func keygen(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	old := Keygen
	Keygen = path
	t.Cleanup(func() { Keygen = old })
}

// newKey makes a SYNTHETIC Ed25519 key file without a passphrase and
// returns its path and public key line.
func newKey(t *testing.T, name string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	run(t, nil, Keygen, "-q", "-t", "ed25519", "-N", "", "-C", "SYNTHETIC test key", "-f", path)
	public, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return path, strings.TrimSpace(string(public))
}

func sign(t *testing.T, keyPath, namespace string, message []byte) []byte {
	t.Helper()
	return run(t, message, Keygen, "-q", "-Y", "sign", "-n", namespace, "-f", keyPath)
}

func run(t *testing.T, stdin []byte, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return out
}

func wire(parts ...string) []byte {
	var b []byte
	for _, part := range parts {
		b = binary.BigEndian.AppendUint32(b, uint32(len(part)))
		b = append(b, part...)
	}
	return b
}

func TestParseKey(t *testing.T) {
	key32 := strings.Repeat("k", 32)
	ed := base64.StdEncoding.EncodeToString(wire(Ed25519, key32))
	sk := base64.StdEncoding.EncodeToString(wire(SKEd25519, key32, "ssh:"))
	key, err := ParseKey(Ed25519 + " " + ed + " SYNTHETIC comment with words")
	if err != nil {
		t.Fatal(err)
	}
	if key.String() != Ed25519+" "+ed {
		t.Errorf("String() = %q; the comment must be dropped", key)
	}
	if _, err := ParseKey(SKEd25519 + " " + sk); err != nil {
		t.Errorf("FIDO key: %v", err)
	}
	for name, line := range map[string]string{
		"empty":            "",
		"no blob":          Ed25519,
		"options":          `command="x" ` + Ed25519 + " " + ed,
		"rsa":              "ssh-rsa " + ed,
		"type mismatch":    SKEd25519 + " " + ed,
		"short key":        Ed25519 + " " + base64.StdEncoding.EncodeToString(wire(Ed25519, "short")),
		"trailing":         Ed25519 + " " + base64.StdEncoding.EncodeToString(append(wire(Ed25519, key32), 0)),
		"not base64":       Ed25519 + " !!!!",
		"two lines":        Ed25519 + " " + ed + "\n" + Ed25519 + " " + ed,
		"sk no app":        SKEd25519 + " " + base64.StdEncoding.EncodeToString(wire(SKEd25519, key32)),
		"sk other app":     SKEd25519 + " " + base64.StdEncoding.EncodeToString(wire(SKEd25519, key32, "https://x")),
		"sk app with ctrl": SKEd25519 + " " + base64.StdEncoding.EncodeToString(wire(SKEd25519, key32, "ssh:\n")),
		"long":             Ed25519 + " " + ed + " " + strings.Repeat("c", MaxKeyLineBytes),
	} {
		if _, err := ParseKey(line); !errors.Is(err, ErrBadKey) {
			t.Errorf("%s: %v, want ErrBadKey", name, err)
		}
	}
}

func TestVerify(t *testing.T) {
	keygen(t)
	ctx := context.Background()
	path, line := newKey(t, "own")
	_, otherLine := newKey(t, "other")
	key, err := ParseKey(line)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ParseKey(otherLine)
	if err != nil {
		t.Fatal(err)
	}
	if listed := string(run(t, nil, Keygen, "-l", "-f", path+".pub")); !strings.Contains(listed, " "+key.Fingerprint()+" ") {
		t.Errorf("Fingerprint() = %s; ssh-keygen -l says %s", key.Fingerprint(), listed)
	}
	message := []byte("SYNTHETIC settings payload\n")
	sig := sign(t, path, "gjallaros-settings", message)

	if err := Verify(ctx, key, "gjallaros-settings", message, sig); err != nil {
		t.Fatalf("good signature: %v", err)
	}
	refused := map[string]error{
		"changed message": Verify(ctx, key, "gjallaros-settings", []byte("SYNTHETIC changed\n"), sig),
		"other key":       Verify(ctx, other, "gjallaros-settings", message, sig),
		"git namespace":   Verify(ctx, key, "gjallaros-settings", message, sign(t, path, "git", message)),
		"empty":           Verify(ctx, key, "gjallaros-settings", message, nil),
		"not armored":     Verify(ctx, key, "gjallaros-settings", message, []byte("SYNTHETIC not a signature")),
		"control bytes":   Verify(ctx, key, "gjallaros-settings", message, bytes.Replace(sig, []byte("\n"), []byte("\n\x1b"), 2)),
		"too large":       Verify(ctx, key, "gjallaros-settings", message, append(sig, bytes.Repeat([]byte("A"), MaxSignatureBytes)...)),
	}
	for name, err := range refused {
		if !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: %v, want ErrBadSignature", name, err)
		}
	}
	if err := Verify(ctx, key, "-n", message, sig); err == nil {
		t.Error("an option as namespace is accepted")
	}
	Keygen = ""
	if err := Verify(ctx, key, "gjallaros-settings", message, sig); !errors.Is(err, ErrNoKeygen) {
		t.Errorf("without ssh-keygen: %v", err)
	}
}

func TestAllowedSignerNeverSkipsTouch(t *testing.T) {
	key, err := ParseKey(SKEd25519 + " " + base64.StdEncoding.EncodeToString(wire(SKEd25519, strings.Repeat("k", 32), "ssh:")))
	if err != nil {
		t.Fatal(err)
	}
	line := allowedSigner(key, "gjallaros-settings")
	if want := `owner namespaces="gjallaros-settings" ` + key.String() + "\n"; line != want || strings.Contains(line, "no-touch-required") {
		t.Errorf("allowed signer = %q, want %q", line, want)
	}
}

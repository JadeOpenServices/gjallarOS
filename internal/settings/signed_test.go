package settings

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T, seed byte) ed25519.PrivateKey {
	t.Helper()
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

func testFile() File {
	return File{
		Schema:   FileSchema,
		Device:   "0123456789abcdef0123456789abcdef",
		User:     "tester",
		UID:      1000,
		Created:  time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		Settings: map[string]json.RawMessage{"hostname": json.RawMessage(`"example-host"`)},
	}
}

func trustOnly(key ed25519.PrivateKey) func(ed25519.PublicKey) bool {
	return func(public ed25519.PublicKey) bool { return public.Equal(key.Public()) }
}

// resign rewrites the payload of a sealed file and signs it again, so a test
// reaches the parser with content Seal would never write.
func resign(t *testing.T, key ed25519.PrivateKey, payload string) []byte {
	t.Helper()
	public := key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(key, signedBytes(fileMagic, public, []byte(payload)))
	return []byte(fileMagic + keyPrefix + base64.StdEncoding.EncodeToString(public) + "\n" +
		sigPrefix + base64.StdEncoding.EncodeToString(sig) + "\n" + payload)
}

func TestSealOpen(t *testing.T) {
	key := testKey(t, 1)
	data, err := Seal(key, testFile())
	if err != nil {
		t.Fatal(err)
	}
	file, public, err := Open(data, trustOnly(key))
	if err != nil {
		t.Fatal(err)
	}
	if !public.Equal(key.Public()) {
		t.Error("Open returned another key")
	}
	want := testFile()
	if file.User != want.User || file.UID != want.UID || file.Device != want.Device ||
		!file.Created.Equal(want.Created) || string(file.Settings["hostname"]) != `"example-host"` {
		t.Errorf("Open = %+v, want %+v", file, want)
	}
}

func TestOpenRefuses(t *testing.T) {
	key := testKey(t, 1)
	sealed, err := Seal(key, testFile())
	if err != nil {
		t.Fatal(err)
	}
	header := len(fileMagic) + keyLineLen + sigLineLen
	payload := string(sealed[header:])

	zeroSig := bytes.Clone(sealed)
	copy(zeroSig[len(fileMagic)+keyLineLen+len(sigPrefix):],
		base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)))
	changed := bytes.Replace(bytes.Clone(sealed), []byte("example-host"), []byte("example-hosu"), 1)
	otherKey := bytes.Clone(sealed)
	copy(otherKey[len(fileMagic)+len(keyPrefix):],
		base64.StdEncoding.EncodeToString(testKey(t, 2).Public().(ed25519.PublicKey)))

	cases := []struct {
		name string
		data []byte
		want error
		text string
	}{
		{"empty", nil, ErrNotSettingsFile, ""},
		{"no magic", []byte(strings.Replace(string(sealed), "v1", "v2", 1)), ErrNotSettingsFile, ""},
		{"too large", append(bytes.Clone(sealed), bytes.Repeat([]byte(" "), MaxFileBytes)...), ErrNotSettingsFile, ""},
		{"changed payload", changed, ErrBadSignature, ""},
		{"zero signature", zeroSig, ErrBadSignature, ""},
		{"swapped key", otherKey, ErrUnknownSigner, ""},
		{"unknown signer", resign(t, testKey(t, 3), payload), ErrUnknownSigner, ""},
		{"duplicate name", resign(t, key, strings.Replace(payload, `"user": "tester",`, `"user": "tester", "user": "root",`, 1)), nil, "twice"},
		{"duplicate setting", resign(t, key, strings.Replace(payload, `"hostname": "example-host"`, `"hostname": "a", "hostname": "b"`, 1)), nil, "twice"},
		{"unknown field", resign(t, key, strings.Replace(payload, `"schema": 1,`, `"schema": 1, "extra": true,`, 1)), nil, "unknown field"},
		{"trailing data", resign(t, key, payload+"{}\n"), nil, "more after"},
		{"not utf-8", resign(t, key, strings.Replace(payload, "tester", "test\xffr", 1)), nil, "UTF-8"},
		{"other schema", resign(t, key, strings.Replace(payload, `"schema": 1,`, `"schema": 2,`, 1)), nil, "version 2"},
		{"no settings", resign(t, key, strings.Replace(payload, `"hostname": "example-host"`, ``, 1)), nil, "no settings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Open(c.data, trustOnly(key))
			switch {
			case err == nil:
				t.Fatal("Open accepted the file")
			case c.want != nil && !errors.Is(err, c.want):
				t.Errorf("Open: %v, want %v", err, c.want)
			case c.text != "" && !strings.Contains(err.Error(), c.text):
				t.Errorf("Open: %v, want it to mention %q", err, c.text)
			}
		})
	}
}

func TestOpenAsksTrustBeforeVerify(t *testing.T) {
	key := testKey(t, 1)
	sealed, err := Seal(key, testFile())
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	_, _, err = Open(sealed, func(ed25519.PublicKey) bool { asked++; return false })
	if !errors.Is(err, ErrUnknownSigner) || asked != 1 {
		t.Errorf("Open: %v after %d trust checks, want ErrUnknownSigner after 1", err, asked)
	}
}

func TestOpenSSH(t *testing.T) {
	data, err := Unsigned(testFile())
	if err != nil {
		t.Fatal(err)
	}
	if !IsUnsigned(data) || IsUnsigned(append([]byte("x"), data...)) {
		t.Fatal("IsUnsigned does not tell own-key files apart")
	}
	var checked []byte
	file, err := OpenSSH(data, func(message []byte) error { checked = message; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checked, data) {
		t.Error("the signature is not checked over the whole file")
	}
	if file.User != "tester" || string(file.Settings["hostname"]) != `"example-host"` {
		t.Errorf("file = %+v", file)
	}

	bad := errors.New("SYNTHETIC bad signature")
	parsed := false
	if _, err := OpenSSH([]byte(sshFileMagic+"{"), func([]byte) error { parsed = true; return bad }); !errors.Is(err, bad) || !parsed {
		t.Errorf("a failed check: %v; the payload must not be parsed first", err)
	}
	sealed, err := Seal(testKey(t, 1), testFile())
	if err != nil {
		t.Fatal(err)
	}
	ok := func([]byte) error { return nil }
	if _, err := OpenSSH(sealed, ok); !errors.Is(err, ErrNotSettingsFile) {
		t.Errorf("a managed file: %v", err)
	}
	if _, err := OpenSSH(append(data, bytes.Repeat([]byte(" "), MaxFileBytes)...), ok); !errors.Is(err, ErrNotSettingsFile) {
		t.Errorf("a large file: %v", err)
	}
	if _, err := OpenSSH([]byte(sshFileMagic+`{"schema":1,"schema":1}`), ok); err == nil {
		t.Error("a repeated name is accepted")
	}
}

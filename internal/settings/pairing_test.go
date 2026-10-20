package settings

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testPairFile() PairFile {
	return PairFile{
		Schema:   PairSchema,
		Device:   "0123456789abcdef0123456789abcdef",
		User:     "tester",
		UID:      1000,
		Created:  time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		Hostname: "example-host",
	}
}

func resignPairing(t *testing.T, key ed25519.PrivateKey, payload string) []byte {
	t.Helper()
	public := key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(key, signedBytes(pairMagic, public, []byte(payload)))
	return []byte(pairMagic + keyPrefix + base64.StdEncoding.EncodeToString(public) + "\n" +
		sigPrefix + base64.StdEncoding.EncodeToString(sig) + "\n" + payload)
}

func TestSealOpenPairing(t *testing.T) {
	key := testKey(t, 1)
	data, err := SealPairing(key, testPairFile())
	if err != nil {
		t.Fatal(err)
	}
	file, public, err := OpenPairing(data)
	if err != nil {
		t.Fatal(err)
	}
	if !public.Equal(key.Public()) || file != testPairFile() {
		t.Fatalf("OpenPairing = %+v", file)
	}
}

func TestPairingAndSettingsFilesDoNotMix(t *testing.T) {
	key := testKey(t, 1)
	pairing, err := SealPairing(key, testPairFile())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(pairing, trustOnly(key)); !errors.Is(err, ErrNotSettingsFile) {
		t.Errorf("Open(pairing file) = %v", err)
	}
	settingsFile, err := Seal(key, testFile())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenPairing(settingsFile); !errors.Is(err, ErrNotPairingFile) {
		t.Errorf("OpenPairing(settings file) = %v", err)
	}
}

func TestOpenPairingRefuses(t *testing.T) {
	key := testKey(t, 1)
	good, err := SealPairing(key, testPairFile())
	if err != nil {
		t.Fatal(err)
	}
	payload := string(good[len(pairMagic)+keyLineLen+sigLineLen:])
	changed := strings.Replace(string(good), "example-host", "other-host!!", 1)
	for _, test := range []struct {
		name string
		data []byte
		want string
	}{
		{"changed", []byte(changed), ErrBadSignature.Error()},
		{"too large", make([]byte, MaxPairBytes+1), "larger"},
		{"unknown field", resignPairing(t, key, strings.Replace(payload, `"schema"`, `"extra": 1, "schema"`, 1)), "unknown field"},
		{"duplicate name", resignPairing(t, key, strings.Replace(payload, `"user": "tester",`, `"user": "tester", "user": "root",`, 1)), "twice"},
		{"bad device", resignPairing(t, key, strings.Replace(payload, "0123456789abcdef0123456789abcdef", "machine", 1)), "device"},
		{"control character", resignPairing(t, key, strings.Replace(payload, "example-host", `example‮host`, 1)), "cannot be shown"},
		{"empty hostname", resignPairing(t, key, strings.Replace(payload, `"example-host"`, `""`, 1)), "hostname"},
		{"trailing data", resignPairing(t, key, payload+"{}"), "more after"},
		{"other schema", resignPairing(t, key, strings.Replace(payload, `"schema": 1`, `"schema": 2`, 1)), "version 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := OpenPairing(test.data); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPairCode(t *testing.T) {
	a := testPairFile().Side(testKey(t, 1).Public().(ed25519.PublicKey))
	b := PairSide{Key: testKey(t, 2).Public().(ed25519.PublicKey), Device: "fedcba9876543210fedcba9876543210", UID: 1001, User: "other"}
	code := PairCode(a, b)
	if !regexp.MustCompile(`^\d{5}( \d{5}){11}$`).MatchString(code) {
		t.Fatalf("code %q is not 12 groups of 5 digits", code)
	}
	if PairCode(b, a) != code {
		t.Error("the code depends on which side computes it")
	}
	digits := strings.ReplaceAll(code, " ", "")
	if !strings.Contains(digits, PairHalf(a)) || !strings.Contains(digits, PairHalf(b)) {
		t.Error("the code does not hold both halves")
	}
	for name, changed := range map[string]PairSide{
		"key":    {Key: testKey(t, 3).Public().(ed25519.PublicKey), Device: b.Device, UID: b.UID, User: b.User},
		"device": {Key: b.Key, Device: a.Device, UID: b.UID, User: b.User},
		"uid":    {Key: b.Key, Device: b.Device, UID: 1002, User: b.User},
		"user":   {Key: b.Key, Device: b.Device, UID: b.UID, User: "another"},
	} {
		if PairHalf(changed) == PairHalf(b) {
			t.Errorf("changing the %s keeps the half", name)
		}
	}
	if id := PairID(b); id != PairHalf(b)[:5]+"-"+PairHalf(b)[5:10] {
		t.Errorf("PairID = %q", id)
	}
}

// The first half is fixed, so a change to the computation shows up here:
// both devices must compute the same digits whatever their version.
func TestPairHalfStable(t *testing.T) {
	side := testPairFile().Side(testKey(t, 1).Public().(ed25519.PublicKey))
	if got, want := PairHalf(side), "046705269197982387894317660967"; got != want {
		t.Fatalf("half %s, want %s", got, want)
	}
}

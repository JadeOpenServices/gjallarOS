package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

const testDevice = "0123456789abcdef0123456789abcdef"

// withKeys gives the fixture a key store in a temp dir with a SYNTHETIC
// host key and the users tester (1000) and other (1001).
func withKeys(t *testing.T, f *fixture) map[uint32]string {
	t.Helper()
	names := map[uint32]string{1000: "tester", 1001: "other"}
	f.svc.keys = &keyStore{
		dir:     t.TempDir(),
		hostKey: bytes.Repeat([]byte{7}, hostKeySize),
		userName: func(uid uint32) (string, error) {
			if name, ok := names[uid]; ok {
				return name, nil
			}
			return "", fmt.Errorf("no user %d", uid)
		},
		now: func() time.Time { return f.now },
	}
	f.svc.device = testDevice
	return names
}

// changeTimezone applies an everyday change, so a file made before it has
// something to bring back.
func changeTimezone(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"timezone":"Etc/UTC"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, testColors); err != nil {
		t.Fatal(err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	f := newFixture(t)
	withKeys(t, f)
	ctx := context.Background()
	before := f.value(t, "timezone")

	data, err := f.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sys.asked) != 1 || f.sys.asked[0] != exportAction+" " {
		t.Fatalf("password checks = %v", f.sys.asked)
	}
	file, _, err := settings.Open(data, func(ed25519.PublicKey) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if file.User != "tester" || file.UID != 1000 || file.Device != testDevice || !file.Created.Equal(f.now) {
		t.Errorf("file header = %s %d %s %s", file.User, file.UID, file.Device, file.Created)
	}
	if _, ok := file.Settings["autoReboot"]; ok {
		t.Error("the file holds an installer key")
	}
	if string(file.Settings["timezone"]) != before {
		t.Errorf("timezone in the file = %s, want %s", file.Settings["timezone"], before)
	}

	changeTimezone(t, f)
	f.now = f.now.Add(72 * time.Hour)
	pending, err := f.svc.Import(ctx, ":1.10", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Keys) != 1 || pending.Keys[0].Key != "timezone" || string(pending.Keys[0].New) != before {
		t.Fatalf("import asks for %+v, want timezone back to %s", pending.Keys, before)
	}
	if len(pending.Notes) != 1 || !strings.Contains(pending.Notes[0], "from a file tester made on this device") ||
		!strings.Contains(pending.Notes[0], "3 days ago") {
		t.Errorf("notes = %q", pending.Notes)
	}
	if err := f.svc.Apply(ctx, ":1.10", pending.ID, testColors); err != nil {
		t.Fatal(err)
	}
	if got := f.value(t, "timezone"); got != before {
		t.Errorf("timezone after import = %s, want %s", got, before)
	}
}

func TestExportNeedsHostKeyAndPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Export(ctx, ":1.10"); !errors.Is(err, errRefused) {
		t.Fatalf("export without keys: %v, want refused", err)
	}
	if _, err := f.svc.Import(ctx, ":1.10", []byte("x")); !errors.Is(err, errRefused) {
		t.Fatalf("import without keys: %v, want refused", err)
	}

	withKeys(t, f)
	f.svc.keys.hostKey = nil
	if _, err := f.svc.Export(ctx, ":1.10"); !errors.Is(err, errNoSigningKey) {
		t.Fatalf("export without host key: %v, want errNoSigningKey", err)
	}
	if len(f.sys.asked) != 0 {
		t.Errorf("asked for a password it could not use: %v", f.sys.asked)
	}

	f.svc.keys.hostKey = bytes.Repeat([]byte{7}, hostKeySize)
	f.sys.allow = false
	if _, err := f.svc.Export(ctx, ":1.10"); !errors.Is(err, errDenied) {
		t.Fatalf("export with a wrong password: %v, want denied", err)
	}
}

func TestImportRefuses(t *testing.T) {
	f := newFixture(t)
	names := withKeys(t, f)
	ctx := context.Background()
	data, err := f.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	changeTimezone(t, f)

	changed := bytes.Replace(bytes.Clone(data), []byte(`"uid": 1000`), []byte(`"uid": 1001`), 1)
	if _, err := f.svc.Import(ctx, ":1.10", changed); !errors.Is(err, errRefused) ||
		!strings.Contains(err.Error(), settings.ErrBadSignature.Error()) {
		t.Errorf("changed file: %v", err)
	}

	f.svc.device = "fedcba9876543210fedcba9876543210"
	if _, err := f.svc.Import(ctx, ":1.10", data); !errors.Is(err, errRefused) {
		t.Errorf("file from another device: %v, want refused", err)
	}
	f.svc.device = testDevice

	names[1000] = "newcomer"
	if _, err := f.svc.Import(ctx, ":1.10", data); !errors.Is(err, errRefused) ||
		!strings.Contains(err.Error(), settings.ErrUnknownSigner.Error()) {
		t.Errorf("file of a removed user: %v", err)
	}
	names[1000] = "tester"
	if _, err := f.svc.Import(ctx, ":1.12", data); !errors.Is(err, errRefused) ||
		!strings.Contains(err.Error(), "only that account") {
		t.Errorf("file of another account: %v", err)
	}
	if _, err := f.svc.Import(ctx, ":1.99", data); !errors.Is(err, errRefused) {
		t.Errorf("unknown caller: %v, want refused", err)
	}
}

func TestNewUserOnOldUIDGetsFreshKey(t *testing.T) {
	f := newFixture(t)
	names := withKeys(t, f)
	old, _, err := f.svc.keys.signer(1000)
	if err != nil {
		t.Fatal(err)
	}
	names[1000] = "newcomer"
	fresh, name, err := f.svc.keys.signer(1000)
	if err != nil {
		t.Fatal(err)
	}
	if name != "newcomer" || fresh.Equal(old) {
		t.Fatalf("signer after the uid changed hands = %s, same key %v", name, fresh.Equal(old))
	}
	trusted, err := f.svc.keys.trusted()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := trusted[string(old.Public().(ed25519.PublicKey))]; ok {
		t.Error("the earlier user's key is still trusted")
	}
}

func TestFileFromEarlierHostKeyStillImports(t *testing.T) {
	f := newFixture(t)
	withKeys(t, f)
	ctx := context.Background()
	data, err := f.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	f.svc.keys.hostKey = bytes.Repeat([]byte{8}, hostKeySize)
	if _, err := f.svc.Export(ctx, ":1.10"); err != nil {
		t.Fatal(err)
	}
	record, err := f.svc.keys.read(1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Retired) != 1 {
		t.Fatalf("retired keys = %d, want 1", len(record.Retired))
	}
	changeTimezone(t, f)
	if _, err := f.svc.Import(ctx, ":1.10", data); err != nil {
		t.Errorf("file signed with the retired key: %v", err)
	}
}

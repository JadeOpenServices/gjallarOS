package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

const otherDevice = "fedcba9876543210fedcba9876543210"

// pairedDevices gives two SYNTHETIC devices, each with its own host key and
// device id, whose user tester (1000) paired the other's tester.
func pairedDevices(t *testing.T) (*fixture, *fixture) {
	t.Helper()
	ctx := context.Background()
	a, b := newFixture(t), newFixture(t)
	withKeys(t, a)
	withKeys(t, b)
	b.svc.device = otherDevice
	b.svc.keys.hostKey = bytes.Repeat([]byte{9}, hostKeySize)
	fromA, err := a.svc.PairExport(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.svc.PairExport(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	offerA, err := a.svc.PairCheck(ctx, ":1.10", fromB)
	if err != nil {
		t.Fatal(err)
	}
	offerB, err := b.svc.PairCheck(ctx, ":1.10", fromA)
	if err != nil {
		t.Fatal(err)
	}
	if offerA.Code != offerB.Code || offerA.Code == "" {
		t.Fatalf("the devices show different codes: %q and %q", offerA.Code, offerB.Code)
	}
	if _, err := a.svc.PairAdd(ctx, ":1.10", fromB, offerA.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.PairAdd(ctx, ":1.10", fromA, offerB.Code); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestPairAndImportFromOtherDevice(t *testing.T) {
	a, b := pairedDevices(t)
	ctx := context.Background()
	if got := a.sys.asked; len(got) != 2 || got[0] != pairAction+" " || got[1] != pairAction+" " {
		t.Errorf("password checks = %v, want one for the file and one for the pairing", got)
	}
	list, err := a.svc.PairList(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].User != "tester" || list[0].Hostname != "gjallarOS" || !list[0].Added.Equal(a.now) {
		t.Fatalf("pairings = %+v", list)
	}
	if other, err := a.svc.PairList(ctx, ":1.12"); err != nil || len(other) != 0 {
		t.Errorf("another account sees pairings %+v, %v", other, err)
	}

	before := a.value(t, "timezone")
	changeTimezone(t, b)
	setValue(t, b, "hostname", `"other-name"`)
	data, err := b.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := a.svc.Import(ctx, ":1.10", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range pending.Keys {
		if device, _ := config.DeviceScoped(key.Key); device {
			t.Errorf("the import carries the device setting %s", key.Key)
		}
	}
	if len(pending.Keys) != 1 || pending.Keys[0].Key != "timezone" || string(pending.Keys[0].Old) != before {
		t.Fatalf("import asks for %+v, want only the timezone", pending.Keys)
	}
	if len(pending.Notes) != 1 || !strings.Contains(pending.Notes[0], `paired device that says it is "gjallarOS"`) ||
		!strings.Contains(pending.Notes[0], "settings of that device were left out") {
		t.Errorf("notes = %q", pending.Notes)
	}
	if _, err := a.svc.Import(ctx, ":1.12", data); !errors.Is(err, errRefused) ||
		!strings.Contains(err.Error(), settings.ErrUnknownSigner.Error()) {
		t.Errorf("an account that did not pair imports: %v", err)
	}

	if err := a.svc.PairRemove(ctx, ":1.10", list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Import(ctx, ":1.10", data); !errors.Is(err, errRefused) {
		t.Errorf("import after the pairing was removed: %v", err)
	}
	if _, err := os.Stat(a.svc.keys.pairPath(1000)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the empty pairing record stays: %v", err)
	}
	if err := a.svc.PairRemove(ctx, ":1.10", list[0].ID); !errors.Is(err, errRefused) {
		t.Errorf("removing it twice: %v", err)
	}
}

// setValue writes one key of the fixture's configuration directly, as an
// earlier applied change would have.
func setValue(t *testing.T, f *fixture, key, value string) {
	t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields[key] = json.RawMessage(value)
	if data, err = json.MarshalIndent(fields, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPairAddRefuses(t *testing.T) {
	a, b := newFixture(t), newFixture(t)
	withKeys(t, a)
	withKeys(t, b)
	b.svc.device = otherDevice
	b.svc.keys.hostKey = bytes.Repeat([]byte{9}, hostKeySize)
	ctx := context.Background()
	fromA, err := a.svc.PairExport(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.svc.PairExport(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.svc.PairCheck(ctx, ":1.12", fromB); !errors.Is(err, errNoOwnKey) {
		t.Errorf("an account without a key: %v, want errNoOwnKey", err)
	}
	if _, err := a.svc.PairCheck(ctx, ":1.10", fromA); !errors.Is(err, errRefused) ||
		!strings.Contains(err.Error(), "made on this device") {
		t.Errorf("own pairing file: %v", err)
	}
	b.svc.device = testDevice
	sameDevice, err := b.svc.PairExport(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	b.svc.device = otherDevice
	if _, err := a.svc.PairCheck(ctx, ":1.10", sameDevice); !errors.Is(err, errRefused) ||
		!strings.Contains(err.Error(), "same device id") {
		t.Errorf("file of a copied system with this device id: %v", err)
	}
	if _, err := a.svc.PairCheck(ctx, ":1.10", []byte("SYNTHETIC not a pairing file")); !errors.Is(err, errRefused) {
		t.Errorf("not a pairing file: %v", err)
	}

	offer, err := a.svc.PairCheck(ctx, ":1.10", fromB)
	if err != nil {
		t.Fatal(err)
	}
	asked := len(a.sys.asked)
	if _, err := a.svc.PairAdd(ctx, ":1.10", fromB, strings.Replace(offer.Code, offer.Code[:1], "x", 1)); !errors.Is(err, errRefused) {
		t.Errorf("wrong code: %v", err)
	}
	if len(a.sys.asked) != asked {
		t.Error("asked for a password for a code that does not match")
	}
	a.sys.allow = false
	if _, err := a.svc.PairAdd(ctx, ":1.10", fromB, offer.Code); !errors.Is(err, errDenied) {
		t.Errorf("wrong password: %v", err)
	}
	if list, _ := a.svc.PairList(ctx, ":1.10"); len(list) != 0 {
		t.Errorf("pairings after refusals = %+v", list)
	}
}

func TestPairingsGoWithTheirUser(t *testing.T) {
	a, b := pairedDevices(t)
	ctx := context.Background()
	changeTimezone(t, b)
	data, err := b.svc.Export(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	lookup := a.svc.keys.userName
	a.svc.keys.userName = func(uid uint32) (string, error) {
		if uid == 1000 {
			return "newcomer", nil
		}
		return lookup(uid)
	}
	if _, err := a.svc.Import(ctx, ":1.10", data); !errors.Is(err, errRefused) {
		t.Errorf("a later account on the uid imports with the pairing: %v", err)
	}
	if _, err := os.Stat(a.svc.keys.pairPath(1000)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the pairing record of the earlier account stays: %v", err)
	}
}

// Pairing again after the other device got a new key replaces the entry.
func TestPairAgainReplaces(t *testing.T) {
	a, b := pairedDevices(t)
	ctx := context.Background()
	b.svc.keys.hostKey = bytes.Repeat([]byte{10}, hostKeySize)
	fromB, err := b.svc.PairExport(ctx, ":1.10")
	if err != nil {
		t.Fatal(err)
	}
	offer, err := a.svc.PairCheck(ctx, ":1.10", fromB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.PairAdd(ctx, ":1.10", fromB, offer.Code); err != nil {
		t.Fatal(err)
	}
	list, err := a.svc.PairList(ctx, ":1.10")
	if err != nil || len(list) != 1 || list[0].ID != offer.ID {
		t.Fatalf("pairings = %+v, %v; want only the new key", list, err)
	}
}

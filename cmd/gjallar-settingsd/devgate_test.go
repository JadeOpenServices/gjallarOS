package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// EXAMPLE proofs: the fake helper takes PIN 2468 and recovery key
// example-recovery, and counts its tries like gjallar-devproof.
func (f *fixture) fakeProof() *[]string {
	var asked []string
	left := devgate.PINTries
	f.svc.askProof = func(_ context.Context, kind string, secret []byte) (devgate.Reply, error) {
		asked = append(asked, kind)
		switch {
		case kind == devgate.ProofPIN && bytes.Equal(secret, []byte("2468")):
			return devgate.Reply{OK: true, PINLeft: left}, nil
		case kind == devgate.ProofRecovery && bytes.Equal(secret, []byte("example-recovery")):
			return devgate.Reply{OK: true, PINLeft: left}, nil
		case kind == devgate.ProofPIN:
			left--
			return devgate.Reply{PINLeft: left}, devgate.ErrWrong
		}
		return devgate.Reply{PINLeft: left}, devgate.ErrWrong
	}
	return &asked
}

func (f *fixture) grant(t *testing.T) devgate.Grant {
	t.Helper()
	g, err := devgate.Load(f.svc.grantPath)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// applyCritical waits out the security delay at home and applies.
func (f *fixture) applyCritical(t *testing.T, id string) error {
	t.Helper()
	f.sys.home = true
	f.now = f.now.Add(settings.SecurityDelay)
	return f.svc.Apply(context.Background(), ":1.10", id, "")
}

func (f *fixture) setChannel(t *testing.T, channel string) {
	t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(channel)
	updated, _, err := config.ChangeText(data, map[string]json.RawMessage{"releaseChannel": value})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, updated, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDevChannelNeedsProof(t *testing.T) {
	f := newFixture(t)
	asked := f.fakeProof()
	ctx := context.Background()

	_, err := f.svc.Request(ctx, ":1.10", `{"releaseChannel":"dev"}`)
	if !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "gjallarctl settings set asks for it") {
		t.Fatalf("dev channel without proof: %v", err)
	}
	f.sys.allow = false
	if _, err := f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, devgate.ProofPIN, []byte("1111")); !errors.Is(err, errDenied) {
		t.Fatalf("proof without password: %v", err)
	}
	if len(*asked) != 0 {
		t.Fatalf("the PIN was tested before the password: %v", *asked)
	}
	f.sys.allow = true

	_, err = f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, devgate.ProofPIN, []byte("1111"))
	if !errors.Is(err, errDenied) || !strings.Contains(err.Error(), "2 tries left") {
		t.Fatalf("wrong PIN: %v", err)
	}
	secret := []byte("2468")
	change, err := f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, devgate.ProofPIN, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secret, make([]byte, len(secret))) {
		t.Fatal("the PIN was not cleared")
	}
	if change.Proof != devgate.ProofPIN {
		t.Fatalf("change proof = %q", change.Proof)
	}
	if !f.grant(t).Empty() {
		t.Fatal("the grant was written before Apply")
	}
	if err := f.applyCritical(t, change.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.value(t, "releaseChannel"); got != `"dev"` {
		t.Fatalf("releaseChannel = %s", got)
	}
	if g := f.grant(t); !g.Channels || g.DeveloperMode || g.Proof != devgate.ProofPIN || !g.Time.Equal(f.now) {
		t.Fatalf("grant = %+v", g)
	}

	// Staging is covered by the same grant; developer mode is not.
	change, err = f.svc.Request(ctx, ":1.10", `{"releaseChannel":"staging"}`)
	if err != nil {
		t.Fatalf("staging with a channel grant: %v", err)
	}
	if err := f.applyCritical(t, change.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Request(ctx, ":1.10", `{"developerMode":true}`); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "developer mode needs") {
		t.Fatalf("developer mode on a channel grant: %v", err)
	}
	change, err = f.svc.RequestWithProof(ctx, ":1.10", `{"developerMode":true}`, devgate.ProofRecovery, []byte("example-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.applyCritical(t, change.ID); err != nil {
		t.Fatal(err)
	}
	if g := f.grant(t); !g.Channels || !g.DeveloperMode || g.Proof != devgate.ProofRecovery {
		t.Fatalf("grant after developer mode = %+v", g)
	}

	// Going back needs no proof and drops the grant, so the next time
	// asks again.
	change, err = f.svc.Request(ctx, ":1.10", `{"releaseChannel":"stable","developerMode":false}`)
	if err != nil {
		t.Fatalf("back to stable: %v", err)
	}
	if err := f.applyCritical(t, change.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.svc.grantPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("grant left after going back to stable: %v", err)
	}
	if _, err := f.svc.Request(ctx, ":1.10", `{"releaseChannel":"dev"}`); !errors.Is(err, errRefused) {
		t.Fatalf("dev again without proof: %v", err)
	}
}

func TestWrongRecoveryKeyAndSpentPINs(t *testing.T) {
	f := newFixture(t)
	f.fakeProof()
	ctx := context.Background()
	_, err := f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, devgate.ProofRecovery, []byte("nope"))
	if !errors.Is(err, errDenied) || !strings.Contains(err.Error(), "wrong recovery key") {
		t.Fatalf("wrong recovery key: %v", err)
	}
	for range devgate.PINTries {
		_, err = f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, devgate.ProofPIN, []byte("0000"))
	}
	if !errors.Is(err, errDenied) || !strings.Contains(err.Error(), "the recovery key still works") {
		t.Fatalf("last wrong PIN: %v", err)
	}
	if _, err := f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, "password", []byte("x")); !errors.Is(err, errRefused) {
		t.Fatalf("unknown proof kind: %v", err)
	}
	f.svc.askProof = func(context.Context, string, []byte) (devgate.Reply, error) {
		return devgate.Reply{Error: "the system disk is not encrypted"}, errors.New("the system disk is not encrypted")
	}
	if _, err := f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, devgate.ProofPIN, []byte("2468")); !errors.Is(err, errRefused) {
		t.Fatalf("helper refusal: %v", err)
	}
	if len(f.svc.pending) != 0 {
		t.Fatalf("failed proofs left changes: %v", f.svc.pending)
	}
}

func TestOldDevDeviceConfirmsOnce(t *testing.T) {
	f := newFixture(t)
	asked := f.fakeProof()
	ctx := context.Background()
	if err := f.svc.ConfirmDeveloper(ctx, ":1.10", devgate.ProofPIN, []byte("2468")); !errors.Is(err, errRefused) {
		t.Fatalf("confirm on stable: %v", err)
	}
	// A device set to staging before the gate has no grant.
	f.setChannel(t, "staging")
	_, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"Example City"}`)
	if !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "confirm-developer") {
		t.Fatalf("everyday change without a grant: %v", err)
	}
	if len(*asked) != 0 {
		t.Fatalf("an everyday change asked for a proof: %v", *asked)
	}
	f.sys.allow = false
	if err := f.svc.ConfirmDeveloper(ctx, ":1.10", devgate.ProofPIN, []byte("2468")); !errors.Is(err, errDenied) || len(*asked) != 0 {
		t.Fatalf("confirm without password: %v, proofs %v", err, *asked)
	}
	f.sys.allow = true
	if err := f.svc.ConfirmDeveloper(ctx, ":1.10", devgate.ProofPIN, []byte("2468")); err != nil {
		t.Fatal(err)
	}
	if g := f.grant(t); !g.Channels || g.DeveloperMode {
		t.Fatalf("confirmed grant = %+v", g)
	}
	if len(f.sys.rebuilt) != 0 {
		t.Fatalf("confirm started a rebuild: %v", f.sys.rebuilt)
	}
	if _, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"Example City"}`); err != nil {
		t.Fatalf("everyday change after confirm: %v", err)
	}
}

func TestApplyRefusesLostGrant(t *testing.T) {
	f := newFixture(t)
	f.setChannel(t, "dev")
	if err := devgate.Save(f.svc.grantPath, devgate.Grant{Channels: true, Proof: devgate.ProofPIN}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"Example City"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.svc.grantPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply after the grant broke: %v", err)
	}
	if got := f.value(t, "weatherCity"); got == `"Example City"` || len(f.sys.rebuilt) != 0 {
		t.Fatalf("a change without a grant was written: %s, rebuilds %v", got, f.sys.rebuilt)
	}
}

func TestGrantWriteFailureStopsRebuild(t *testing.T) {
	f := newFixture(t)
	f.fakeProof()
	f.svc.grantPath = "/nonexistent/developer-grant.json"
	ctx := context.Background()
	change, err := f.svc.RequestWithProof(ctx, ":1.10", `{"releaseChannel":"dev"}`, devgate.ProofPIN, []byte("2468"))
	if err != nil {
		t.Fatal(err)
	}
	err = f.applyCritical(t, change.ID)
	if err == nil || !strings.Contains(err.Error(), "the rebuild did not start") {
		t.Fatalf("grant write failure: %v", err)
	}
	if len(f.sys.rebuilt) != 0 {
		t.Fatalf("rebuild started without a grant: %v", f.sys.rebuilt)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

// EMULATED disk: keyslot 0 recovery, keyslot 1 TPM2 with PIN.
func fakeChecker(t *testing.T) (*checker, *int, *[]time.Duration) {
	t.Helper()
	pinTests := 0
	var slept []time.Duration
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	c := &checker{
		budgetPath:   filepath.Join(t.TempDir(), "budget.json"),
		now:          func() time.Time { return now },
		sleep:        func(d time.Duration) { slept = append(slept, d) },
		device:       func(context.Context) (string, error) { return "/dev/example", nil },
		tpmAvailable: func() bool { return true },
		keyslots: func(context.Context, string) (diskcrypto.Keyslots, error) {
			return diskcrypto.Keyslots{All: []int{0, 1}, TPM2: []int{1}, TPM2PIN: true, TPM2Token: map[int]string{1: "0"}, Recovery: []int{0}}, nil
		},
		testPIN: func(_ context.Context, device, token string, pin []byte) error {
			pinTests++
			if device == "/dev/example" && token == "0" && bytes.Equal(pin, []byte("2468")) {
				return nil
			}
			return errors.New("exit status 2")
		},
		testSlot: func(_ context.Context, _ string, slot int, key []byte) error {
			if slot == 0 && bytes.Equal(key, []byte("example-recovery")) {
				return nil
			}
			return errors.New("exit status 2")
		},
	}
	return c, &pinTests, &slept
}

func TestPINBudget(t *testing.T) {
	c, tests, _ := fakeChecker(t)
	ctx := context.Background()
	if r := c.check(ctx, devgate.ProofPIN, []byte("2468")); !r.OK || r.PINLeft != devgate.PINTries {
		t.Fatalf("right PIN: %+v", r)
	}
	for i := range devgate.PINTries {
		r := c.check(ctx, devgate.ProofPIN, []byte("1111"))
		if r.OK || r.Error != "" || r.PINLeft != devgate.PINTries-1-i {
			t.Fatalf("wrong PIN %d: %+v", i, r)
		}
	}
	before := *tests
	r := c.check(ctx, devgate.ProofPIN, []byte("2468"))
	if r.OK || r.Error == "" || r.Retry.IsZero() || *tests != before {
		t.Fatalf("spent budget still tested the chip: %+v, tests %d -> %d", r, before, *tests)
	}
	if r := c.check(ctx, devgate.ProofRecovery, []byte("example-recovery")); !r.OK {
		t.Fatalf("recovery key refused after spent PIN budget: %+v", r)
	}
}

func TestWrongRecoveryWaits(t *testing.T) {
	c, _, slept := fakeChecker(t)
	r := c.check(context.Background(), devgate.ProofRecovery, []byte("nope"))
	if r.OK || r.Error != "" || len(*slept) != 1 || (*slept)[0] != wrongKeyDelay {
		t.Fatalf("wrong recovery key: %+v, slept %v", r, *slept)
	}
}

func TestNoPINOrRefusals(t *testing.T) {
	c, tests, _ := fakeChecker(t)
	c.keyslots = func(context.Context, string) (diskcrypto.Keyslots, error) {
		return diskcrypto.Keyslots{All: []int{0, 1}, TPM2: []int{1}, TPM2Token: map[int]string{1: "0"}, Recovery: []int{0}}, nil
	}
	ctx := context.Background()
	if r := c.check(ctx, devgate.ProofPIN, []byte("2468")); r.OK || r.Error == "" || *tests != 0 {
		t.Fatalf("PIN without a PIN token: %+v", r)
	}
	if r := c.check(ctx, "password", []byte("x")); r.Error != "unknown proof" {
		t.Fatalf("unknown kind: %+v", r)
	}
	if r := c.check(ctx, devgate.ProofRecovery, nil); r.Error != "empty proof" {
		t.Fatalf("empty proof: %+v", r)
	}
	c.device = func(context.Context) (string, error) { return "", nil }
	if r := c.check(ctx, devgate.ProofRecovery, []byte("example-recovery")); r.OK || r.Error == "" {
		t.Fatalf("unencrypted disk: %+v", r)
	}
}

func TestUnencryptedDisk(t *testing.T) {
	c, tests, _ := fakeChecker(t)
	c.device = func(context.Context) (string, error) { return "", nil }
	r := c.check(context.Background(), devgate.ProofPIN, []byte("2468"))
	if r.OK || r.Error != devgate.Unencrypted || *tests != 0 {
		t.Fatalf("%+v, %d PIN tests", r, *tests)
	}
}

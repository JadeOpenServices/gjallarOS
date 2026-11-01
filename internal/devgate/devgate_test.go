package devgate

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNeedsAndCovers(t *testing.T) {
	cases := []struct {
		channel string
		dev     bool
		grant   Grant
		ok      bool
	}{
		{"stable", false, Grant{}, true},
		{"", false, Grant{}, true},
		{"staging", false, Grant{}, false},
		{"dev", false, Grant{Channels: true}, true},
		{"stable", true, Grant{Channels: true}, false},
		{"stable", true, Grant{DeveloperMode: true}, true},
		{"dev", true, Grant{DeveloperMode: true}, false},
		{"dev", true, Grant{Channels: true, DeveloperMode: true}, true},
	}
	for _, c := range cases {
		if got := c.grant.Covers(Needs(c.channel, c.dev)); got != c.ok {
			t.Errorf("%+v covers %s/%v = %v, want %v", c.grant, c.channel, c.dev, got, c.ok)
		}
	}
}

func TestSaveLoadRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grant.json")
	if g, err := Load(path); err != nil || !g.Empty() {
		t.Fatalf("missing grant = %+v, %v", g, err)
	}
	want := Grant{Channels: true, Proof: ProofPIN, Time: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("grant mode %v, %v", info, err)
	}
	got, err := Load(path)
	if err != nil || got != want {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if err := Save(path, Grant{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty grant left a file: %v", err)
	}
	if err := Save(path, Grant{}); err != nil {
		t.Fatalf("removing a missing grant: %v", err)
	}
}

func TestCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grant.json")
	if err := Check(path, "stable", false); err != nil {
		t.Fatal(err)
	}
	err := Check(path, "staging", true)
	if err == nil || !strings.Contains(err.Error(), "staging channel and developer mode") {
		t.Fatalf("no grant: %v", err)
	}
	if err := Save(path, Grant{Channels: true}); err != nil {
		t.Fatal(err)
	}
	if err := Check(path, "dev", false); err != nil {
		t.Fatal(err)
	}
	if err := Check(path, "dev", true); err == nil || !strings.HasPrefix(err.Error(), "developer mode needs") {
		t.Fatalf("channel grant allowed developer mode: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check(path, "dev", false); err == nil {
		t.Fatal("broken grant allowed dev")
	}
}

func TestBudget(t *testing.T) {
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var b Budget
	for i := range PINTries {
		if left, _ := b.Left(start); left != PINTries-i {
			t.Fatalf("try %d: left %d", i, left)
		}
		b.Fail(start.Add(time.Duration(i) * time.Minute))
	}
	now := start.Add(10 * time.Minute)
	left, retry := b.Left(now)
	if left != 0 || !retry.Equal(start.Add(PINWindow)) {
		t.Fatalf("spent budget: left %d retry %v", left, retry)
	}
	if left, _ := b.Left(start.Add(PINWindow)); left != 1 {
		t.Fatalf("after the window: left %d", left)
	}
	b.Undo()
	if left, _ := b.Left(now); left != 1 {
		t.Fatalf("after undo: left %d", left)
	}
	// A clock set back keeps counting.
	if left, _ := b.Left(start.Add(-time.Hour)); left != 1 {
		t.Fatalf("clock back: left %d", left)
	}
}

func TestBudgetFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "budget.json")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	b, err := LoadBudget(path, now)
	if err != nil || len(b.PINFails) != 0 {
		t.Fatalf("missing budget: %+v, %v", b, err)
	}
	b.Fail(now)
	if err := SaveBudget(path, b); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadBudget(path, now); err != nil || len(got.PINFails) != 1 {
		t.Fatalf("saved budget: %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBudget(path, now)
	if left, _ := got.Left(now); err == nil || left != 0 {
		t.Fatalf("broken budget: left %d, %v", left, err)
	}
}

func TestAskServe(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "s")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			Serve(context.Background(), conn, conn, func(_ context.Context, kind string, secret []byte) Reply {
				switch {
				case kind == ProofPIN && bytes.Equal(secret, []byte("123456")):
					return Reply{OK: true, PINLeft: 3}
				case kind == ProofPIN:
					return Reply{PINLeft: 2}
				}
				return Reply{Error: "unknown proof"}
			})
			conn.Close()
		}
	}()
	ctx := context.Background()
	if r, err := Ask(ctx, socket, ProofPIN, []byte("123456")); err != nil || !r.OK {
		t.Fatalf("right PIN: %+v, %v", r, err)
	}
	if r, err := Ask(ctx, socket, ProofPIN, []byte("1")); !errors.Is(err, ErrWrong) || r.PINLeft != 2 {
		t.Fatalf("wrong PIN: %+v, %v", r, err)
	}
	if _, err := Ask(ctx, socket, "x", []byte("1")); err == nil || err.Error() != "unknown proof" {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestServeRejectsLongQuestion(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(`{"kind":"pin","secret":"` + strings.Repeat("A", 2*maxMessage) + `"}` + "\n")
	called := false
	Serve(context.Background(), in, &out, func(context.Context, string, []byte) Reply { called = true; return Reply{OK: true} })
	if called || !strings.Contains(out.String(), "unreadable question") {
		t.Fatalf("long question: called %v, reply %s", called, out.String())
	}
}

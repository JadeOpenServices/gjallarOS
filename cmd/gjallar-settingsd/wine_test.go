package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

func wineFixture(t *testing.T) (*fixture, *int) {
	t.Helper()
	f := newFixture(t)
	f.svc.wineDir = filepath.Join(t.TempDir(), "wine")
	names := map[uint32]string{1000: "synthetic-a", 1001: "synthetic-b"}
	f.svc.userName = func(uid uint32) (string, error) {
		name, ok := names[uid]
		if !ok {
			return "", errNoAccount
		}
		return name, nil
	}
	changed := new(int)
	f.svc.wineChanged = func() { *changed++ }
	return f, changed
}

func TestWineOnAsksOffDoesNot(t *testing.T) {
	f, changed := wineFixture(t)
	ctx := context.Background()

	programs, err := f.svc.SetWine(ctx, ":1.10", "notepad", winesandbox.Flags{Internet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !programs["notepad"].Internet || len(f.sys.asked) != 1 || f.sys.asked[0] != wineAction+" " {
		t.Fatalf("programs %v, asked %v", programs, f.sys.asked)
	}
	info, err := os.Stat(f.svc.winePath(1000))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store file: %v %v", info, err)
	}

	// Another session of the same user sees it; another user does not.
	if got, err := f.svc.Wine(ctx, ":1.11"); err != nil || !got["notepad"].Internet {
		t.Errorf("same user reads %v, %v", got, err)
	}
	if got, err := f.svc.Wine(ctx, ":1.12"); err != nil || len(got) != 0 {
		t.Errorf("other user reads %v, %v", got, err)
	}

	// Home on asks again; both off asks nothing and removes the file.
	if _, err := f.svc.SetWine(ctx, ":1.10", "notepad", winesandbox.Flags{Internet: true, Home: true}); err != nil {
		t.Fatal(err)
	}
	f.sys.allow = false
	if _, err := f.svc.SetWine(ctx, ":1.10", "notepad", winesandbox.Flags{Home: true}); err != nil {
		t.Fatalf("turning off asked: %v", err)
	}
	if _, err := f.svc.SetWine(ctx, ":1.10", "notepad", winesandbox.Flags{}); err != nil {
		t.Fatal(err)
	}
	if len(f.sys.asked) != 2 || *changed != 4 {
		t.Errorf("asked %v, changed %d", f.sys.asked, *changed)
	}
	if _, err := os.Stat(f.svc.winePath(1000)); !os.IsNotExist(err) {
		t.Errorf("empty store kept: %v", err)
	}
}

func TestWineDenied(t *testing.T) {
	f, changed := wineFixture(t)
	f.sys.allow = false
	if _, err := f.svc.SetWine(context.Background(), ":1.10", "notepad", winesandbox.Flags{Home: true}); !errors.Is(err, errDenied) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := f.svc.Wine(context.Background(), ":1.10"); len(got) != 0 || *changed != 0 {
		t.Errorf("denied change stored: %v, changed %d", got, *changed)
	}
}

func TestWineRefusesBadIDAndUnknownCaller(t *testing.T) {
	f, _ := wineFixture(t)
	ctx := context.Background()
	for _, id := range []string{"", "../x", "Notepad", "a b"} {
		if _, err := f.svc.SetWine(ctx, ":1.10", id, winesandbox.Flags{Internet: true}); !errors.Is(err, errRefused) {
			t.Errorf("id %q: err = %v", id, err)
		}
	}
	if _, err := f.svc.Wine(ctx, ":1.99"); err == nil {
		t.Error("unknown caller read switches")
	}
	if len(f.sys.asked) != 0 {
		t.Errorf("asked %v", f.sys.asked)
	}
}

func TestWineLimit(t *testing.T) {
	f, _ := wineFixture(t)
	ctx := context.Background()
	for i := range maxWinePrograms {
		if _, err := f.svc.SetWine(ctx, ":1.10", fmt.Sprintf("app-%d", i), winesandbox.Flags{Internet: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.SetWine(ctx, ":1.10", "one-more", winesandbox.Flags{Internet: true}); !errors.Is(err, errRefused) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.svc.SetWine(ctx, ":1.10", "app-0", winesandbox.Flags{}); err != nil {
		t.Fatalf("turning off at the limit: %v", err)
	}
}

func TestWineDropsRemovedUser(t *testing.T) {
	f, _ := wineFixture(t)
	ctx := context.Background()
	if _, err := f.svc.SetWine(ctx, ":1.10", "notepad", winesandbox.Flags{Internet: true}); err != nil {
		t.Fatal(err)
	}
	// A new account got uid 1000.
	f.svc.userName = func(uint32) (string, error) { return "synthetic-new", nil }
	got, err := f.svc.Wine(ctx, ":1.10")
	if err != nil || len(got) != 0 {
		t.Fatalf("new account reads %v, %v", got, err)
	}
	if _, err := os.Stat(f.svc.winePath(1000)); !os.IsNotExist(err) {
		t.Errorf("old account's file kept: %v", err)
	}
}

func TestWineOpens(t *testing.T) {
	for _, c := range []struct {
		have, want winesandbox.Flags
		out        string
	}{
		{winesandbox.Flags{}, winesandbox.Flags{}, ""},
		{winesandbox.Flags{Internet: true}, winesandbox.Flags{}, ""},
		{winesandbox.Flags{}, winesandbox.Flags{Internet: true}, "the internet"},
		{winesandbox.Flags{Internet: true}, winesandbox.Flags{Home: true}, "your home network and tailnet"},
		{winesandbox.Flags{}, winesandbox.Flags{Internet: true, Home: true}, "the internet and your home network"},
	} {
		if got := wineOpens(c.have, c.want); got != c.out {
			t.Errorf("%v -> %v: %q, want %q", c.have, c.want, got, c.out)
		}
	}
}

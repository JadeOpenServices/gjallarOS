package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// SYNTHETIC colors, not taken from any session.
const testColors = "base00=101010,base01=111111,base02=121212,base03=131313," +
	"base04=141414,base05=151515,base06=161616,base07=171717," +
	"base08=181818,base09=191919,base0A=1a1a1a,base0B=1b1b1b," +
	"base0C=1c1c1c,base0D=1d1d1d,base0E=1e1e1e,base0F=1f1f1f"

type fakeSystem struct {
	callers    map[string]caller
	allow      bool
	home       bool
	rebuilding bool
	asked      []string
	rebuilt    []string
}

func (f *fakeSystem) Caller(_ context.Context, sender string) (caller, error) {
	who, ok := f.callers[sender]
	if !ok {
		return caller{}, errRefused
	}
	return who, nil
}

func (f *fakeSystem) Authorize(_ context.Context, _, action string, details map[string]string) error {
	f.asked = append(f.asked, action+" "+details["org.gjallaros.keys"])
	if !f.allow {
		return errDenied
	}
	return nil
}

func (f *fakeSystem) Home() bool                               { return f.home }
func (f *fakeSystem) Rebuilding(context.Context) (bool, error) { return f.rebuilding, nil }
func (f *fakeSystem) StartRebuild(_ context.Context, colors string) error {
	f.rebuilt = append(f.rebuilt, colors)
	return nil
}

type fixture struct {
	sys  *fakeSystem
	svc  *service
	now  time.Time
	path string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "user.config.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		sys: &fakeSystem{allow: true, callers: map[string]caller{
			":1.10": {UID: 1000, Session: "2"},
			":1.11": {UID: 1000, Session: "5"},
			":1.12": {UID: 1001, Session: "3"},
		}},
		now:  time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		path: path,
	}
	f.svc = newService(f.sys, dir, func(string) error { return nil })
	f.svc.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) value(t *testing.T, key string) string {
	t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return string(fields[key])
}

func TestEverydayChangeAppliesAndRebuilds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"Example City"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sys.asked) != 0 {
		t.Fatalf("an everyday request asked for the password: %v", f.sys.asked)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, testColors); err != nil {
		t.Fatal(err)
	}
	if got := f.value(t, "weatherCity"); got != `"Example City"` {
		t.Fatalf("weatherCity = %s", got)
	}
	if len(f.sys.asked) != 1 || f.sys.asked[0] != "org.gjallaros.settings.everyday weatherCity" {
		t.Fatalf("password checks = %v", f.sys.asked)
	}
	if len(f.sys.rebuilt) != 1 || f.sys.rebuilt[0] != testColors {
		t.Fatalf("rebuilds = %v", f.sys.rebuilt)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("second apply: %v, want refused", err)
	}
}

func TestSecurityChangeWaits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"hostname":"desk-07"}`)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(settings.ReadingDelay)
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply away from home after 10 s: %v, want refused", err)
	}
	f.sys.home = true
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); err != nil {
		t.Fatalf("apply once home is proven: %v", err)
	}
	if got := f.value(t, "hostname"); got != `"desk-07"` {
		t.Fatalf("hostname = %s", got)
	}
}

func TestCriticalChangeAsksAtRequestAndWaitsAtHome(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.sys.allow = false
	if _, err := f.svc.Request(ctx, ":1.10", `{"secureBootPrompt":false}`); !errors.Is(err, errDenied) {
		t.Fatalf("critical request without password: %v, want not authorized", err)
	}
	f.sys.allow, f.sys.home = true, true
	change, err := f.svc.Request(ctx, ":1.10", `{"secureBootPrompt":false}`)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("critical apply at home after a minute: %v, want refused", err)
	}
	f.now = f.now.Add(settings.SecurityDelay)
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); err != nil {
		t.Fatal(err)
	}
}

func TestChangesBelongToTheirSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"Example City"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, sender := range []string{":1.11", ":1.12"} {
		if err := f.svc.Apply(ctx, sender, change.ID, ""); !errors.Is(err, errRefused) {
			t.Errorf("apply from %s: %v, want refused", sender, err)
		}
		if err := f.svc.Cancel(ctx, sender, change.ID); !errors.Is(err, errRefused) {
			t.Errorf("cancel from %s: %v, want refused", sender, err)
		}
		if list, _ := f.svc.Pending(ctx, sender); len(list) != 0 {
			t.Errorf("%s sees %d changes of another session", sender, len(list))
		}
	}
	f.svc.DropSession("2")
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply after the session ended: %v, want refused", err)
	}
}

func TestApplyRefusesChangedFileBadColorsAndRunningRebuild(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"Example City"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, "base00=../etc"); !errors.Is(err, errRefused) {
		t.Fatalf("bad colors: %v, want refused", err)
	}
	f.sys.rebuilding = true
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply during a rebuild: %v, want refused", err)
	}
	f.sys.rebuilding = false
	data, _ := os.ReadFile(f.path)
	moved, _, err := config.ChangeText(data, map[string]json.RawMessage{"weatherCity": json.RawMessage(`"Other City"`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, moved, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply over a changed file: %v, want refused", err)
	}
	if got := f.value(t, "weatherCity"); got != `"Other City"` {
		t.Fatalf("weatherCity = %s; the stale change was written", got)
	}
	if len(f.sys.rebuilt) != 0 {
		t.Fatalf("rebuilt %v without a written change", f.sys.rebuilt)
	}
}

func TestApplyRefusesBadCheckout(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc.checkout = func(string) error { return errors.New("the checkout is writable by uid 1000") }
	change, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"Example City"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); err == nil || !strings.Contains(err.Error(), "nothing written") {
		t.Fatalf("apply into a bad checkout: %v", err)
	}
}

func TestRequestLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := range maxPendingPerUser {
		if _, err := f.svc.Request(ctx, ":1.10", `{"weatherCity":"City `+string(rune('A'+i))+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Request(ctx, ":1.11", `{"weatherCity":"City E"}`); !errors.Is(err, errRefused) {
		t.Fatalf("request over the limit: %v, want refused", err)
	}
	for _, values := range []string{`[]`, `{"runRebuild":false}`, `{"weatherCity":` + strings.Repeat(" ", settings.MaxRequestBytes) + `"x"}`} {
		if _, err := f.svc.Request(ctx, ":1.12", values); !errors.Is(err, errRefused) {
			t.Errorf("request %.40q: %v, want refused", values, err)
		}
	}
	if _, err := f.svc.Request(ctx, ":1.99", `{"weatherCity":"City F"}`); err == nil {
		t.Fatal("accepted a caller without a session")
	}
}

func TestReadHomeTrustsOnlyFreshRootVerdict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vpn-trust.json")
	if err := os.WriteFile(path, []byte(`{"trust":"home","reason":"r","bypass":[],"exitNode":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if os.Geteuid() == 0 && !readHome(path, now) {
		t.Fatal("a fresh root verdict is not home")
	}
	if os.Geteuid() != 0 && readHome(path, now) {
		t.Fatal("a verdict not owned by root counts as home")
	}
	if readHome(path, now.Add(verdictMaxAge+time.Minute)) {
		t.Fatal("a stale verdict counts as home")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if readHome(link, now) {
		t.Fatal("a link counts as home")
	}
}

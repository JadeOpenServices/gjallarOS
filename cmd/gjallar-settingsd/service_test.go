package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
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

	device     string
	trusted    bool
	tailnet    nettrust.Tailnet
	tailnetErr error
	taken      map[string]netip.Addr
	probed     []string
	usbDisk    string
	usbArmed   bool
	usbErr     error

	verdicts  []nettrust.Verdict
	reapplied int
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

func (f *fakeSystem) Home() bool              { return f.home }
func (f *fakeSystem) Network() (string, bool) { return f.device, f.trusted }
func (f *fakeSystem) Tailnet(context.Context) (nettrust.Tailnet, error) {
	return f.tailnet, f.tailnetErr
}
func (f *fakeSystem) NameTaken(_ context.Context, device, name string) (netip.Addr, error) {
	f.probed = append(f.probed, device+" "+name)
	return f.taken[name], nil
}
func (f *fakeSystem) Rebuilding(context.Context) (bool, error) { return f.rebuilding, nil }
func (f *fakeSystem) SystemDiskOnUSB() string                  { return f.usbDisk }
func (f *fakeSystem) USBArmed() (bool, error)                  { return f.usbArmed, f.usbErr }

// Verdict hands out the queued verdicts in turn and repeats the last one.
func (f *fakeSystem) Verdict() (nettrust.Verdict, bool) {
	if len(f.verdicts) == 0 {
		return nettrust.Verdict{}, false
	}
	v := f.verdicts[0]
	if len(f.verdicts) > 1 {
		f.verdicts = f.verdicts[1:]
	}
	return v, true
}
func (f *fakeSystem) ReapplyVPN(context.Context) error {
	f.reapplied++
	return nil
}
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

func TestReadVerdictTrustsOnlyFreshRootVerdict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vpn-trust.json")
	if err := os.WriteFile(path, []byte(`{"trust":"home","reason":"r","bypass":[],"exitNode":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if verdict, ok := readVerdict(path, now); os.Geteuid() == 0 && (!ok || verdict.Trust != nettrust.TrustHome) {
		t.Fatal("a fresh root verdict is not home")
	}
	if _, ok := readVerdict(path, now); os.Geteuid() != 0 && ok {
		t.Fatal("a verdict not owned by root counts as home")
	}
	if _, ok := readVerdict(path, now.Add(verdictMaxAge+time.Minute)); ok {
		t.Fatal("a stale verdict counts as home")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := readVerdict(link, now); ok {
		t.Fatal("a link counts as home")
	}
}

// The peer names and addresses are SYNTHETIC.
func TestHostnameMustBeFree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.sys.tailnet = nettrust.Tailnet{Running: true, Peers: []nettrust.Peer{
		{HostName: "Desk", DNSName: "desk.example-tailnet.ts.net."},
	}}
	f.sys.device, f.sys.trusted = "eth0", true
	f.sys.taken = map[string]netip.Addr{"printer": netip.MustParseAddr("192.0.2.7")}

	if _, err := f.svc.Request(ctx, ":1.10", `{"hostname":"DESK"}`); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "desk.example-tailnet.ts.net") {
		t.Errorf("tailnet clash: %v", err)
	}
	if _, err := f.svc.Request(ctx, ":1.10", `{"hostname":"printer"}`); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "192.0.2.7") {
		t.Errorf("network clash: %v", err)
	}
	change, err := f.svc.Request(ctx, ":1.10", `{"hostname":"lab"}`)
	if err != nil || len(change.Notes) != 0 {
		t.Fatalf("free name: %v, notes %q", err, change.Notes)
	}
	if want := []string{"eth0 printer", "eth0 lab"}; !slices.Equal(f.sys.probed, want) {
		t.Errorf("probed %q, want %q", f.sys.probed, want)
	}

	// Another machine takes the name during the wait.
	f.sys.taken["lab"] = netip.MustParseAddr("192.0.2.8")
	f.now = f.now.Add(2 * time.Hour)
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "192.0.2.8") {
		t.Fatalf("apply after the name was taken: %v", err)
	}
	if len(f.sys.rebuilt) != 0 || f.value(t, "hostname") == `"lab"` {
		t.Fatal("wrote a name that is taken")
	}
}

func TestHostnameNotes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"hostname":"lab"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"hostname not checked on the tailnet: Tailscale is not running",
		"hostname not checked on this network: it is not trusted",
	}
	if !slices.Equal(change.Notes, want) || len(f.sys.probed) != 0 {
		t.Errorf("notes %q, probed %q", change.Notes, f.sys.probed)
	}
}

func TestSlowBuildNote(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	change, err := f.svc.Request(ctx, ":1.10", `{"dolphinDriveUsage":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Notes) != 1 || !strings.Contains(change.Notes[0], "can take hours") {
		t.Errorf("notes %q", change.Notes)
	}
	setValue(t, f, "dolphinDriveUsage", "true")
	if change, err = f.svc.Request(ctx, ":1.12", `{"dolphinDriveUsage":false}`); err != nil || len(change.Notes) != 0 {
		t.Errorf("turning it off: notes %q, %v", change.Notes, err)
	}
}

func TestHostnameCheckFailsClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.sys.tailnetErr = errors.New("tailscaled does not answer")
	if _, err := f.svc.Request(ctx, ":1.10", `{"hostname":"lab"}`); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "could not check the tailnet") {
		t.Errorf("failed check: %v", err)
	}
	// A change of letter case only is the same name and asks no one.
	var old string
	if err := json.Unmarshal([]byte(f.value(t, "hostname")), &old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Request(ctx, ":1.10", `{"hostname":"`+strings.ToUpper(old)+`"}`); err != nil {
		t.Errorf("case change: %v", err)
	}
}

func TestUSBBlockingRefusedOnUSBSystemDisk(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.sys.usbDisk = "the system disk (/dev/sda) is a USB drive, and blocking could cut off the disk the system runs from."
	review := `"usbguardEnable":true,"usbTrustTpmHandle":"` + config.DefaultUSBTrustTPMHandle + `"`
	if _, err := f.svc.Request(ctx, ":1.10", `{`+review+`,"usbTrustEnforce":true}`); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "/dev/sda") {
		t.Fatalf("blocking on a USB system disk: %v", err)
	}
	if _, err := f.svc.Request(ctx, ":1.10", `{`+review+`}`); err != nil {
		t.Fatalf("review without blocking: %v", err)
	}
	f.sys.usbDisk = ""
	if _, err := f.svc.Request(ctx, ":1.11", `{`+review+`,"usbTrustEnforce":true}`); err != nil {
		t.Fatalf("blocking on an internal system disk: %v", err)
	}
}

func TestUSBBlockingStaysOnWhileArmed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["usbguardEnable"], fields["usbTrustEnforce"], fields["usbTrustTpmHandle"] = true, true, config.DefaultUSBTrustTPMHandle
	if data, err = json.Marshal(fields); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	f.sys.usbArmed = true
	for _, request := range []string{`{"usbTrustEnforce":false}`, `{"usbguardEnable":false,"usbTrustEnforce":false}`} {
		if _, err := f.svc.Request(ctx, ":1.10", request); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "gjallarctl usb disarm") {
			t.Fatalf("%s while armed: %v", request, err)
		}
	}
	f.sys.usbArmed, f.sys.usbErr = false, errors.New("decode USB trust state")
	if _, err := f.svc.Request(ctx, ":1.10", `{"usbTrustEnforce":false}`); !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("unreadable state: %v", err)
	}
	f.sys.usbErr = nil
	if _, err := f.svc.Request(ctx, ":1.10", `{"usbTrustEnforce":false}`); err != nil {
		t.Fatalf("after the disarm: %v", err)
	}
}

func TestReadSystemDiskNeedsRootCheck(t *testing.T) {
	dir := t.TempDir()
	if readSystemDisk(filepath.Join(dir, "missing.json")) == "" {
		t.Fatal("a missing boot check counts as an internal disk")
	}
	path := filepath.Join(dir, "system-disk.json")
	if err := os.WriteFile(path, []byte(`{"usbReason":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSystemDisk(path); (os.Geteuid() == 0) != (got == "") {
		t.Fatalf("reason %q from a file owned by uid %d", got, os.Geteuid())
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if readSystemDisk(path) == "" {
		t.Fatal("a check without a reason field counts as an internal disk")
	}
}

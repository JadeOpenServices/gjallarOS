package main

import (
	"bytes"
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

// fakeSettingsBus answers like gjallar-settingsd. Its keys, ids and times
// are SYNTHETIC.
type fakeSettingsBus struct {
	values   map[string]settings.KeyValue
	pending  []settings.Pending
	readyIn  time.Duration
	missing  bool
	requests []string
	applied  []string
	rebuilds int
	result   string
	notes    []string
	exported []byte
	imported [][]byte
}

var settingsTestNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func (b *fakeSettingsBus) Close() error { return nil }

func (b *fakeSettingsBus) Call(_ context.Context, method string, args ...any) (string, error) {
	if b.missing {
		return "", errSettingsMissing
	}
	answer := func(v any) (string, error) {
		data, err := json.Marshal(v)
		return string(data), err
	}
	switch method {
	case "Get":
		return answer(b.values[args[0].(string)])
	case "List":
		var list []settings.KeyValue
		for _, v := range b.values {
			list = append(list, v)
		}
		return answer(list)
	case "Request":
		request := args[0].(string)
		b.requests = append(b.requests, request)
		var values map[string]json.RawMessage
		if err := json.Unmarshal([]byte(request), &values); err != nil {
			return "", err
		}
		change := settings.Pending{
			Change: settings.Change{
				ID: "c0ffee00" + strings.Repeat("0", 24), Tier: config.TierEveryday,
				Asked: settingsTestNow,
			},
			ReadyAt: settingsTestNow.Add(b.readyIn),
			Notes:   b.notes,
		}
		for key, value := range values {
			kv := b.values[key]
			if kv.Tier == config.TierSecurity {
				change.Tier = kv.Tier
			}
			change.Keys = append(change.Keys, settings.KeyChange{Key: key, Old: kv.Value, New: value})
		}
		b.pending = append(b.pending, change)
		return answer(change)
	case "Import":
		b.imported = append(b.imported, args[0].([]byte))
		change := settings.Pending{
			Change: settings.Change{
				ID: "f11e0000" + strings.Repeat("0", 24), Tier: config.TierEveryday, Asked: settingsTestNow,
				Keys: []settings.KeyChange{{Key: "timezone", Old: json.RawMessage(`"Etc/UTC"`), New: json.RawMessage(`"UTC"`)}},
			},
			ReadyAt: settingsTestNow,
			Notes:   b.notes,
		}
		b.pending = append(b.pending, change)
		return answer(change)
	case "Pending":
		return answer(b.pending)
	case "Apply":
		b.applied = append(b.applied, args[0].(string))
		return "", nil
	case "Cancel":
		b.pending = nil
		return "", nil
	}
	return "", nil
}

func (b *fakeSettingsBus) Export(context.Context) ([]byte, error) {
	if b.missing {
		return nil, errSettingsMissing
	}
	return b.exported, nil
}

func (b *fakeSettingsBus) Rebuild(context.Context) (bool, string, error) {
	b.rebuilds++
	return b.rebuilds < 3, b.result, nil
}

func useFakeSettings(t *testing.T, bus *fakeSettingsBus) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	oldConnect, oldNow, oldSleep, oldEUID := settingsConnect, settingsNow, settingsSleep, settingsEUID
	t.Cleanup(func() {
		settingsConnect, settingsNow, settingsSleep, settingsEUID = oldConnect, oldNow, oldSleep, oldEUID
	})
	settingsConnect = func() (settingsBus, error) { return bus, nil }
	settingsNow = func() time.Time { return settingsTestNow }
	settingsSleep = func(context.Context, time.Duration) error { return nil }
	settingsEUID = func() int { return 1000 }
	if bus.values == nil {
		bus.values = map[string]settings.KeyValue{
			"touchpadWorkspaceSwipe": {Key: "touchpadWorkspaceSwipe", Tier: config.TierEveryday, Value: json.RawMessage(`true`)},
			"timezone":               {Key: "timezone", Tier: config.TierEveryday, Value: json.RawMessage(`"UTC"`)},
			"networkPrintingEnable":  {Key: "networkPrintingEnable", Tier: config.TierSecurity, Value: json.RawMessage(`false`)},
		}
	}
	if bus.result == "" {
		bus.result = "success"
	}
}

func TestSettingsSetEverydayAppliesAndWatches(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "set", "touchpadWorkspaceSwipe=false"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if len(bus.requests) != 1 || bus.requests[0] != `{"touchpadWorkspaceSwipe":false}` {
		t.Fatalf("requests %q", bus.requests)
	}
	if len(bus.applied) != 1 || bus.rebuilds != 3 {
		t.Fatalf("applied %q, rebuild polls %d", bus.applied, bus.rebuilds)
	}
	for _, want := range []string{"touchpadWorkspaceSwipe: true -> false", "the change is live"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
}

func TestSettingsSetFailedRebuild(t *testing.T) {
	bus := &fakeSettingsBus{result: "exit-code"}
	useFakeSettings(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "set", "touchpadWorkspaceSwipe=false"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(stderr.String(), "the rebuild failed (exit-code)") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestSettingsSetLongWaitLeavesItPending(t *testing.T) {
	bus := &fakeSettingsBus{readyIn: settings.SecurityDelay, notes: []string{"not checked here"}}
	useFakeSettings(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "set", "networkPrintingEnable=true"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if len(bus.applied) != 0 {
		t.Fatalf("applied %q before the wait", bus.applied)
	}
	for _, want := range []string{"(security)", "Note: not checked here", "not on its home network", "gjallarctl settings apply c0ffee00"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
}

func TestSettingsSetShortWaitApplies(t *testing.T) {
	bus := &fakeSettingsBus{readyIn: 10 * time.Second}
	useFakeSettings(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "set", "networkPrintingEnable=true"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if len(bus.applied) != 1 || !strings.Contains(stdout.String(), "applied in 10 s") {
		t.Fatalf("applied %q, stdout %s", bus.applied, stdout.String())
	}
}

func TestSettingsSetTextValue(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{
		{"timezone=Europe/Berlin", `{"timezone":"Europe/Berlin"}`},
		{"timezone=123", `{"timezone":"123"}`},
		{"timezone=true", `{"timezone":"true"}`},
	} {
		bus := &fakeSettingsBus{}
		useFakeSettings(t, bus)
		var stdout, stderr bytes.Buffer
		run([]string{"settings", "set", tc.arg}, &stdout, &stderr)
		if len(bus.requests) != 1 || bus.requests[0] != tc.want {
			t.Errorf("%s: requests %q", tc.arg, bus.requests)
		}
	}
}

func TestSettingsSetRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"settings", "set"},
		{"settings", "set", "timezone"},
		{"settings", "set", "timezone=UTC", "timezone=UTC"},
	} {
		bus := &fakeSettingsBus{}
		useFakeSettings(t, bus)
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 || len(bus.requests) != 0 {
			t.Errorf("%q: code %d, requests %q", args, code, bus.requests)
		}
	}
}

func TestSettingsPick(t *testing.T) {
	bus := &fakeSettingsBus{pending: []settings.Pending{
		{Change: settings.Change{ID: "abcd1111", Tier: config.TierEveryday}},
		{Change: settings.Change{ID: "abcd2222", Tier: config.TierEveryday}},
	}}
	useFakeSettings(t, bus)
	for _, tc := range []struct {
		args []string
		want string
		err  string
	}{
		{nil, "", "2 changes wait"},
		{[]string{"abcd"}, "", "fits 2 changes"},
		{[]string{"abcd2"}, "abcd2222", ""},
		{[]string{"ffff"}, "", "no change ffff"},
		{[]string{""}, "", "no change"},
	} {
		got, err := settingsPick(context.Background(), bus, tc.args)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: error %v, want %q", tc.args, err, tc.err)
			}
			continue
		}
		if err != nil || got.ID != tc.want {
			t.Errorf("%q: got %q, %v", tc.args, got.ID, err)
		}
	}
	bus.pending = bus.pending[:1]
	if got, err := settingsPick(context.Background(), bus, nil); err != nil || got.ID != "abcd1111" {
		t.Errorf("only change: got %q, %v", got.ID, err)
	}
}

func TestSettingsServiceMissing(t *testing.T) {
	bus := &fakeSettingsBus{missing: true}
	useFakeSettings(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "list"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(stderr.String(), "/etc/gjallar/system") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestSettingsRefusesRoot(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	settingsEUID = func() int { return 0 }
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "pending"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(stderr.String(), "as your own user") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestSettingsNamesSkipInstallerKeys(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "list", "--names"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	names := strings.Fields(stdout.String())
	tiers, err := config.Tiers()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
		if tiers[name] == config.TierInstaller {
			t.Errorf("installer key %s listed", name)
		}
	}
	if !seen["timezone"] || !seen["luksTpm2Pin"] || seen["unattendedInstall"] {
		t.Errorf("names %q", names)
	}
}

func TestSettingsGetShowsControlCharacters(t *testing.T) {
	bus := &fakeSettingsBus{values: map[string]settings.KeyValue{
		"timezone": {Key: "timezone", Tier: config.TierEveryday, Value: json.RawMessage("\"a\u202eb\"")},
	}}
	useFakeSettings(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "get", "timezone"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if strings.ContainsRune(stdout.String(), 0x202e) {
		t.Fatalf("raw escape in %q", stdout.String())
	}
}

func TestSettingsExportWritesNewPrivateFile(t *testing.T) {
	bus := &fakeSettingsBus{exported: []byte("gjallaros-settings-v1\nSYNTHETIC\n")}
	useFakeSettings(t, bus)
	path := filepath.Join(t.TempDir(), "settings.gjs")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "export", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, bus.exported) {
		t.Errorf("file = %q", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", info.Mode(), err)
	}
	stdout.Reset()
	if code := run([]string{"settings", "export", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("export over an existing file: code %d", code)
	}
}

func TestSettingsImportSendsFileAndApplies(t *testing.T) {
	bus := &fakeSettingsBus{notes: []string{"from a file tester made on this device on 2026-10-16 12:00:00, 3 days ago"}}
	useFakeSettings(t, bus)
	path := filepath.Join(t.TempDir(), "settings.gjs")
	content := []byte("gjallaros-settings-v1\nSYNTHETIC\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "import", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if len(bus.imported) != 1 || !bytes.Equal(bus.imported[0], content) {
		t.Fatalf("imported %q", bus.imported)
	}
	for _, want := range []string{`timezone: "Etc/UTC" -> "UTC"`, "Note: from a file tester made", "the change is live"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}

	big := filepath.Join(t.TempDir(), "big.gjs")
	if err := os.WriteFile(big, make([]byte, settings.MaxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"settings", "import", big}, &stdout, &stderr); code != 1 || len(bus.imported) != 1 {
		t.Fatalf("import of an oversized file: code %d, sent %d", code, len(bus.imported))
	}
}

// onTheSystemCheckout makes every config path count as the system
// checkout's.
func onTheSystemCheckout(t *testing.T) {
	t.Helper()
	old := onSystemCheckout
	onSystemCheckout = func(string) bool { return true }
	t.Cleanup(func() { onSystemCheckout = old })
}

func TestAddonsOnSystemCheckoutAskTheService(t *testing.T) {
	bus := &fakeSettingsBus{readyIn: settings.SecurityDelay, values: map[string]settings.KeyValue{
		"firefoxAddons": {Key: "firefoxAddons", Tier: config.TierSecurity, Value: json.RawMessage(`{}`)},
	}}
	useFakeSettings(t, bus)
	onTheSystemCheckout(t)
	path := addonsConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := addonsSet(path, []string{"passwords.passbolt", "locked"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(bus.requests) != 1 || !strings.HasPrefix(bus.requests[0], `{"firefoxAddons":{`) ||
		!strings.Contains(bus.requests[0], `"passbolt":"locked"`) {
		t.Fatalf("requests %q", bus.requests)
	}
	if len(bus.applied) != 0 || !strings.Contains(out.String(), "gjallarctl settings apply c0ffee00") {
		t.Fatalf("applied %q, out %s", bus.applied, out.String())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("the command wrote user.config.json itself")
	}
}

func TestAddonsOnSystemCheckoutStopAsRoot(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	onTheSystemCheckout(t)
	settingsEUID = func() int { return 0 }
	var out bytes.Buffer
	if err := addonsSet(addonsConfig(t), []string{"passwords.passbolt", "locked"}, &out); !errors.Is(err, errShown) {
		t.Fatalf("err %v", err)
	}
	if len(bus.requests) != 0 {
		t.Fatalf("requests %q", bus.requests)
	}
}

func TestUSBReviewTickOnSystemCheckoutAppliesWithoutWatching(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	onTheSystemCheckout(t)
	if err := applySystemSettingNow(usbReviewConfigKey, true); err != nil {
		t.Fatal(err)
	}
	if len(bus.requests) != 1 || bus.requests[0] != `{"usbReviewTechnicalView":true}` {
		t.Fatalf("requests %q", bus.requests)
	}
	if len(bus.applied) != 1 || bus.rebuilds != 0 {
		t.Fatalf("applied %q, rebuild polls %d", bus.applied, bus.rebuilds)
	}
}

func TestUSBReviewTickThatWaitsIsNotApplied(t *testing.T) {
	bus := &fakeSettingsBus{readyIn: settings.SecurityDelay}
	useFakeSettings(t, bus)
	err := applySystemSettingNow(usbReviewConfigKey, true)
	if err == nil || !strings.Contains(err.Error(), "gjallarctl settings apply c0ffee00") || len(bus.applied) != 0 {
		t.Fatalf("err %v, applied %q", err, bus.applied)
	}
}

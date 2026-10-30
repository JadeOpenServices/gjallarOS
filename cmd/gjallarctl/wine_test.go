package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// wineFakeHome makes a home with the SYNTHETIC app "synthetic".
func wineFakeHome(t *testing.T) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := winesandbox.PathsFor(home, "synthetic", "Synthetic App")
	for _, dir := range []string{filepath.Dir(paths.Record), paths.Prefix, paths.Files} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.Record, []byte(`{"name":"Synthetic App","exe":"app.exe"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := wineHome
	t.Cleanup(func() { wineHome = old })
	wineHome = func() (string, error) { return home, nil }
}

func TestWineList(t *testing.T) {
	bus := &fakeSettingsBus{wine: map[string]winesandbox.Flags{"synthetic": {Internet: true}, "gone": {Home: true}}}
	useFakeSettings(t, bus)
	wineFakeHome(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"wine", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	want := "synthetic  Synthetic App  internet on, home network off\n" +
		"gone  (removed)  internet off, home network on; gjallarctl wine set gone --internet off --home off clears it\n"
	if stdout.String() != want {
		t.Errorf("list:\n%s", stdout.String())
	}

	stdout.Reset()
	if code := run([]string{"wine", "list", "--ids"}, &stdout, &stderr); code != 0 || stdout.String() != "synthetic\n" {
		t.Errorf("ids %d %q", code, stdout.String())
	}
}

func TestWineListWithoutService(t *testing.T) {
	useFakeSettings(t, &fakeSettingsBus{missing: true})
	wineFakeHome(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"wine", "list"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(stdout.String(), "synthetic  Synthetic App  internet off, home network off") ||
		!strings.Contains(stderr.String(), "not on this system") {
		t.Errorf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestWineSet(t *testing.T) {
	bus := &fakeSettingsBus{wine: map[string]winesandbox.Flags{"synthetic": {Internet: true}, "gone": {Home: true}}}
	useFakeSettings(t, bus)
	wineFakeHome(t)
	var stdout, stderr bytes.Buffer
	// A switch not named keeps its state.
	if code := run([]string{"wine", "set", "synthetic", "--home", "on"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if stdout.String() != "synthetic: internet on, home network on\n" {
		t.Errorf("set: %q", stdout.String())
	}
	// A removed app's switches can go off, not on.
	if code := run([]string{"wine", "set", "gone", "--internet", "on"}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "gjallarctl wine list") {
		t.Errorf("removed app on: %d %q", code, stderr.String())
	}
	if code := run([]string{"wine", "set", "gone", "--home", "off"}, &stdout, &stderr); code != 0 {
		t.Errorf("removed app off: %d %q", code, stderr.String())
	}
	if got := strings.Join(bus.wineSets, "; "); got != "synthetic {Internet:true Home:true}; gone {Internet:false Home:false}" {
		t.Errorf("sets %s", got)
	}
	if _, ok := bus.wine["gone"]; ok {
		t.Error("removed app's switches kept")
	}
}

func TestWineSetRefuses(t *testing.T) {
	bus := &fakeSettingsBus{}
	useFakeSettings(t, bus)
	wineFakeHome(t)
	for _, args := range [][]string{
		{"set", "synthetic"},
		{"set", "synthetic", "--internet"},
		{"set", "synthetic", "--internet", "yes"},
		{"set", "synthetic", "--internet", "on", "--internet", "off"},
		{"set", "synthetic", "--wifi", "on"},
		{"set", "../synthetic", "--internet", "on"},
		{"list", "--json"},
		{"remove", "synthetic"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(append([]string{"wine"}, args...), &stdout, &stderr); code != 2 {
			t.Errorf("%q: code %d", args, code)
		}
	}
	if len(bus.wineSets) != 0 {
		t.Errorf("sets %v", bus.wineSets)
	}

	settingsEUID = func() int { return 0 }
	var stdout, stderr bytes.Buffer
	if code := run([]string{"wine", "set", "synthetic", "--internet", "off"}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "your own user") {
		t.Errorf("root: %d %q", code, stderr.String())
	}
}

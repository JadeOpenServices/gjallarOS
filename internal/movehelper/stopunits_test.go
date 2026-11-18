package movehelper

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

// No test runs the real systemctl or systemd-run: the package default is a
// systemctl that finds no unit loaded and a systemd-run that fails. A plain
// user cannot mount, so lock leaves base as it is, except in the namespace
// child of TestIsolateBase, where the test is root of its own namespaces.
func TestMain(m *testing.M) {
	if os.Getenv(usernsChild) == "" {
		prepareBase = func(string) error { return nil }
	}
	dir, err := os.MkdirTemp("", "movehelper-units-")
	if err != nil {
		panic(err)
	}
	for name, code := range map[string]string{"systemctl": "5", "systemd-run": "1"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit "+code+"\n"), 0o755); err != nil {
			panic(err)
		}
	}
	storecopy.Systemctl = filepath.Join(dir, "systemctl")
	storecopy.SystemdRun = filepath.Join(dir, "systemd-run")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeSystemctl swaps storecopy.Systemctl for a script that logs its
// arguments and exits with code; calls returns the logged lines. No test
// runs the real one.
func fakeSystemctl(t *testing.T, code string) (calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	logf := filepath.Join(dir, "log")
	fake := filepath.Join(dir, "systemctl")
	script := "#!/bin/sh\necho \"$*\" >> " + logf + "\nexit " + code + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := storecopy.Systemctl
	t.Cleanup(func() { storecopy.Systemctl = saved })
	storecopy.Systemctl = fake
	return func() []string {
		got, _ := os.ReadFile(logf)
		return strings.Fields(strings.ReplaceAll(string(got), "stop -- ", ""))
	}
}

// L135: a unit left from a killed helper is stopped before the cache goes,
// and one that does not stop keeps the cache as it is.
func TestRemoveCacheStopsFirst(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		stops      int
		gone       bool
	}{{"stopped", "0", len(storecopy.Units), true}, {"still running", "1", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeSystemctl(t, tc.code)
			top := t.TempDir()
			put := filepath.Join(top, storecopy.CacheName, "nar", "x.nar")
			if err := os.MkdirAll(filepath.Dir(put), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(put, []byte("SYNTHETIC"), 0o644); err != nil {
				t.Fatal(err)
			}
			d, err := safefs.OpenDir(top)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			err = removeCache(context.Background(), d)
			if got := calls(); len(got) != tc.stops {
				t.Errorf("stopped %q", got)
			}
			if (err == nil) != tc.gone {
				t.Errorf("got %v", err)
			}
			if _, statErr := os.Stat(put); (statErr == nil) == tc.gone {
				t.Errorf("cache gone=%v, want %v (%v)", statErr != nil, tc.gone, statErr)
			}
		})
	}
}

// close stops the units before it unmounts anything; a unit that does not
// stop fails close as a hard error.
func TestCloseStopsUnits(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	calls := fakeSystemctl(t, "0")
	if out, err := run(t, base, `{"op":"close"}`); err != nil || strings.TrimSpace(out) != "{}" || len(calls()) != len(storecopy.Units) {
		t.Fatalf("got %q, %v, stops %q", out, err, calls())
	}
	fakeSystemctl(t, "1")
	if _, err := run(t, base, `{"op":"close"}`); err == nil || !strings.Contains(err.Error(), "stopping ") {
		t.Fatalf("close with a running unit: %v", err)
	}
}

// Every confined unit of the move is in the list stopUnits works through.
func TestUnitsListed(t *testing.T) {
	for _, u := range []string{storecopy.ExportUnit, storecopy.ClosureUnit} {
		found := false
		for _, l := range storecopy.Units {
			found = found || l == u+".service"
		}
		if !found {
			t.Errorf("%s not stopped before cleanup", u)
		}
	}
}

// The shipped stopUnits against a stand-in systemctl: every unit gets its
// own "stop --" call, "not loaded" is fine, any other failure is not.
func TestStopUnitsExitCodes(t *testing.T) {
	for code, ok := range map[string]bool{"0": true, "5": true, "1": false, "4": false} {
		calls := fakeSystemctl(t, code)
		err := stopUnits(context.Background())
		if (err == nil) != ok {
			t.Errorf("exit %s: %v", code, err)
		}
		want := storecopy.Units
		if !ok {
			want = want[:1]
		}
		if got := calls(); !slices.Equal(got, want) {
			t.Errorf("exit %s: stopped %q, want %q", code, got, want)
		}
	}
}

package movehelper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

// stopCalls counts stopUnits calls; stopErr is what the next call returns.
var (
	stopCalls int
	stopErr   error
)

// No test runs the real systemctl.
func TestMain(m *testing.M) {
	stopUnits = func(context.Context) error {
		stopCalls++
		return stopErr
	}
	os.Exit(m.Run())
}

func swapStop(t *testing.T, err error) {
	t.Helper()
	stopCalls, stopErr = 0, err
	t.Cleanup(func() { stopCalls, stopErr = 0, nil })
}

var errStop = errors.New("SYNTHETIC unit did not stop")

// L135: a unit left from a killed helper is stopped before the cache goes,
// and one that does not stop keeps the cache as it is.
func TestRemoveCacheStopsFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop error
		gone bool
	}{{"stopped", nil, true}, {"still running", errStop, false}} {
		t.Run(tc.name, func(t *testing.T) {
			swapStop(t, tc.stop)
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
			if stopCalls != 1 {
				t.Errorf("stop called %d times", stopCalls)
			}
			if !errors.Is(err, tc.stop) || (tc.stop == nil) != (err == nil) {
				t.Errorf("got %v, want %v", err, tc.stop)
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
	swapStop(t, nil)
	if out, err := run(t, base, `{"op":"close"}`); err != nil || strings.TrimSpace(out) != "{}" || stopCalls != 1 {
		t.Fatalf("got %q, %v, %d stops", out, err, stopCalls)
	}
	swapStop(t, errStop)
	if _, err := run(t, base, `{"op":"close"}`); !errors.Is(err, errStop) {
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

// realStop is stopUnits as the helper ships it, before TestMain swaps it.
var realStop = stopUnits

// The shipped stopUnits against a stand-in systemctl: every unit gets its
// own "stop --" call, "not loaded" is fine, any other failure is not.
func TestStopUnitsExitCodes(t *testing.T) {
	dir := t.TempDir()
	logf := filepath.Join(dir, "log")
	fake := filepath.Join(dir, "systemctl")
	saved := storecopy.Systemctl
	t.Cleanup(func() { storecopy.Systemctl = saved })
	storecopy.Systemctl = fake
	for code, ok := range map[string]bool{"0": true, "5": true, "1": false, "4": false} {
		os.Remove(logf)
		script := "#!/bin/sh\necho \"$*\" >> " + logf + "\nexit " + code + "\n"
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		err := realStop(context.Background())
		if (err == nil) != ok {
			t.Errorf("exit %s: %v", code, err)
		}
		got, _ := os.ReadFile(logf)
		want := ""
		for _, u := range storecopy.Units {
			want += "stop -- " + u + "\n"
			if !ok {
				break
			}
		}
		if string(got) != want {
			t.Errorf("exit %s: calls %q, want %q", code, got, want)
		}
	}
}

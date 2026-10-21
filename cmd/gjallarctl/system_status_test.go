package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/localchanges"
)

// SYNTHETIC folder for the status tests.
var statusFolder = map[string]string{"system/test.nix": "{ }\n"}

// useRootCopy writes tree as root's copy. The test user owns it, so it is
// read with ReadDir, which takes the same files as ReadRoot.
func useRootCopy(t *testing.T, tree localchanges.Tree) {
	t.Helper()
	localChangesPath = filepath.Join(t.TempDir(), "local")
	if err := localchanges.Write(localChangesPath, tree); err != nil {
		t.Fatal(err)
	}
	old := readRootCopy
	readRootCopy = localchanges.ReadDir
	t.Cleanup(func() { readRootCopy = old })
}

// useClock makes chronyd report state at the SYNTHETIC time now.
func useClock(t *testing.T, state clockState, now time.Time) {
	t.Helper()
	oldRead, oldNow, oldBoot, oldRouter := readClock, clockNow, readTimeBoot, readTimeRouter
	readClock = func() clockState { return state }
	clockNow = func() time.Time { return now }
	readTimeBoot = func() timeBoot { return timeBoot{} }
	readTimeRouter = func() *timeRouter { return nil }
	t.Cleanup(func() { readClock, clockNow, readTimeBoot, readTimeRouter = oldRead, oldNow, oldBoot, oldRouter })
}

func useBuildSource(t *testing.T, data string) {
	t.Helper()
	useClock(t, clockState{}, time.Unix(0, 0))
	old := buildSourcePath
	buildSourcePath = filepath.Join(t.TempDir(), "build-source.json")
	t.Cleanup(func() { buildSourcePath = old })
	if err := os.WriteFile(buildSourcePath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSystemStatusShowsWhatWasBuilt(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, true, statusFolder)
	files := map[string][]byte{}
	for rel, data := range statusFolder {
		files[rel] = []byte(data)
	}
	tree, err := localchanges.Check(files)
	if err != nil {
		t.Fatal(err)
	}
	useRootCopy(t, tree)
	useBuildSource(t, `{"revision":"b1a07ba98fe16fe03d6e08e57f7276aff13cb272","oddc":"eab37c4f3de331d3d7b1d5f273c60e59fe6bd0c2",`+
		`"local":{"hash":"`+tree.Hash()+`","files":["system/test.nix"]}}`)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Running system: b1a07ba98fe1 (oddc eab37c4f3de3)",
		"Developer mode: on",
		"Built with local files: " + tree.Hash()[:12],
		"  system/test.nix",
		"): built",
		"): same as the system copy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestSystemStatusShowsPendingAndDifferentFolder(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, true, statusFolder)
	tree, err := localchanges.Check(map[string][]byte{"system/other.nix": []byte("{ }\n")})
	if err != nil {
		t.Fatal(err)
	}
	useRootCopy(t, tree)
	useBuildSource(t, `{"revision":"b1a07ba98fe16fe03d6e08e57f7276aff13cb272"}`)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Built with local files: none",
		"not built yet; the next rebuild builds it",
		"differs from the system copy; gjallarctl system apply shows how",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestSystemApplyCreatesMissingFolder(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, true, nil)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if len(bus.folders) != 0 {
		t.Fatalf("a new template was sent to root: %v", bus.folders)
	}
	dir := filepath.Join(os.Getenv("HOME"), "GjallarOS-local")
	tree, err := localchanges.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Empty() || !strings.Contains(stdout.String(), "Created "+dir+" from the template") {
		t.Fatalf("folder %v, stdout %s", tree.Paths(), stdout.String())
	}
}

func TestDeveloperModeOnCreatesFolder(t *testing.T) {
	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, false, nil)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "set", "developerMode=true"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	dir := filepath.Join(os.Getenv("HOME"), "GjallarOS-local")
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("template not created: %v\n%s", err, stdout.String())
	}

	// A folder that already exists is left as it is.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("SYNTHETIC mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := run([]string{"settings", "set", "developerMode=true"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(data) != "SYNTHETIC mine\n" || strings.Contains(stdout.String(), "Created") {
		t.Fatalf("existing folder touched: %q\n%s", data, stdout.String())
	}
}

func TestSystemStatusTimeLine(t *testing.T) {
	// SYNTHETIC times.
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
	recent := now.Add(-20 * time.Minute)
	old := now.Add(-3 * time.Hour)
	for _, c := range []struct {
		state clockState
		want  string
	}{
		{clockState{}, "Time: not confirmed, chronyd is not running"},
		{clockState{Running: true, Started: "Wed 2026-10-21 11:00:00 CEST"}, "Time: not confirmed since chronyd started at Wed 2026-10-21 11:00:00 CEST"},
		{clockState{Running: true}, "Time: not confirmed since chronyd started"},
		{clockState{Running: true, Ref: recent}, "Time: not confirmed since " + recent.Format("2006-01-02 15:04 MST")},
		{clockState{Running: true, Synced: true, Ref: old}, "Time: not confirmed since " + old.Format("2006-01-02 15:04 MST")},
		{clockState{Running: true, Synced: true, Ref: recent}, "Time: confirmed by NTS at " + recent.Format("2006-01-02 15:04 MST")},
	} {
		if got := timeLine(c.state, now); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.state, got, c.want)
		}
	}

	bus := &fakeSettingsBus{}
	useLocalFolder(t, bus, true, statusFolder)
	useRootCopy(t, localchanges.Tree{})
	useBuildSource(t, `{"revision":"b1a07ba98fe16fe03d6e08e57f7276aff13cb272"}`)
	useClock(t, clockState{Running: true, Synced: true, Ref: recent}, now)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "\nTime: confirmed by NTS at ") {
		t.Errorf("stdout lacks the time line:\n%s", stdout.String())
	}
}

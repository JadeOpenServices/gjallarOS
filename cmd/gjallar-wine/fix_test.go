package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winefix"
	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// SYNTHETIC fix file.
var fixBody = []byte("SYNTHETIC runtime installer\n")

type toolRun struct {
	tool *winesandbox.Tool
	args []string
	seed []byte
}

// fixSetup adds the SYNTHETIC app of doctorSetup and a SYNTHETIC fix table
// whose one download comes from a local test server.
func fixSetup(t *testing.T) (*fakeSetup, winesandbox.Paths, *[]toolRun, *int) {
	t.Helper()
	f, paths := doctorSetup(t)
	t.Setenv("XDG_CACHE_HOME", "")
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(fixBody)
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(fixBody)
	file := winefix.File{Verb: "vcrun2010", URL: srv.URL + "/vcredist_x64.exe", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(fixBody))}
	tab := &winefix.Table{
		Fixes: []winefix.Fix{
			{ID: "vcrun2010", Title: "SYNTHETIC runtime", Kind: winefix.KindSafe, Why: "SYNTHETIC",
				Actions: []winefix.Action{{Do: winefix.DoVerb, Verb: "vcrun2010"}}, Files: []winefix.File{file}},
			{ID: "d3dx9", Title: "SYNTHETIC offered", Kind: winefix.KindOffer, Why: "SYNTHETIC",
				Actions:   []winefix.Action{{Do: winefix.DoDLLOverride, DLL: "d3dx9_43", Mode: "native"}},
				Conflicts: []string{"other"}},
			{ID: "other", Title: "SYNTHETIC other", Kind: winefix.KindSafe, Why: "SYNTHETIC",
				Actions: []winefix.Action{{Do: winefix.DoDLLOverride, DLL: "x", Mode: "builtin"}}},
		},
		Refused: []winefix.Refusal{{ID: "vcrun2019", Reason: "SYNTHETIC reason"}},
	}
	runs := &[]toolRun{}
	oldLoad, oldTool, oldClient := loadFixes, launchTool, fixClient
	t.Cleanup(func() { loadFixes, launchTool, fixClient = oldLoad, oldTool, oldClient })
	loadFixes = func() (*winefix.Table, error) { return tab, nil }
	fixClient = srv.Client()
	launchTool = func(id string, tool *winesandbox.Tool, args []string, stderr io.Writer) (int, error) {
		run := toolRun{tool: tool, args: args}
		if tool != nil {
			run.seed, _ = os.ReadFile(filepath.Join(paths.Prefix, seedDir, "vcrun2010", "vcredist_x64.exe"))
		}
		*runs = append(*runs, run)
		return 0, nil
	}
	return f, paths, runs, &hits
}

func readApp(t *testing.T, f *fakeSetup) winesandbox.App {
	t.Helper()
	a, _, err := winesandbox.LoadApp(f.home, "synthetic-app")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFix(t *testing.T) {
	f, paths, runs, hits := fixSetup(t)
	var out strings.Builder
	if err := fixCmd([]string{"synthetic-app", "VCRUN2010"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(*runs) != 1 {
		t.Fatalf("runs %v", *runs)
	}
	run := (*runs)[0]
	if run.tool == nil || run.tool.Path != winetricksPath || !slices.Equal(run.args, []string{"-q", "vcrun2010"}) {
		t.Errorf("ran %+v", run)
	}
	if !slices.Contains(run.tool.Env, "W_CACHE="+filepath.Join(paths.Prefix, seedDir)) {
		t.Errorf("env %q", run.tool.Env)
	}
	if string(run.seed) != string(fixBody) {
		t.Errorf("seeded %q", run.seed)
	}
	if exists(filepath.Join(paths.Prefix, seedDir)) {
		t.Error("fix files left in the prefix")
	}
	if a := readApp(t, f); !slices.Equal(a.Fixes, []string{"vcrun2010"}) {
		t.Errorf("record fixes %q", a.Fixes)
	}
	if len(snapshots(t, f)) != 1 {
		t.Error("no snapshot before the fix")
	}
	if !strings.Contains(out.String(), "gjallarctl wine undo synthetic-app") {
		t.Errorf("no undo hint: %s", out.String())
	}

	// Again: nothing to do, nothing downloaded.
	out.Reset()
	if err := fixCmd([]string{"synthetic-app", "vcrun2010"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "already has") {
		t.Errorf("again: %v %s", err, out.String())
	}
	if len(*runs) != 1 || *hits != 1 {
		t.Errorf("again ran %d, downloaded %d", len(*runs), *hits)
	}

	// An offered fix warns and sets the override offline in Wine.
	out.Reset()
	if err := fixCmd([]string{"synthetic-app", "d3dx9"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	run = (*runs)[1]
	if run.tool != nil || !slices.Equal(run.args, []string{"reg", "add", dllOverrides, "/v", "d3dx9_43", "/d", "native", "/f"}) {
		t.Errorf("override ran %+v", run)
	}
	if !strings.Contains(out.String(), "replaces some of Wine's own") {
		t.Errorf("no warning: %s", out.String())
	}

	// A conflict is refused either way round.
	if err := fixCmd([]string{"synthetic-app", "other"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "does not go together") {
		t.Errorf("conflict: %v", err)
	}

	// Undo takes the last fix out of the record again.
	if err := undoCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if a := readApp(t, f); !slices.Equal(a.Fixes, []string{"vcrun2010"}) {
		t.Errorf("after undo %q", a.Fixes)
	}
}

func TestFixRefuses(t *testing.T) {
	f, _, runs, _ := fixSetup(t)
	for args, want := range map[[2]string]string{
		{"synthetic-app", "vcrun2019"}: "not offered: SYNTHETIC reason",
		{"synthetic-app", "nosuch"}:    "gjallarctl wine fixes lists them",
		{"no-app", "vcrun2010"}:        "",
	} {
		err := fixCmd(args[:], io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", args, err)
		}
	}
	if err := fixCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != errUsage {
		t.Errorf("one argument: %v", err)
	}
	if len(*runs) != 0 || len(snapshots(t, f)) != 0 {
		t.Error("a refused fix changed the app")
	}
}

func TestFixBadDownload(t *testing.T) {
	f, _, runs, _ := fixSetup(t)
	fixBody = []byte("SYNTHETIC changed by its publisher\n")
	t.Cleanup(func() { fixBody = []byte("SYNTHETIC runtime installer\n") })
	err := fixCmd([]string{"synthetic-app", "vcrun2010"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("changed download used")
	}
	if len(*runs) != 0 || len(snapshots(t, f)) != 0 || len(readApp(t, f).Fixes) != 0 {
		t.Error("a failed download changed the app")
	}
}

func TestRepairPutsFixesBack(t *testing.T) {
	f, _, runs, _ := fixSetup(t)
	if err := fixCmd([]string{"synthetic-app", "vcrun2010"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := repairCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(*runs) != 2 || !slices.Equal((*runs)[1].args, []string{"-q", "--force", "vcrun2010"}) {
		t.Errorf("repair ran %+v", *runs)
	}
	if !slices.Equal(readApp(t, f).Fixes, []string{"vcrun2010"}) {
		t.Error("repair lost the fixes")
	}
}

func TestResetDropsFixes(t *testing.T) {
	f, _, _, _ := fixSetup(t)
	if err := fixCmd([]string{"synthetic-app", "vcrun2010"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := resetCmd([]string{"synthetic-app"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(readApp(t, f).Fixes) != 0 || !strings.Contains(out.String(), "gjallarctl wine fix synthetic-app FIX") {
		t.Errorf("reset kept fixes: %s", out.String())
	}
	if err := undoCmd([]string{"synthetic-app"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(readApp(t, f).Fixes, []string{"vcrun2010"}) {
		t.Error("undo did not bring the fixes back")
	}
}

func TestSeedFilesStaysInPrefix(t *testing.T) {
	prefix := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "drive_c"), 0o700); err != nil {
		t.Fatal(err)
	}
	// SYNTHETIC: a link in the prefix that leads out.
	if err := os.Symlink(outside, filepath.Join(prefix, seedDir)); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "src")
	writeFile(t, src, fixBody)
	file := winefix.File{Verb: "v", URL: "https://example.invalid/a.exe"}
	fixes := []*winefix.Fix{{ID: "v", Files: []winefix.File{file}}}
	done, err := seedFiles(prefix, fixes, map[winefix.File]string{file: src})
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(outside); len(left) != 0 {
		t.Errorf("wrote outside the prefix: %v", left)
	}
	done()
	if exists(filepath.Join(prefix, seedDir)) {
		t.Error("seed left behind")
	}
}

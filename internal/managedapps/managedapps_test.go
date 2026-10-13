package managedapps

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// SYNTHETIC profile: extensions.json holds only the fields Remove reads, in
// the shape Firefox writes them.
const extensions = `{"schemaVersion": 36, "addons": [
  {"id": "policy@example.org", "location": "app-profile", "installTelemetryInfo": {"source": "enterprise-policy"}},
  {"id": "mine@example.org", "location": "app-profile", "installTelemetryInfo": {"source": "amo"}},
  {"id": "linked@example.org", "location": "app-profile", "installTelemetryInfo": {"source": "enterprise-policy"}},
  {"id": "system@example.org", "location": "app-system-defaults", "installTelemetryInfo": {"source": "enterprise-policy"}}
]}`

func profile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ext := filepath.Join(dir, "extensions")
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"policy@example.org", "mine@example.org"} {
		if err := os.WriteFile(filepath.Join(ext, id+".xpi"), []byte("xpi"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/nonexistent", filepath.Join(ext, "linked@example.org.xpi")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(extensions), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

func TestReconcileFirefox(t *testing.T) {
	dir := profile(t)
	ff, err := NewFirefox(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "managed")
	all := []string{"policy@example.org", "mine@example.org", "linked@example.org", "system@example.org"}

	// The first run only records.
	if res, err := Reconcile(state, ff, all); err != nil || len(res.Removed) != 0 {
		t.Fatalf("first run %+v %v", res, err)
	}
	res, err := Reconcile(state, ff, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Removed, []string{"policy@example.org"}) {
		t.Fatalf("removed %v", res.Removed)
	}
	if !slices.Equal(res.Left, []string{"mine@example.org", "system@example.org"}) {
		t.Fatalf("left %v", res.Left)
	}
	if _, ok := res.Failed["linked@example.org"]; !ok || len(res.Failed) != 1 {
		t.Fatalf("failed %v", res.Failed)
	}
	ext := filepath.Join(dir, "extensions")
	if exists(t, filepath.Join(ext, "policy@example.org.xpi")) || !exists(t, filepath.Join(ext, "mine@example.org.xpi")) || !exists(t, filepath.Join(ext, "linked@example.org.xpi")) {
		t.Fatal("wrong files removed")
	}
	if got, _ := readLedger(filepath.Join(state, ff.Ledger()+".json")); !slices.Equal(got, []string{"linked@example.org"}) {
		t.Fatalf("ledger keeps %v", got)
	}
}

func TestReconcileWaitsWhileFirefoxRuns(t *testing.T) {
	dir := profile(t)
	ff, err := NewFirefox(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	if _, err := Reconcile(state, ff, []string{"policy@example.org"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("127.0.0.1:+"+strconv.Itoa(os.Getpid()), filepath.Join(dir, "lock")); err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(state, ff, nil)
	if err != nil || !res.Busy || len(res.Removed) != 0 {
		t.Fatalf("busy run %+v %v", res, err)
	}
	if got, _ := readLedger(filepath.Join(state, ff.Ledger()+".json")); !slices.Equal(got, []string{"policy@example.org"}) {
		t.Fatalf("ledger dropped a waiting item: %v", got)
	}

	// A lock left by a crash does not block.
	os.Remove(filepath.Join(dir, "lock"))
	if err := os.Symlink("127.0.0.1:+999999999", filepath.Join(dir, "lock")); err != nil {
		t.Fatal(err)
	}
	if res, err := Reconcile(state, ff, nil); err != nil || res.Busy || !slices.Equal(res.Removed, []string{"policy@example.org"}) {
		t.Fatalf("stale lock run %+v %v", res, err)
	}
}

func TestFirefoxRejects(t *testing.T) {
	dir := profile(t)
	if _, err := NewFirefox("relative"); err == nil {
		t.Fatal("relative profile accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFirefox(link); err == nil {
		t.Fatal("linked profile accepted")
	}
	ff, _ := NewFirefox(dir)
	if _, err := ff.Remove("../policy@example.org"); err == nil {
		t.Fatal("path in ID accepted")
	}
	if _, err := Reconcile(t.TempDir(), badName{}, nil); err == nil {
		t.Fatal("bad ledger name accepted")
	}
}

type badName struct{ *Firefox }

func (badName) Ledger() string { return "../x" }

func TestLedgerName(t *testing.T) {
	if got := (&Firefox{Profile: "/home/u/.mozilla/firefox/My Profile"}).Ledger(); got != "firefox-my-profile" {
		t.Fatalf("ledger %q", got)
	}
}

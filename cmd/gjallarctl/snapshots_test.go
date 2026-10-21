package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSnapshotsBus answers like gjallar-snapshotd. Its snapshot names and
// file contents are SYNTHETIC.
type fakeSnapshotsBus struct {
	names   []string
	content string
	mode    os.FileMode
	err     error
	asked   []string
}

func (b *fakeSnapshotsBus) Close() error { return nil }

func (b *fakeSnapshotsBus) List(context.Context) ([]string, error) {
	return b.names, b.err
}

func (b *fakeSnapshotsBus) Restore(_ context.Context, path, from string) (*os.File, string, error) {
	b.asked = append(b.asked, path+"|"+from)
	if b.err != nil {
		return nil, "", b.err
	}
	old := filepath.Join(os.Getenv("SNAPSHOT_TEST_DIR"), "old")
	if err := os.WriteFile(old, []byte(b.content), b.mode); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(old, b.mode); err != nil {
		return nil, "", err
	}
	file, err := os.Open(old)
	if err != nil {
		return nil, "", err
	}
	name := b.names[0]
	if from != "" {
		name = from
	}
	return file, name, nil
}

func useFakeSnapshots(t *testing.T, bus *fakeSnapshotsBus) string {
	t.Helper()
	t.Setenv("SNAPSHOT_TEST_DIR", t.TempDir())
	oldConnect, oldEUID := snapshotsConnect, settingsEUID
	t.Cleanup(func() { snapshotsConnect, settingsEUID = oldConnect, oldEUID })
	snapshotsConnect = func() (snapshotsBus, error) { return bus, nil }
	settingsEUID = func() int { return 1000 }
	if bus.names == nil {
		bus.names = []string{"home.20261021T030000+0200", "home.20261021T020000+0200"}
	}
	if bus.mode == 0 {
		bus.mode = 0o640
	}
	home := t.TempDir()
	dir, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSnapshotsListNames(t *testing.T) {
	bus := &fakeSnapshotsBus{}
	useFakeSnapshots(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"snapshots", "list", "--names"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if stdout.String() != "home.20261021T030000+0200\nhome.20261021T020000+0200\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestSnapshotsRestoreWritesNewFile(t *testing.T) {
	bus := &fakeSnapshotsBus{content: "SYNTHETIC old notes\n"}
	home := useFakeSnapshots(t, bus)
	original := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(original, []byte("SYNTHETIC new notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"snapshots", "restore", original}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if len(bus.asked) != 1 || bus.asked[0] != original+"|" {
		t.Fatalf("asked %v", bus.asked)
	}
	copied := filepath.Join(home, "notes.restored-20261021T030000.txt")
	data, err := os.ReadFile(copied)
	if err != nil || string(data) != bus.content {
		t.Fatalf("copy %q, %v", data, err)
	}
	if info, _ := os.Stat(copied); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode())
	}
	if data, _ := os.ReadFile(original); string(data) != "SYNTHETIC new notes\n" {
		t.Fatalf("original changed: %q", data)
	}
}

func TestSnapshotsRestoreNeverOverwrites(t *testing.T) {
	bus := &fakeSnapshotsBus{content: "SYNTHETIC old notes\n"}
	home := useFakeSnapshots(t, bus)
	existing := filepath.Join(home, "notes.restored-20261021T020000.txt")
	if err := os.WriteFile(existing, []byte("SYNTHETIC kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"snapshots", "restore", filepath.Join(home, "notes.txt"), "--from", "home.20261021T020000+0200"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "already there") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if data, _ := os.ReadFile(existing); string(data) != "SYNTHETIC kept\n" {
		t.Fatalf("existing file changed: %q", data)
	}
}

func TestSnapshotsRestoreToFolder(t *testing.T) {
	bus := &fakeSnapshotsBus{content: "SYNTHETIC report\n"}
	home := useFakeSnapshots(t, bus)
	to := filepath.Join(home, "back")
	if err := os.Mkdir(to, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	gone := filepath.Join(home, "gone", "report.md")
	var code int
	if code = run([]string{"snapshots", "restore", gone}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "--to FOLDER") {
		t.Fatalf("missing folder: code %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code = run([]string{"snapshots", "restore", gone, "--to", to}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(to, "report.restored-20261021T030000.md")); err != nil {
		t.Fatal(err)
	}
	if len(bus.asked) != 1 || bus.asked[0] != gone+"|" {
		t.Fatalf("asked %v", bus.asked)
	}
}

func TestSnapshotsRefusesRoot(t *testing.T) {
	bus := &fakeSnapshotsBus{}
	useFakeSnapshots(t, bus)
	settingsEUID = func() int { return 0 }
	var stdout, stderr bytes.Buffer
	if code := run([]string{"snapshots", "list"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "your own user") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}

func TestSnapshotsMissingService(t *testing.T) {
	bus := &fakeSnapshotsBus{err: errSnapshotsMissing}
	useFakeSnapshots(t, bus)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"snapshots", "list"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "no snapshots on this system") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}

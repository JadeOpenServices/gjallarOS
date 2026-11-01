package winesandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitChanged(t *testing.T, changed <-chan struct{}, errs <-chan error, what string) {
	t.Helper()
	select {
	case <-changed:
	case err := <-errs:
		t.Fatalf("%s: %v", what, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no report", what)
	}
}

func TestWatchTrustFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vpn-trust.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed, errs, err := Watch(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Drain anything the device's network sent meanwhile.
	for len(changed) > 0 {
		<-changed
	}

	tmp := filepath.Join(dir, "vpn-trust.json.new")
	if err := os.WriteFile(tmp, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	waitChanged(t, changed, errs, "replaced")
	for len(changed) > 0 {
		<-changed
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitChanged(t, changed, errs, "removed")
}

func TestWatchFolderGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gjallar")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, errs, err := Watch(ctx, filepath.Join(dir, "vpn-trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errs:
	case <-time.After(5 * time.Second):
		t.Fatal("no error after the folder went away")
	}
}

func TestWatchMissingFolder(t *testing.T) {
	if _, _, err := Watch(context.Background(), filepath.Join(t.TempDir(), "none", "vpn-trust.json")); err == nil {
		t.Fatal("no error for a missing folder")
	}
}

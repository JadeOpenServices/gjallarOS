package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSystemHelpers(t *testing.T) (string, *[]string) {
	t.Helper()
	dir := t.TempDir()
	var started []string
	oldDir, oldExec := systemLibexec, helperExec
	t.Cleanup(func() { systemLibexec, helperExec = oldDir, oldExec })
	systemLibexec = dir
	helperExec = func(argv0 string, argv []string, _ []string) error {
		started = append([]string{argv0}, argv...)
		return os.ErrPermission
	}
	return dir, &started
}

func TestNetworkForward(t *testing.T) {
	dir, started := fakeSystemHelpers(t)
	helper := filepath.Join(dir, "gjallar-network-diagnostics")
	if err := os.WriteFile(helper, []byte("SYNTHETIC"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	run([]string{"network", "dns", "example.invalid"}, &stdout, &stderr)
	want := []string{helper, "gjallar-network-diagnostics", "dns", "example.invalid"}
	if strings.Join(*started, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("started %q", *started)
	}
}

func TestNetworkForwardWithoutHelper(t *testing.T) {
	_, started := fakeSystemHelpers(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"network"}, &stdout, &stderr); code != 1 || *started != nil {
		t.Errorf("code %d, started %q", code, *started)
	}
	if !strings.Contains(stderr.String(), "not part of this system") {
		t.Errorf("stderr %q", stderr.String())
	}
}

func TestRegisterKeyForward(t *testing.T) {
	dir, started := fakeSystemHelpers(t)
	helper := filepath.Join(dir, "gjallar-register-key")
	if err := os.WriteFile(helper, []byte("SYNTHETIC"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	run([]string{"register-key", "https://example.invalid/register"}, &stdout, &stderr)
	want := []string{helper, "gjallar-register-key", "https://example.invalid/register"}
	if strings.Join(*started, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("started %q", *started)
	}
}

func TestManagementStatus(t *testing.T) {
	dir, started := fakeSystemHelpers(t)
	helper := filepath.Join(dir, "gjallar-management-status")
	if err := os.WriteFile(helper, []byte("SYNTHETIC"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"management"}, &stdout, &stderr); code != 2 || *started != nil {
		t.Fatalf("bare management: code %d, started %q", code, *started)
	}
	run([]string{"management", "status"}, &stdout, &stderr)
	want := []string{helper, "gjallar-management-status"}
	if strings.Join(*started, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("started %q", *started)
	}
}

// Standalone installs have no management helper; the command says so
// instead of failing to start it.
func TestManagementStatusStandalone(t *testing.T) {
	_, started := fakeSystemHelpers(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"management", "status"}, &stdout, &stderr); code != 1 || *started != nil {
		t.Errorf("code %d, started %q", code, *started)
	}
	if !strings.Contains(stderr.String(), "device management is not part of this system") {
		t.Errorf("stderr %q", stderr.String())
	}
}

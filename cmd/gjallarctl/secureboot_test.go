package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSecureBootUpdateDBXReportsMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runSecureBoot([]string{"update-dbx"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "gjallar-secure-boot-dbx-update not found") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSecureBootUpdateDBXRejectsRepo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSecureBoot([]string{"update-dbx", "--repo", "/tmp"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), secureBootUsage) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSecureBootUsageListsUpdateDBX(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSecureBoot([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "update-dbx") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// Firmware without Secure Boot gets one plain sentence from setup, before
// the repository or anything root-only is read, and status says the same
// (Plane 328).
func TestSecureBootWithoutFirmwareSupport(t *testing.T) {
	old := secureBootOffered
	t.Cleanup(func() { secureBootOffered = old })
	secureBootOffered = func() (bool, error) { return false, nil }

	var stdout, stderr bytes.Buffer
	if code := runSecureBoot([]string{"setup", "--repo", t.TempDir() + "/missing"}, &stdout, &stderr); code != 1 {
		t.Fatalf("setup code = %d, want 1; stderr %q", code, stderr.String())
	}
	if stderr.String() != noFirmwareSecureBoot+" Nothing was changed.\n" || stdout.Len() != 0 {
		t.Fatalf("setup stdout %q stderr %q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runSecureBoot([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status code = %d, want 0; stderr %q", code, stderr.String())
	}
	if stdout.String() != "Firmware:  no Secure Boot\n" || stderr.Len() != 0 {
		t.Fatalf("status stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestSecureBootSetupFirmwareReadError(t *testing.T) {
	old := secureBootOffered
	t.Cleanup(func() { secureBootOffered = old })
	secureBootOffered = func() (bool, error) { return false, errors.New("EXAMPLE read error") }

	var stdout, stderr bytes.Buffer
	if code := runSecureBoot([]string{"setup"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "read the firmware's Secure Boot variables: EXAMPLE read error") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

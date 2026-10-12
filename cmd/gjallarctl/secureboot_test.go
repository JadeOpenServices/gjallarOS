package main

import (
	"bytes"
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

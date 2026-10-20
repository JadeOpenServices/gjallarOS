package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseConfigAllowsUnenrolledWithoutTPMHandle(
	t *testing.T,
) {
	root := t.TempDir()

	cfg, err := parseConfig(
		[]string{
			"--socket", filepath.Join(root, "control.sock"),
			"--owner-uid", "1000",
			"--oddc-resolved", filepath.Join(root, "resolved.json"),
			"--oddc-model", "model/synthetic",
			"--state-dir", filepath.Join(root, "state"),
		},
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ownerUID != 1000 {
		t.Fatalf(
			"owner UID = %d, want 1000",
			cfg.ownerUID,
		)
	}

	if cfg.tpmHandle != "" {
		t.Fatalf(
			"unexpected TPM handle %q",
			cfg.tpmHandle,
		)
	}
}

func TestParseConfigRequiresOwnerUID(t *testing.T) {
	root := t.TempDir()

	_, err := parseConfig(
		[]string{
			"--socket", filepath.Join(root, "control.sock"),
			"--oddc-resolved", filepath.Join(root, "resolved.json"),
			"--oddc-model", "model/synthetic",
			"--state-dir", filepath.Join(root, "state"),
		},
		&bytes.Buffer{},
	)

	if err == nil {
		t.Fatal("missing owner UID accepted")
	}
}

func TestParseConfigRejectsRelativeSecurityPaths(
	t *testing.T,
) {
	_, err := parseConfig(
		[]string{
			"--socket", "control.sock",
			"--owner-uid", "1000",
			"--oddc-resolved", "resolved.json",
			"--oddc-model", "model/synthetic",
			"--state-dir", "state",
		},
		&bytes.Buffer{},
	)

	if err == nil {
		t.Fatal("relative security paths accepted")
	}
}

func TestBuildReaderRejectsInvalidTPMHandle(
	t *testing.T,
) {
	_, err := buildReader(config{
		resolvedFile:   "/synthetic/resolved.json",
		oddcModel:      "model/synthetic",
		stateDir:       "/synthetic/state",
		tpmHandle:      "not-a-handle",
		usbguardBinary: "usbguard",
	})

	if err == nil {
		t.Fatal("invalid TPM handle accepted")
	}
}

func TestUnenrolledTrustSourceNeedsNoTPMHandle(
	t *testing.T,
) {
	stateDir := filepath.Join(
		t.TempDir(),
		"never-enrolled",
	)

	reader, err := buildReader(config{
		resolvedFile:   "/synthetic/resolved.json",
		oddcModel:      "model/synthetic",
		stateDir:       stateDir,
		usbguardBinary: "usbguard",
	})
	if err != nil {
		t.Fatal(err)
	}

	document, err := reader.Trust.Trusted(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if document != nil {
		t.Fatal(
			"unenrolled state became trusted document",
		)
	}
}

func TestExistingStateWithoutTPMHandleFailsClosed(
	t *testing.T,
) {
	stateDir := t.TempDir()

	if err := os.Chmod(
		stateDir,
		0700,
	); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"trust.json",
		"trust.sig.json",
	} {
		if err := os.WriteFile(
			filepath.Join(stateDir, name),
			[]byte(`synthetic`),
			0600,
		); err != nil {
			t.Fatal(err)
		}
	}

	reader, err := buildReader(config{
		resolvedFile:   "/synthetic/resolved.json",
		oddcModel:      "model/synthetic",
		stateDir:       stateDir,
		usbguardBinary: "usbguard",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := reader.Trust.Trusted(
		context.Background(),
	); err == nil {
		t.Fatal(
			"existing signed state accepted without TPM signer",
		)
	}
}

func TestParseConfigLapseDays(t *testing.T) {
	root := t.TempDir()
	args := []string{
		"--socket", filepath.Join(root, "control.sock"),
		"--owner-uid", "1000",
		"--oddc-resolved", filepath.Join(root, "resolved.json"),
		"--oddc-model", "model/synthetic",
		"--state-dir", filepath.Join(root, "state"),
	}
	cfg, err := parseConfig(append(args, "--lapse-days", "30"), &bytes.Buffer{})
	if err != nil || cfg.lapseDays != 30 {
		t.Fatalf("lapse days: %d %v", cfg.lapseDays, err)
	}
	reader, err := buildReader(cfg)
	if err != nil || reader.LapseAfter != 30*24*time.Hour {
		t.Fatalf("reader lapse: %v %v", reader.LapseAfter, err)
	}
	if _, err := parseConfig(append(args, "--lapse-days", "-1"), &bytes.Buffer{}); err == nil {
		t.Fatal("negative lapse accepted")
	}
}

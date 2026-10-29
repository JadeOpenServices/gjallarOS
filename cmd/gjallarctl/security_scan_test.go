package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanCheckStates(t *testing.T) {
	f := goodSecurityFacts()
	if c := checkByID(t, securityChecks(f), "scan"); c.State != checkOK || c.Switch != "on" {
		t.Errorf("fresh signatures = %s switch %q, want ok on", c.State, c.Switch)
	}
	f.scanSigs = time.Now().Add(-8 * 24 * time.Hour)
	if c := checkByID(t, securityChecks(f), "scan"); c.State != checkWeak {
		t.Errorf("8 day old signatures = %s, want weak", c.State)
	}
	f.scanSigsErr = fs.ErrNotExist
	if c := checkByID(t, securityChecks(f), "scan"); c.State != checkWeak || !strings.Contains(c.Detail, "No virus signatures yet") {
		t.Errorf("no signatures = %s %q, want weak", c.State, c.Detail)
	}
	f.settings["virusScanEnable"] = "false"
	if c := checkByID(t, securityChecks(f), "scan"); c.State != checkOff || c.Switch != "off" {
		t.Errorf("scan turned off = %s switch %q, want off", c.State, c.Switch)
	}
}

func TestSignatureTimeTakesNewestDaily(t *testing.T) {
	dir := t.TempDir()
	if _, err := signatureTime(dir); err == nil {
		t.Fatal("empty folder has signatures")
	}
	old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	for name, at := range map[string]time.Time{"daily.cvd": old, "daily.cld": recent} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := signatureTime(dir)
	if err != nil || got.Sub(recent).Abs() > time.Second {
		t.Fatalf("signatureTime = %v, %v, want %v", got, err, recent)
	}
}

func TestParseScanOutput(t *testing.T) {
	out := "/home/u/Downloads/a: b.txt: Eicar-Signature FOUND\n/home/u/x: Access denied. ERROR\n\n"
	got := parseScanOutput(out)
	if len(got) != 1 || got[0].Path != "/home/u/Downloads/a: b.txt" || got[0].Signature != "Eicar-Signature" {
		t.Fatalf("parseScanOutput = %+v", got)
	}
}

// fakeScan puts a clamscan on PATH and signatures in place, and replaces
// the sandboxed run with a shell that prints out and exits with code.
func fakeScan(t *testing.T, out string, code int) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "clamscan"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	db := t.TempDir()
	if err := os.WriteFile(filepath.Join(db, "daily.cld"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	oldDB, oldCmd := clamavDBDir, scanCommand
	clamavDBDir = db
	scanCommand = func(clamscan, target string) *exec.Cmd {
		return exec.Command("sh", "-c", `printf '%s' "$1"; exit "$2"`, "sh", out, string(rune('0'+code)))
	}
	t.Cleanup(func() { clamavDBDir, scanCommand = oldDB, oldCmd })
}

func TestSecurityScanReportsFound(t *testing.T) {
	target := t.TempDir()
	fakeScan(t, target+"/eicar.txt: Eicar-Signature FOUND\n", 1)
	var stdout, stderr bytes.Buffer
	if code := runSecurity([]string{"scan", target, "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var result scanResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Found) != 1 || result.Found[0].Signature != "Eicar-Signature" || result.Error != "" {
		t.Errorf("result = %+v", result)
	}
}

func TestSecurityScanClean(t *testing.T) {
	target := t.TempDir()
	fakeScan(t, "", 0)
	var stdout, stderr bytes.Buffer
	if code := runSecurity([]string{"scan", target}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "No virus found") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestSecurityScanFailureIsNotClean(t *testing.T) {
	target := t.TempDir()
	fakeScan(t, "", 2)
	var stdout, stderr bytes.Buffer
	if code := runSecurity([]string{"scan", target}, &stdout, &stderr); code != 2 || strings.Contains(stdout.String(), "No virus found") {
		t.Fatalf("exit %d, stdout %q", code, stdout.String())
	}
}

func TestSecurityScanNeedsSignatures(t *testing.T) {
	target := t.TempDir()
	fakeScan(t, "", 0)
	clamavDBDir = t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runSecurity([]string{"scan", target}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "No virus signatures yet") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

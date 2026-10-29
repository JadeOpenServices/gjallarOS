package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// clamavDBDir is where freshclam keeps the signatures (virus-scan.nix).
var clamavDBDir = "/var/lib/clamav"

// staleSignatures is how old the newest signatures may get before the
// page says so. ClamAV publishes new ones several times a day.
const staleSignatures = 7 * 24 * time.Hour

// signatureTime is when freshclam last wrote the daily signatures.
func signatureTime(dir string) (time.Time, error) {
	var newest time.Time
	for _, name := range []string{"daily.cld", "daily.cvd"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return time.Time{}, err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return time.Time{}, fs.ErrNotExist
	}
	return newest, nil
}

func scanCheck(f securityFacts) securityCheck {
	c := securityCheck{ID: "scan", Title: "Virus scan"}
	if f.settings["virusScanEnable"] == "false" {
		c.State, c.Detail = checkOff, "Off. GjallarOS does not scan files."
		return c
	}
	switch {
	case errors.Is(f.scanSigsErr, fs.ErrNotExist):
		c.State, c.Detail = checkWeak, "No virus signatures yet. They download within the hour while the computer is online."
	case f.scanSigsErr != nil:
		c.State, c.Detail = checkUnknown, "The virus signatures cannot be read."
	case time.Since(f.scanSigs) > staleSignatures:
		c.State, c.Detail = checkWeak, fmt.Sprintf("Signatures from %s are old. They update while the computer is online.", f.scanSigs.Local().Format("2 Jan"))
	default:
		c.State, c.Detail = checkOK, fmt.Sprintf("Scans files when you ask. Signatures from %s.", f.scanSigs.Local().Format("2 Jan 15:04"))
	}
	return c
}

// scanFinding is one file clamscan flagged.
type scanFinding struct {
	Path      string `json:"path"`
	Signature string `json:"signature"`
}

type scanResult struct {
	Target string        `json:"target"`
	Found  []scanFinding `json:"found"`
	Error  string        `json:"error,omitempty"`
}

// scanCommand runs clamscan as the user in a transient unit that has no
// network, sees the system and home read-only and may not gain rights. A
// file that exploits the scanner gets nothing to write to and nobody to
// talk to.
var scanCommand = func(clamscan, target string) *exec.Cmd {
	return exec.Command("systemd-run", "--user", "--pipe", "--wait", "--collect", "--quiet",
		"--unit=gjallar-scan-"+fmt.Sprint(os.Getpid()),
		"-p", "PrivateUsers=yes",
		"-p", "PrivateNetwork=yes",
		"-p", "PrivateTmp=yes",
		"-p", "PrivateDevices=yes",
		"-p", "ProtectSystem=strict",
		"-p", "ProtectHome=read-only",
		"-p", "NoNewPrivileges=yes",
		"-p", "RestrictAddressFamilies=none",
		"-p", "RestrictNamespaces=yes",
		"-p", "LockPersonality=yes",
		"-p", "SystemCallArchitectures=native",
		"-p", "SystemCallFilter=@system-service",
		"-p", "MemoryMax=3G",
		"--", clamscan, "--recursive", "--infected", "--no-summary", "--stdout",
		"--database="+clamavDBDir, "--", target)
}

// parseScanOutput reads clamscan's "PATH: SIGNATURE FOUND" lines; a path
// may hold ": " itself, the signature never does.
func parseScanOutput(out string) []scanFinding {
	found := []scanFinding{}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutSuffix(strings.TrimSpace(line), " FOUND")
		if !ok {
			continue
		}
		i := strings.LastIndex(rest, ": ")
		if i < 0 {
			continue
		}
		found = append(found, scanFinding{Path: rest[:i], Signature: rest[i+2:]})
	}
	return found
}

// runSecurityScan scans a file or folder on demand. Exit 0 is clean, 1 a
// virus was found, 2 the scan could not run.
func runSecurityScan(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	var target string
	for _, arg := range args {
		switch {
		case arg == "--json":
			asJSON = true
		case target == "":
			target = arg
		default:
			fmt.Fprintln(stderr, securityUsage)
			return 2
		}
	}
	if target == "" {
		fmt.Fprintln(stderr, securityUsage)
		return 2
	}
	result := scanResult{Target: target, Found: []scanFinding{}}
	code := scan(&result)
	if asJSON {
		data, err := json.Marshal(result)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 2
		}
		fmt.Fprintln(stdout, string(data))
		return code
	}
	for _, finding := range result.Found {
		fmt.Fprintf(stdout, "%s: %s\n", finding.Path, finding.Signature)
	}
	if result.Error != "" {
		fmt.Fprintf(stderr, "ERROR: %s\n", result.Error)
	} else if code == 0 {
		fmt.Fprintf(stdout, "No virus found in %s.\n", result.Target)
	}
	return code
}

func scan(result *scanResult) int {
	abs, err := filepath.Abs(result.Target)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	if err != nil {
		result.Error = fmt.Sprintf("%s cannot be read.", result.Target)
		return 2
	}
	result.Target = abs
	clamscan, err := exec.LookPath("clamscan")
	if err != nil {
		result.Error = "The virus scan is off. Turn it on in gjallarHorn."
		return 2
	}
	if _, err := signatureTime(clamavDBDir); err != nil {
		result.Error = "No virus signatures yet. They download within the hour while the computer is online."
		return 2
	}
	var out, errOut bytes.Buffer
	cmd := scanCommand(clamscan, abs)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	result.Found = parseScanOutput(out.String())
	var exit *exec.ExitError
	if err == nil {
		return 0
	}
	// clamscan exits 2 when some files could not be read, even when it
	// found a virus in others; a found virus always counts.
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		result.Error = "The scan did not finish."
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			result.Error += " " + lastLine(msg)
		}
	}
	if len(result.Found) > 0 {
		return 1
	}
	if result.Error == "" {
		result.Error = "The scan did not finish."
	}
	return 2
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

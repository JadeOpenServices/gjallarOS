package baremetalinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

// The line from the v2026.10.69 install that stopped (Plane 431), with the
// store path shortened.
const nixDownloadError = "error: unable to download 'https://github.com/example/releases/download/v1/abc.nar.zst': Stream error in the HTTP/2 framing layer (16)\n"

func TestDownloadWatchFindsHostAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	w := &downloadWatch{out: &out}
	half := len(nixDownloadError) / 2
	io.WriteString(w, "copying path '/nix/store/x' from 'https://github.com'...\r")
	io.WriteString(w, nixDownloadError[:half])
	io.WriteString(w, nixDownloadError[half:])
	if got := w.failedHost(); got != "github.com" {
		t.Fatalf("failedHost = %q, want github.com", got)
	}
	if !strings.Contains(out.String(), nixDownloadError) {
		t.Fatalf("output not passed through:\n%s", out.String())
	}
}

func TestDownloadWatchIgnoresOtherErrors(t *testing.T) {
	w := &downloadWatch{out: io.Discard}
	io.WriteString(w, "error: hash mismatch in fixed-output derivation\n")
	io.WriteString(w, "error: unable to download 'not a url': nope\n")
	if got := w.failedHost(); got != "" {
		t.Fatalf("failedHost = %q, want none", got)
	}
}

func TestDownloadWatchCapsUnfinishedLine(t *testing.T) {
	w := &downloadWatch{out: io.Discard}
	io.WriteString(w, strings.Repeat("x", 3*maxWatchLine))
	if len(w.partial) > maxWatchLine {
		t.Fatalf("partial line kept %d bytes, cap is %d", len(w.partial), maxWatchLine)
	}
}

func TestInstallNamesFailedDownload(t *testing.T) {
	repo := testRepo(t)
	r := mountedRunner()
	key := commandKey("sudo", "nixos-install", "--root", "/mnt", "--flake",
		"path:"+repo+"#gjallarOS", "--no-root-passwd")
	r.errors[key] = fmt.Errorf("exit status 1")
	r.stderr = map[string]string{key: nixDownloadError}

	_, err := install(context.Background(), Input{Repo: repo, Hostname: "gjallarOS", Out: io.Discard}, r)
	if err == nil || !strings.Contains(err.Error(), "a download from github.com failed") {
		t.Fatalf("error does not name the failed download: %v", err)
	}
}

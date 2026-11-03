package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

func setupGrantPaths(t *testing.T, channel string) string {
	t.Helper()
	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	user.ReleaseChannel = channel
	cfg, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	oldMarker, oldCfg, oldGrant := grantEnrollmentMarker, grantUserConfig, grantPath
	t.Cleanup(func() { grantEnrollmentMarker, grantUserConfig, grantPath = oldMarker, oldCfg, oldGrant })
	grantEnrollmentMarker = filepath.Join(dir, "tpm2-enrollment-complete")
	grantUserConfig = filepath.Join(dir, "user.config.json")
	// The settings state directory does not exist yet on the first start.
	grantPath = filepath.Join(dir, "settings", "developer-grant.json")
	if err := os.WriteFile(grantUserConfig, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGrantDeveloperAtFirstStartWritesChannelGrant(t *testing.T) {
	setupGrantPaths(t, "staging")
	var out bytes.Buffer
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	if err := grantDeveloperAtFirstStart(devgate.ProofPIN, now, &out); err != nil {
		t.Fatal(err)
	}
	g, err := devgate.Load(grantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Channels || g.DeveloperMode || g.Proof != devgate.ProofPIN || !g.Time.Equal(now) {
		t.Fatalf("grant = %+v", g)
	}
	if err := devgate.Check(grantPath, "staging", false); err != nil {
		t.Fatalf("rebuild check refuses the first-start grant: %v", err)
	}
	info, err := os.Stat(filepath.Dir(grantPath))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir = %v, %v", info, err)
	}
	if !strings.Contains(out.String(), "staging channel confirmed") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestGrantDeveloperAtFirstStartStableWritesNothing(t *testing.T) {
	setupGrantPaths(t, "stable")
	var out bytes.Buffer
	if err := grantDeveloperAtFirstStart(devgate.ProofRecovery, time.Now(), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(grantPath); !os.IsNotExist(err) {
		t.Fatalf("stable install wrote a grant: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q", out.String())
	}
}

func TestGrantDeveloperRefusedAfterEnrollment(t *testing.T) {
	setupGrantPaths(t, "dev")
	if err := os.WriteFile(grantEnrollmentMarker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := grantDeveloperAtFirstStart(devgate.ProofPIN, time.Now(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "confirm-developer") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(grantPath); !os.IsNotExist(err) {
		t.Fatalf("grant written after enrollment: %v", err)
	}
}

func TestGrantDeveloperArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"password"}, {"pin", "extra"}} {
		var stderr bytes.Buffer
		if code := runLUKSGrantDeveloper(args, &bytes.Buffer{}, &stderr); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
	}
}

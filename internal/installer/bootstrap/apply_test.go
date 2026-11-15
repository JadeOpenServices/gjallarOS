package bootstrap

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type applyRig struct {
	steps   []string
	written []byte
	dst     string
	rebuilt string
}

func newApplyRig(t *testing.T, rebuildErr error) *applyRig {
	t.Helper()
	rig := &applyRig{}
	prevRun, prevWrite, prevRebuild := run, writeRootFile, rebuild
	t.Cleanup(func() { run, writeRootFile, rebuild = prevRun, prevWrite, prevRebuild })
	run = func(_ context.Context, name string, args ...string) error {
		rig.steps = append(rig.steps, name+" "+strings.Join(args, " "))
		return nil
	}
	writeRootFile = func(_ context.Context, data []byte, dst string) error {
		rig.steps = append(rig.steps, "write "+dst)
		rig.written, rig.dst = slices.Clone(data), dst
		return nil
	}
	rebuild = func(_ context.Context, configPath string) error {
		rig.steps = append(rig.steps, "rebuild "+configPath)
		rig.rebuilt = configPath
		return rebuildErr
	}
	return rig
}

// The new configuration reaches root through a pipe; no file the user's
// session could swap is left in the temp folder or named to root (L54).
func TestApplyPassesConfigWithoutTempFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	rig := newApplyRig(t, nil)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	backup, err := Apply(context.Background(), "/etc/nixos/configuration.nix", []byte("{ }\n"), now)
	if err != nil || backup != "" {
		t.Fatalf("Apply = %q, %v", backup, err)
	}
	want := []string{
		"sudo cp -a -- /etc/nixos/configuration.nix /etc/nixos/configuration.nix.gjallar-backup.20261115120000",
		"write /etc/nixos/configuration.nix",
		"rebuild /etc/nixos/configuration.nix",
		"sudo rm -f -- /etc/nixos/configuration.nix.gjallar-backup.20261115120000",
	}
	if !slices.Equal(rig.steps, want) {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(rig.steps, "\n"), strings.Join(want, "\n"))
	}
	if string(rig.written) != "{ }\n" {
		t.Errorf("written %q", rig.written)
	}
	for _, step := range rig.steps {
		if strings.Contains(step, tmp) || strings.Contains(step, os.TempDir()) {
			t.Errorf("root step names a temp path: %s", step)
		}
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("temp files left: %v", entries)
	}
}

func TestApplyRestoresOnFailedRebuild(t *testing.T) {
	rig := newApplyRig(t, errors.New("boom"))
	backup, err := Apply(context.Background(), "/etc/nixos/configuration.nix", []byte("{ }\n"), time.Unix(0, 0).UTC())
	if err == nil || backup != "" || !strings.Contains(err.Error(), "previous configuration restored") {
		t.Fatalf("Apply = %q, %v", backup, err)
	}
	if got := rig.steps[len(rig.steps)-2]; !strings.HasPrefix(got, "sudo cp -a -- /etc/nixos/configuration.nix.gjallar-backup.") {
		t.Errorf("no restore: %v", rig.steps)
	}
}

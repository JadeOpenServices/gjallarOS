package app

import (
	"context"
	"errors"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/hardwareconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecideHardwareLifecycle(t *testing.T) {
	tests := []struct {
		name            string
		existing        bool
		skipHardware    bool
		refreshHardware bool
		hardwareExists  bool
		want            hardwareLifecycleAction
	}{
		{"existing retain", true, false, false, true, hardwareRetain},
		{"existing missing generate", true, false, false, false, hardwareGenerate},
		{"existing refresh generate", true, false, true, true, hardwareGenerate},
		{"existing refresh missing generate", true, false, true, false, hardwareGenerate},
		{"skip overrides refresh", true, true, true, true, hardwareSkip},
		{"skip missing remains skip", true, true, false, false, hardwareSkip},
		{"fresh generate", false, false, false, false, hardwareGenerate},
		{"fresh refresh generate", false, false, true, true, hardwareGenerate},
		{"fresh skip", false, true, false, true, hardwareSkip},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideHardwareLifecycle(
				tt.existing,
				tt.skipHardware,
				tt.refreshHardware,
				tt.hardwareExists,
			)
			if got != tt.want {
				t.Fatalf("action = %v, want %v", got, tt.want)
			}
		})
	}
}

func testHardwarePath(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	target := filepath.Join(
		root,
		"generated",
		"hardware.nix",
	)

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}

	return root, target
}

func TestReconcileExistingMissingGenerates(t *testing.T) {
	root, target := testHardwarePath(t)

	calls := 0
	generate := func(
		_ context.Context,
		repo string,
		gotTarget string,
		_ time.Time,
	) (hardwareconfig.Result, error) {
		calls++

		if repo != root {
			t.Fatalf("repo = %q, want %q", repo, root)
		}
		if gotTarget != target {
			t.Fatalf("target = %q, want %q", gotTarget, target)
		}

		if err := os.WriteFile(
			target,
			[]byte("{ config, lib, pkgs, ... }: {}\n"),
			0600,
		); err != nil {
			return hardwareconfig.Result{}, err
		}

		return hardwareconfig.Result{}, nil
	}

	result, err := reconcileHardwareConfiguration(
		context.Background(),
		root,
		target,
		true,
		false,
		false,
		time.Unix(0, 0),
		generate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.action != hardwareGenerate {
		t.Fatalf("action = %v, want generate", result.action)
	}
	if result.existed {
		t.Fatal("missing existing-install hardware was reported as pre-existing")
	}
	if calls != 1 {
		t.Fatalf("generator calls = %d, want 1", calls)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("generated hardware file missing: %v", err)
	}
}

func TestReconcileExistingHardwareRetainsWithoutGeneration(t *testing.T) {
	root, target := testHardwarePath(t)

	original := []byte("existing hardware\n")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	generate := func(
		context.Context,
		string,
		string,
		time.Time,
	) (hardwareconfig.Result, error) {
		calls++
		return hardwareconfig.Result{}, errors.New("generator must not run")
	}

	result, err := reconcileHardwareConfiguration(
		context.Background(),
		root,
		target,
		true,
		false,
		false,
		time.Unix(0, 0),
		generate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.action != hardwareRetain {
		t.Fatalf("action = %v, want retain", result.action)
	}
	if !result.existed {
		t.Fatal("existing hardware not reported as existing")
	}
	if calls != 0 {
		t.Fatalf("generator calls = %d, want 0", calls)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("retained hardware changed: %q", data)
	}
}

func TestReconcileRefreshRegeneratesExisting(t *testing.T) {
	root, target := testHardwarePath(t)

	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	generate := func(
		context.Context,
		string,
		string,
		time.Time,
	) (hardwareconfig.Result, error) {
		calls++
		return hardwareconfig.Result{Backup: target + ".bak.test", DroppedSwap: []string{"/dev/disk/by-uuid/swap-test"}}, nil
	}

	result, err := reconcileHardwareConfiguration(
		context.Background(),
		root,
		target,
		true,
		false,
		true,
		time.Unix(0, 0),
		generate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.action != hardwareGenerate {
		t.Fatalf("action = %v, want generate", result.action)
	}
	if !result.existed {
		t.Fatal("refresh lost pre-existing hardware state")
	}
	if calls != 1 {
		t.Fatalf("generator calls = %d, want 1", calls)
	}
	if len(result.droppedSwap) != 1 || result.droppedSwap[0] != "/dev/disk/by-uuid/swap-test" {
		t.Fatalf("dropped swap = %v", result.droppedSwap)
	}
	if result.backup != target+".bak.test" {
		t.Fatalf("backup = %q", result.backup)
	}
}

func TestReconcileSkipExistingNeverGenerates(t *testing.T) {
	root, target := testHardwarePath(t)

	if err := os.WriteFile(target, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	generate := func(
		context.Context,
		string,
		string,
		time.Time,
	) (hardwareconfig.Result, error) {
		calls++
		return hardwareconfig.Result{}, errors.New("generator must not run")
	}

	result, err := reconcileHardwareConfiguration(
		context.Background(),
		root,
		target,
		true,
		true,
		true,
		time.Unix(0, 0),
		generate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.action != hardwareSkip {
		t.Fatalf("action = %v, want skip", result.action)
	}
	if calls != 0 {
		t.Fatalf("generator calls = %d, want 0", calls)
	}
}

func TestReconcileSkipMissingFailsBeforeGeneration(t *testing.T) {
	root, target := testHardwarePath(t)

	calls := 0
	generate := func(
		context.Context,
		string,
		string,
		time.Time,
	) (hardwareconfig.Result, error) {
		calls++
		return hardwareconfig.Result{}, nil
	}

	_, err := reconcileHardwareConfiguration(
		context.Background(),
		root,
		target,
		true,
		true,
		false,
		time.Unix(0, 0),
		generate,
	)

	if err == nil {
		t.Fatal("skip-hardware accepted missing generated hardware configuration")
	}
	if !strings.Contains(err.Error(), "refusing to continue to flake validation") {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("generator calls = %d, want 0", calls)
	}
}

func TestHardwareConfigurationExistsRejectsDirectory(t *testing.T) {
	_, target := testHardwarePath(t)

	if err := os.Remove(filepath.Dir(target)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := hardwareConfigurationExists(target); err == nil {
		t.Fatal("directory accepted as hardware configuration")
	}
}

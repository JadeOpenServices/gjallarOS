package oddcvalidation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

type commandCall struct {
	dir  string
	name string
	args []string
}

func TestCommandGatesUseExactCommands(t *testing.T) {
	var calls []commandCall

	runner := func(
		ctx context.Context,
		dir string,
		name string,
		args ...string,
	) ([]byte, error) {
		calls = append(calls, commandCall{
			dir:  dir,
			name: name,
			args: append([]string(nil), args...),
		})
		return nil, nil
	}

	repo := "/repo"

	results := []Result{
		runFocusedODDCTests(context.Background(), runner, repo),
		runAllGoTests(context.Background(), runner, repo),
		runFlakeCheck(context.Background(), runner, repo),
	}

	for _, result := range results {
		if !result.Passed {
			t.Fatalf("unexpected failed gate: %+v", result)
		}
	}

	want := []commandCall{
		{
			dir:  repo,
			name: "go",
			args: []string{
				"test",
				"./internal/installer/oddc",
				"./internal/installer/oddcvalidation",
			},
		},
		{
			dir:  repo,
			name: "go",
			args: []string{"test", "./..."},
		},
		{
			dir:  repo,
			name: "nix",
			args: []string{
				"flake",
				"check",
				"--no-build",
				"--all-systems",
				"path:.",
			},
		},
	}

	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands=%#v want %#v", calls, want)
	}
}

func TestCommandGateReportsFailure(t *testing.T) {
	runner := func(
		ctx context.Context,
		dir string,
		name string,
		args ...string,
	) ([]byte, error) {
		return []byte("boom"), errors.New("exit status 1")
	}

	result := runAllGoTests(
		context.Background(),
		runner,
		"/repo",
	)

	if result.Passed {
		t.Fatal("failed command gate was accepted")
	}
	if result.Details == "" {
		t.Fatal("failed command gate had no details")
	}
}

func TestDeviceEvaluationUsesConfiguredHost(t *testing.T) {
	var got commandCall

	runner := func(
		ctx context.Context,
		dir string,
		name string,
		args ...string,
	) ([]byte, error) {
		got = commandCall{
			dir:  dir,
			name: name,
			args: append([]string(nil), args...),
		}
		return []byte("/nix/store/test-system.drv"), nil
	}

	result := runDeviceEvaluation(
		context.Background(),
		runner,
		"/repo",
		"gjallarOS",
	)
	if !result.Passed {
		t.Fatalf("device evaluation failed: %+v", result)
	}

	want := commandCall{
		dir:  "/repo",
		name: "nix",
		args: []string{
			"eval",
			"--raw",
			"path:.#nixosConfigurations.gjallarOS.config.system.build.toplevel.drvPath",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command=%#v want %#v", got, want)
	}
}

func TestRealMachineRebuildUsesGjallarctl(t *testing.T) {
	var got commandCall

	runner := func(
		ctx context.Context,
		dir string,
		name string,
		args ...string,
	) ([]byte, error) {
		got = commandCall{
			dir:  dir,
			name: name,
			args: append([]string(nil), args...),
		}
		return nil, nil
	}

	result := runRealMachineRebuild(
		context.Background(),
		runner,
		systemcheckout.Path,
		"/usr/bin/gjallarctl",
	)
	if !result.Passed {
		t.Fatalf("rebuild gate failed: %+v", result)
	}

	want := commandCall{
		dir:  systemcheckout.Path,
		name: "/usr/bin/gjallarctl",
		args: []string{"rebuild", "--no-cleanup"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command=%#v want %#v", got, want)
	}

	// Any other checkout fails without running anything (Q199).
	got = commandCall{}
	result = runRealMachineRebuild(context.Background(), runner, "/home/someone/gjallarOS", "/usr/bin/gjallarctl")
	if result.Passed || got.name != "" || !strings.Contains(result.Details, "only the system checkout") {
		t.Fatalf("user checkout: %+v, ran %#v", result, got)
	}
}

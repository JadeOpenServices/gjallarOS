package installstate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshBaselineAndRerun(t *testing.T) {
	repo := t.TempDir()
	if err := Ensure(context.Background(), repo, nil, "tester", "26.05"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "generated", "install-state.nix")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"nixosStateVersion", "homeManagerStateVersion"} {
		if !strings.Contains(string(before), field+` = "26.05";`) {
			t.Fatalf("missing %s in %s", field, before)
		}
	}
	if err := Ensure(context.Background(), repo, &Existing{Dir: "/nonexistent", Host: "synthetic"}, "tester", "26.11"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("rerun advanced compatibility baselines")
	}
}

func TestExistingVersionsRemainIndependentOfRelease(t *testing.T) {
	repo := t.TempDir()
	if err := ensure(repo, func() (Versions, error) {
		return Versions{NixOS: "23.11", Home: "24.05"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(repo, "generated", "install-state.nix"))
	if !strings.Contains(string(data), `nixosStateVersion = "23.11"`) ||
		!strings.Contains(string(data), `homeManagerStateVersion = "24.05"`) {
		t.Fatalf("historical versions lost: %s", data)
	}
}

func TestFailedDiscoveryDoesNotPublishState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		versions Versions
		err      error
	}{
		{"evaluation failure", Versions{}, fmt.Errorf("configuration unavailable")},
		{"invalid version", Versions{NixOS: "26.05", Home: ""}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := ensure(repo, func() (Versions, error) { return tc.versions, tc.err }); err == nil {
				t.Fatal("accepted missing compatibility state")
			}
			if _, err := os.Stat(filepath.Join(repo, "generated", "install-state.nix")); !os.IsNotExist(err) {
				t.Fatal("published state after discovery failure")
			}
		})
	}
}

// fakeTool puts a SYNTHETIC stand-in for a Nix command first on PATH. It
// writes its arguments, one per line, to args and runs body.
func fakeTool(t *testing.T, name, body string) (args string) {
	bin := t.TempDir()
	args = filepath.Join(bin, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + args + "\n" + body
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return args
}

func TestFailedEvaluationShowsNixMessage(t *testing.T) {
	fakeTool(t, "nix-instantiate", "printf 'error:\\n       SYNTHETIC trace line\\n\\n       error: SYNTHETIC reason\\n' >&2\nexit 1\n")
	repo := t.TempDir()
	err := Ensure(context.Background(), repo, &Existing{Dir: t.TempDir(), Host: "synthetic"}, "tester", "26.05")
	if err == nil || !strings.Contains(err.Error(), "exit status 1\nerror:") || !strings.HasSuffix(err.Error(), "error: SYNTHETIC reason") {
		t.Fatalf("error without Nix's message: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "generated", "install-state.nix")); !os.IsNotExist(err) {
		t.Fatal("published state after a failed evaluation")
	}
}

func TestPlainConfiguration(t *testing.T) {
	args := fakeTool(t, "nix-instantiate", "echo '{\"nixosStateVersion\":\"23.11\",\"homeManagerStateVersion\":\"24.05\"}'\n")
	dir := t.TempDir()
	repo := t.TempDir()
	if err := Ensure(context.Background(), repo, &Existing{Dir: dir, Host: "synthetic"}, "tester", "26.05"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(args)
	if !strings.Contains(string(got), "nixos-config="+filepath.Join(dir, "configuration.nix")+"\n") {
		t.Errorf("arguments: %s", got)
	}
	data, _ := os.ReadFile(filepath.Join(repo, "generated", "install-state.nix"))
	if !strings.Contains(string(data), `nixosStateVersion = "23.11"`) {
		t.Errorf("state: %s", data)
	}
}

// A flake's configuration.nix can need options only the flake brings, so
// the flake is read, as nixos-rebuild builds it (report 2026-11-02:
// "read existing versions from /etc/nixos/configuration.nix: exit status 1"
// on a NixOS set up with a lanzaboote flake).
func TestFlakeConfiguration(t *testing.T) {
	args := fakeTool(t, "nix", "echo '{\"nixosStateVersion\":\"25.05\",\"homeManagerStateVersion\":\"26.05\"}'\n")
	real := t.TempDir()
	for _, f := range []string{"flake.nix", "configuration.nix"} {
		if err := os.WriteFile(filepath.Join(real, f), []byte("# SYNTHETIC\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// /etc/nixos/flake.nix may be a link to the flake elsewhere.
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(real, "flake.nix"), filepath.Join(dir, "flake.nix")); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := Ensure(context.Background(), repo, &Existing{Dir: dir, Host: "synthetic-host"}, "tester", "26.05"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(args)
	if !strings.Contains(string(got), "\n"+real+`#nixosConfigurations."synthetic-host".config`+"\n--apply\n") ||
		!strings.Contains(string(got), "nix-command flakes\n") || !strings.Contains(string(got), "--no-write-lock-file\n") {
		t.Errorf("arguments: %s", got)
	}
	data, _ := os.ReadFile(filepath.Join(repo, "generated", "install-state.nix"))
	if !strings.Contains(string(data), `nixosStateVersion = "25.05"`) || !strings.Contains(string(data), `homeManagerStateVersion = "26.05"`) {
		t.Errorf("state: %s", data)
	}

	for _, host := range []string{`x"; y = "`, "-x", "a#b"} {
		err := Ensure(context.Background(), t.TempDir(), &Existing{Dir: dir, Host: host}, "tester", "26.05")
		if err == nil || !strings.Contains(err.Error(), "cannot name a flake configuration") {
			t.Errorf("host %q: %v", host, err)
		}
	}
}

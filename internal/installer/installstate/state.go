// Package installstate initializes compatibility baselines once. These versions
// describe data migrations, so an installer rerun must never advance them.
package installstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/nixrender"
)

type Versions struct {
	NixOS string `json:"nixosStateVersion"`
	Home  string `json:"homeManagerStateVersion"`
}

var versionPattern = regexp.MustCompile(`^[0-9]{2}\.(05|11)$`)

// Ensure preserves an existing file. For an existing NixOS installation it
// evaluates that installation's configuration instead of using its release as
// the historical stateVersion. An empty configPath denotes a fresh target.
func Ensure(ctx context.Context, repo, configPath, username, release string) error {
	return ensure(repo, func() (Versions, error) {
		versions := Versions{NixOS: release, Home: release}
		if configPath == "" {
			return versions, nil
		}
		expr := `let c = (import <nixpkgs/nixos> {}).config; u = ` + nixrender.String(username) + `;
in {
  nixosStateVersion = c.system.stateVersion;
  homeManagerStateVersion = if c ? home-manager && builtins.hasAttr u c.home-manager.users
    then c.home-manager.users.${u}.home.stateVersion else ` + nixrender.String(release) + `;
}`
		out, err := exec.CommandContext(ctx, "nix-instantiate", "--eval", "--strict", "--json",
			"-I", "nixos-config="+configPath, "--expr", expr).Output()
		if err != nil {
			return Versions{}, fmt.Errorf("read existing compatibility versions from %s: %w%s", configPath, err, nixMessage(err))
		}
		if err := json.Unmarshal(out, &versions); err != nil {
			return Versions{}, fmt.Errorf("decode existing compatibility versions: %w", err)
		}
		return versions, nil
	})
}

// nixMessageMax is how much of Nix's own error text a failure shows; the
// reason is at the end, after the trace.
const nixMessageMax = 2048

// nixMessage gives what nix-instantiate printed on a failed run, so the
// installer shows why the configuration could not be read.
func nixMessage(err error) string {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return ""
	}
	msg := strings.TrimSpace(string(exit.Stderr))
	if msg == "" {
		return ""
	}
	if len(msg) > nixMessageMax {
		msg = "..." + msg[len(msg)-nixMessageMax:]
	}
	return "\n" + msg
}

func ensure(repo string, discover func() (Versions, error)) error {
	path := filepath.Join(repo, "generated", "install-state.nix")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("compatibility state must be a nonempty regular file: %s", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	versions, err := discover()
	if err != nil {
		return err
	}
	if !versionPattern.MatchString(versions.NixOS) || !versionPattern.MatchString(versions.Home) {
		return fmt.Errorf("invalid compatibility versions: NixOS=%q Home Manager=%q", versions.NixOS, versions.Home)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".install-state.nix-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "# Historical compatibility baselines; do not advance on upgrades.\n{\n  nixosStateVersion = %s;\n  homeManagerStateVersion = %s;\n}\n",
		nixrender.String(versions.NixOS), nixrender.String(versions.Home)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Publish atomically without overwriting a concurrently created baseline.
	return os.Link(f.Name(), path)
}

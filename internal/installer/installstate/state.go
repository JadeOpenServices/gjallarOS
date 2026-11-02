// Package installstate initializes compatibility baselines once. These versions
// describe data migrations, so an installer rerun must never advance them.
package installstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// Existing names the NixOS installation the installer runs on: its
// configuration folder (/etc/nixos) and its host name.
type Existing struct {
	Dir  string
	Host string
}

// hostPattern is what may stand between the quotes of a flake attribute.
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Ensure preserves an existing file. For an existing NixOS installation it
// evaluates that installation's configuration instead of using its release as
// the historical stateVersion. A nil existing denotes a fresh target.
//
// The configuration is the one nixos-rebuild builds by default: the flake in
// the folder when it has a flake.nix, nixosConfigurations."<host>" in it,
// else configuration.nix through <nixpkgs/nixos>. A flake's
// configuration.nix alone can set options only its flake brings (such as
// boot.lanzaboote), so it does not evaluate on its own.
func Ensure(ctx context.Context, repo string, existing *Existing, username, release string) error {
	return ensure(repo, func() (Versions, error) {
		versions := Versions{NixOS: release, Home: release}
		if existing == nil {
			return versions, nil
		}
		read := `{
  nixosStateVersion = c.system.stateVersion;
  homeManagerStateVersion = if c ? home-manager && builtins.hasAttr u c.home-manager.users
    then c.home-manager.users.${u}.home.stateVersion else ` + nixrender.String(release) + `;
}`
		u := nixrender.String(username)
		var cmd *exec.Cmd
		var source string
		flake, err := filepath.EvalSymlinks(filepath.Join(existing.Dir, "flake.nix"))
		switch {
		case err == nil:
			host := existing.Host
			if host == "" {
				host = "default"
			}
			if !hostPattern.MatchString(host) {
				return Versions{}, fmt.Errorf("host name %q cannot name a flake configuration", host)
			}
			source = filepath.Dir(flake) + `#nixosConfigurations."` + host + `"`
			cmd = exec.CommandContext(ctx, "nix", "--extra-experimental-features", "nix-command flakes",
				"eval", "--json", "--no-write-lock-file", source+".config", "--apply", "c: let u = "+u+"; in "+read)
		case errors.Is(err, fs.ErrNotExist):
			source = filepath.Join(existing.Dir, "configuration.nix")
			cmd = exec.CommandContext(ctx, "nix-instantiate", "--eval", "--strict", "--json",
				"-I", "nixos-config="+source, "--expr", "let c = (import <nixpkgs/nixos> {}).config; u = "+u+"; in "+read)
		default:
			return Versions{}, fmt.Errorf("read existing compatibility versions: %w", err)
		}
		out, err := cmd.Output()
		if err != nil {
			return Versions{}, fmt.Errorf("read existing compatibility versions from %s: %w%s", source, err, nixMessage(err))
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

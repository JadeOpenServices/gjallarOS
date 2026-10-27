package release

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Policy struct {
	Release string `json:"release"`
}

func Expected(repo string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repo, "deployment", "release-policy.json"))
	if err != nil {
		return "", fmt.Errorf("read release policy: %w", err)
	}

	var policy Policy
	if err := json.Unmarshal(data, &policy); err != nil {
		return "", fmt.Errorf("parse release policy: %w", err)
	}

	if strings.TrimSpace(policy.Release) == "" {
		return "", fmt.Errorf("release policy does not define a NixOS release")
	}

	return policy.Release, nil
}

func Inspect(repo, osRelease string) (expected, actual string, err error) {
	expected, err = Expected(repo)
	if err != nil {
		return "", "", err
	}

	f, err := os.Open(osRelease)
	if err != nil {
		return "", "", fmt.Errorf("read OS release: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok && key == "VERSION_ID" {
			actual = strings.Trim(value, `"`)
		}
	}
	if actual == "" {
		return "", "", fmt.Errorf("could not determine the active NixOS release")
	}
	return expected, actual, scanner.Err()
}

// Source is the nixpkgs revision flake.lock pins for the release.
type Source struct {
	URL     string
	NarHash string
}

// Pinned reads the nixpkgs input from flake.lock. The lock must follow the
// release branch of expected, so a channel switch lands on the same tree the
// flake later builds.
func Pinned(repo, expected string) (Source, error) {
	data, err := os.ReadFile(filepath.Join(repo, "flake.lock"))
	if err != nil {
		return Source{}, fmt.Errorf("read flake.lock: %w", err)
	}
	type ref struct {
		Type, Owner, Repo, Rev, Ref, NarHash string
	}
	var lock struct {
		Root  string `json:"root"`
		Nodes map[string]struct {
			Inputs   map[string]json.RawMessage `json:"inputs"`
			Locked   ref                        `json:"locked"`
			Original ref                        `json:"original"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return Source{}, fmt.Errorf("parse flake.lock: %w", err)
	}
	var name string
	if err := json.Unmarshal(lock.Nodes[lock.Root].Inputs["nixpkgs"], &name); err != nil {
		return Source{}, fmt.Errorf("flake.lock has no nixpkgs root input")
	}
	node := lock.Nodes[name]
	locked := node.Locked
	if locked.Type != "github" || locked.Owner == "" || locked.Repo == "" || len(locked.Rev) != 40 || !strings.HasPrefix(locked.NarHash, "sha256-") {
		return Source{}, fmt.Errorf("flake.lock nixpkgs is not a locked GitHub revision")
	}
	if node.Original.Ref != "nixos-"+expected {
		return Source{}, fmt.Errorf("flake.lock nixpkgs follows %q, not nixos-%s", node.Original.Ref, expected)
	}
	return Source{
		URL:     "https://github.com/" + locked.Owner + "/" + locked.Repo + "/archive/" + locked.Rev + ".tar.gz",
		NarHash: locked.NarHash,
	}, nil
}

// Commands lists what Align runs. The rebuild uses the tree fetched against
// the flake.lock hash; the channel points at the same revision so later
// channel use stays on it instead of a moving branch.
func Commands(src Source, tree, nixosConfig string) [][]string {
	return [][]string{
		{"nix-channel", "--add", src.URL, "nixos"},
		{"nix-channel", "--update", "nixos"},
		{"nixos-rebuild", "boot", "-I", "nixpkgs=" + tree, "-I", "nixos-config=" + nixosConfig},
	}
}

// fetchExpr fetches url and fails unless its NAR hash is hash.
const fetchExpr = `{ url, hash }: builtins.fetchTarball { inherit url; sha256 = hash; }`

func Align(ctx context.Context, repo, expected, nixosConfig string) error {
	src, err := Pinned(repo, expected)
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "nix-instantiate", "--eval", "--json", "--argstr", "url", src.URL, "--argstr", "hash", src.NarHash, "--expr", fetchExpr).Output()
	if err != nil {
		return fmt.Errorf("fetch pinned nixpkgs %s: %w", src.URL, err)
	}
	var tree string
	if err := json.Unmarshal(out, &tree); err != nil || !strings.HasPrefix(tree, "/nix/store/") {
		return fmt.Errorf("fetch pinned nixpkgs %s: unexpected result %q", src.URL, strings.TrimSpace(string(out)))
	}
	for _, args := range Commands(src, tree, nixosConfig) {
		cmd := exec.CommandContext(ctx, "sudo", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sudo %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

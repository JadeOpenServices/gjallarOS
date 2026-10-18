package installercheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverRepository resolves the active GjallarOS checkout.
//
// Order:
//  1. explicit --repo
//  2. GJALLAROS_REPO
//  3. last successfully rebuilt checkout
//  4. the system's checkout, SystemRepositoryFile
//  5. cwd and its parents
func DiscoverRepository(explicit string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine current directory: %w", err)
	}

	remembered, _ := readRememberedRepository()
	system, _ := os.ReadFile(SystemRepositoryFile)

	return discoverRepository(
		explicit,
		os.Getenv("GJALLAROS_REPO"),
		remembered,
		string(system),
		cwd,
	)
}

// SystemRepositoryFile names the checkout the running system was built
// from (settings.dotfilesDir), so every GjallarOS deployment has one fixed
// place to find it.
var SystemRepositoryFile = "/etc/gjallar/repository"

func discoverRepository(
	explicit string,
	environment string,
	remembered string,
	system string,
	start string,
) (string, error) {
	for _, candidate := range []struct {
		name string
		path string
	}{
		{"explicit repository", strings.TrimSpace(explicit)},
		{"GJALLAROS_REPO", strings.TrimSpace(environment)},
		{"remembered repository", strings.TrimSpace(remembered)},
		{"system repository", strings.TrimSpace(system)},
	} {
		if candidate.path == "" {
			continue
		}

		root, err := ResolveRepository(candidate.path)
		if err == nil {
			return root, nil
		}

		// Stale pointers fall through; explicit choices must resolve.
		if candidate.name != "remembered repository" &&
			candidate.name != "system repository" {
			return "", fmt.Errorf(
				"%s %q: %w",
				candidate.name,
				candidate.path,
				err,
			)
		}
	}

	current, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf(
			"resolve repository search path: %w",
			err,
		)
	}

	for {
		if root, err := ResolveRepository(current); err == nil {
			return root, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return "", fmt.Errorf(
		"no GjallarOS repository found; use --repo PATH or GJALLAROS_REPO",
	)
}

func RememberRepository(root string) error {
	root, err := ResolveRepository(root)
	if err != nil {
		return err
	}

	path, err := repositoryStatePath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create repository state directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".repository-*")
	if err != nil {
		return fmt.Errorf("create repository state file: %w", err)
	}

	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}

	if _, err := fmt.Fprintln(tmp, root); err != nil {
		tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

func readRememberedRepository() (string, error) {
	path, err := repositoryStatePath()
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

func repositoryStatePath() (string, error) {
	if state := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); state != "" {
		return filepath.Join(state, "gjallarOS", "repository"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(
		home,
		".local",
		"state",
		"gjallarOS",
		"repository",
	), nil
}

package installercheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

// DiscoverRepository resolves the active GjallarOS checkout.
//
// Order:
//  1. explicit --repo
//  2. GJALLAROS_REPO
//  3. the system's checkout, SystemRepositoryFile
//  4. cwd and its parents
//
// When SystemRepositoryFile names the root-owned system checkout, that
// checkout moves up to 2: the system builds nothing else, so a clone left
// in GJALLAROS_REPO must not take settings the system never reads.
func DiscoverRepository(explicit string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine current directory: %w", err)
	}

	system, _ := os.ReadFile(SystemRepositoryFile)

	return discoverRepository(
		explicit,
		os.Getenv("GJALLAROS_REPO"),
		string(system),
		cwd,
	)
}

// SystemRepositoryFile names the checkout the running system was built
// from (settings.dotfilesDir), so every GjallarOS deployment has one fixed
// place to find it.
var SystemRepositoryFile = "/etc/gjallar/repository"

// systemCheckoutPath is systemcheckout.Path; tests point it elsewhere.
var systemCheckoutPath = systemcheckout.Path

func discoverRepository(
	explicit string,
	environment string,
	system string,
	start string,
) (string, error) {
	type candidate struct {
		name string
		path string
	}
	candidates := []candidate{
		{"explicit repository", strings.TrimSpace(explicit)},
		{"GJALLAROS_REPO", strings.TrimSpace(environment)},
		{"system repository", strings.TrimSpace(system)},
	}
	if s := strings.TrimSpace(system); s != "" && filepath.Clean(s) == systemCheckoutPath {
		candidates = []candidate{
			candidates[0],
			{"system checkout", systemCheckoutPath},
		}
	}
	stale := ""
	for _, candidate := range candidates {
		if candidate.path == "" {
			continue
		}

		root, err := ResolveRepository(candidate.path)
		if err == nil {
			return root, nil
		}

		if candidate.name == "system checkout" {
			return "", fmt.Errorf(
				"the system checkout %s is missing or broken: %w; "+
					"with a copy of this machine's generated/ and user.config.json, "+
					"move %s away and run: sudo gjallarctl system clone --from DIR",
				candidate.path, err, candidate.path,
			)
		}
		if candidate.name == "system repository" {
			stale = candidate.path
		}
		// A stale system pointer falls through; explicit choices must resolve.
		if candidate.name != "system repository" {
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

	if stale != "" {
		return "", fmt.Errorf(
			"the checkout at %s that this system was built from is missing or empty; "+
				"use --repo PATH or GJALLAROS_REPO",
			stale,
		)
	}
	return "", fmt.Errorf(
		"no GjallarOS repository found; use --repo PATH or GJALLAROS_REPO",
	)
}

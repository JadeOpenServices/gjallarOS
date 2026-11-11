package sourcerevision

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const EnvironmentVariable = "GJALLAROS_REVISION"

func Resolve(repo string, recovery bool) (string, error) {
	if revision := strings.TrimSpace(os.Getenv(EnvironmentVariable)); revision != "" {
		return revision, nil
	}

	if recovery {
		return "", fmt.Errorf(
			"%s is required in recovery mode",
			EnvironmentVariable,
		)
	}

	output, err := exec.Command(
		"git",
		"-C",
		repo,
		"rev-parse",
		"HEAD",
	).Output()
	if err != nil {
		return "", fmt.Errorf(
			"resolve gjallarOS repository revision: %w",
			err,
		)
	}

	revision := strings.TrimSpace(string(output))
	if revision == "" {
		return "", fmt.Errorf(
			"resolve gjallarOS repository revision: empty revision",
		)
	}

	return revision, nil
}

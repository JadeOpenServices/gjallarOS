package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

// runningAnswers reads the answers the installed system was built from: the
// user.config.json of the checkout /etc/gjallar/repository names. A rerun on
// an installed system offers them as the defaults, so Enter keeps what the
// machine runs. They are this machine's own saved answers, not values taken
// from the account or git config. Without them it says why and returns nil,
// and the interview offers what a new install gets. Swapped in tests.
var runningAnswers = func(ctx context.Context, out io.Writer) *config.User {
	user, err := readRunningAnswers(ctx, installercheck.SystemRepositoryFile)
	if err != nil {
		fmt.Fprintf(out, "The answers of the running system are not available (%v); the questions start from the new-install defaults.\n", err)
		return nil
	}
	fmt.Fprintln(out, "The questions start from the answers of the running system; Enter keeps each one.")
	return user
}

// readPrivileged reads a file of the root-owned system checkout that is
// not readable for the installing account.
var readPrivileged = func(ctx context.Context, path string) ([]byte, error) {
	return exec.CommandContext(ctx, "sudo", "cat", "--", path).Output()
}

func readRunningAnswers(ctx context.Context, repositoryFile string) (*config.User, error) {
	data, err := os.ReadFile(repositoryFile)
	if err != nil {
		return nil, err
	}
	checkout := strings.TrimSpace(string(data))
	if !filepath.IsAbs(checkout) {
		return nil, fmt.Errorf("%s does not name an absolute checkout path", repositoryFile)
	}
	path := filepath.Join(filepath.Clean(checkout), "user.config.json")
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrPermission) {
		contents, err = readPrivileged(ctx, path)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	user, err := config.Parse(contents)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &user, nil
}

// interviewDefaults are the answers the interview offers. A new install
// gets fixed starting points and leaves personal values empty, so their
// prompts show made-up examples. A rerun on an installed system gets the
// running answers; a value the running system lacks, or one this checkout
// no longer offers, falls back to the new-install default.
func interviewDefaults(running *config.User, o discovery.Options) config.User {
	fresh := config.User{
		Hostname:               "gjallarOS",
		Timezone:               "Europe/Berlin",
		Locale:                 "en_US.UTF-8",
		KeyboardLayout:         "de",
		TouchpadWorkspaceSwipe: true,
		ClamshellEnable:        true,
		Shell:                  first(o.Shells),
		Editors:                []string{"vscodium"},
		Browsers:               []string{first(o.Browsers)},
	}
	if running == nil {
		return fresh
	}
	a := *running
	for _, field := range []struct {
		value    *string
		fallback string
	}{
		{&a.Hostname, fresh.Hostname},
		{&a.Timezone, fresh.Timezone},
		{&a.Locale, fresh.Locale},
		{&a.KeyboardLayout, fresh.KeyboardLayout},
	} {
		if strings.TrimSpace(*field.value) == "" {
			*field.value = field.fallback
		}
	}
	if !slices.Contains(o.Shells, a.Shell) {
		a.Shell = fresh.Shell
	}
	if a.Editors = offered(a.Editors, o.Editors); len(a.Editors) == 0 {
		a.Editors = fresh.Editors
	}
	if a.Browsers = offered(a.Browsers, o.Browsers); len(a.Browsers) == 0 {
		a.Browsers = fresh.Browsers
	}
	return a
}

// offered keeps the values the options still list.
func offered(values, options []string) []string {
	kept := []string{}
	for _, value := range values {
		if slices.Contains(options, value) {
			kept = append(kept, value)
		}
	}
	return kept
}

// askPersonal offers the running answer of a personal value; without one
// the prompt shows a made-up example and fills in nothing.
func askPersonal(ctx context.Context, ui prompt.UI, label, running, example string, required bool) (string, error) {
	if running != "" {
		return ui.Value(ctx, label, running)
	}
	return ui.Example(ctx, label, example, required)
}

// offeredOr is value when options list it, else fallback.
func offeredOr(value string, options []string, fallback string) string {
	if slices.Contains(options, value) {
		return value
	}
	return fallback
}

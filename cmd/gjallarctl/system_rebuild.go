package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

// systemRebuildCommand is the root side of a system checkout rebuild. It is
// part of the running system, so the user session cannot pick what root
// runs.
var systemRebuildCommand = "/run/current-system/sw/bin/gjallar-system-rebuild"

type rebuildOptions struct {
	repo, host           string
	debug, cleanup       bool
	hardwareUpdate       bool
	channel, switchNow   string
	update, updateInputs bool
	rebuildArgs          []string
}

// systemRebuildArgs turns the user's rebuild options into the fixed argument
// list of the root side. The checkout, host and nixos-rebuild arguments are
// not the user's to choose; only a checked oddc commit passes.
func systemRebuildArgs(opts rebuildOptions) ([]string, error) {
	switch {
	case opts.repo != "":
		return nil, fmt.Errorf("this system builds %s; --repo is not used", systemcheckout.Path)
	case opts.host != "":
		return nil, fmt.Errorf("this system builds the host in its configuration; --host is not used")
	case opts.updateInputs:
		return nil, fmt.Errorf("--update-inputs is for development checkouts, not %s", systemcheckout.Path)
	}
	args := []string{}
	if opts.update {
		args = append(args, "--update")
	}
	if opts.hardwareUpdate {
		args = append(args, "--hardware-update")
	}
	if opts.channel != "" {
		args = append(args, "--channel", opts.channel)
	}
	if opts.switchNow != "" {
		args = append(args, "--switch", opts.switchNow)
	}
	if opts.debug {
		args = append(args, "--debug")
	}
	if !opts.cleanup {
		args = append(args, "--no-cleanup")
	}
	extra := opts.rebuildArgs
	if len(extra) == 3 && extra[0] == "--override-input" && extra[1] == "oddc" {
		rev, err := oddcOverrideRevision(extra)
		if err != nil {
			return nil, err
		}
		return append(args, "--oddc-commit", rev), nil
	}
	if len(extra) > 0 {
		return nil, fmt.Errorf("%s is not passed to the system rebuild; only --override-input oddc <commit> is", extra[0])
	}
	return args, nil
}

// startSystemRebuild authenticates and hands the rebuild to the root side.
func startSystemRebuild(opts rebuildOptions, stdout, stderr io.Writer) int {
	args, err := systemRebuildArgs(opts)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: %v\n", err)
		return 2
	}
	home, _ := os.UserHomeDir()
	rootArgs := append([]string{"env", "GJALLAR_REBUILD_CALLER_HOME=" + home, systemRebuildCommand}, args...)

	userConfig, err := config.Load(filepath.Join(systemcheckout.Path, "user.config.json"))
	if err == nil && userConfig.EndpointManagedDevice && os.Geteuid() != 0 {
		fmt.Fprintln(stdout, "[GjallarOS] Managed endpoint: authenticating as root for rebuild.")
		return runCommand(context.Background(), stdout, stderr, "su", "-l", "-c", shellJoin(rootArgs), "root")
	}
	return runPrivilegedCommand(context.Background(), stdout, stderr, rootArgs...)
}

func runSystem(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "check" && args[0] != "rebuild") {
		fmt.Fprintln(stderr, "Usage: gjallarctl system check|rebuild [OPTIONS]")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintf(stderr, "ERROR: gjallarctl system %s runs as root; use rebuild\n", args[0])
		return 2
	}
	if err := systemcheckout.CheckOwnership(systemcheckout.Path); err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: %v; nothing rebuilt\n", err)
		return 1
	}
	// The installed gjallarctl runs check before the checkout's own
	// gjallarctl is built, so that one comes from a checked source too.
	if args[0] == "check" {
		return checkSystemSource(systemcheckout.Path, stdout, stderr)
	}
	opts, err := parseSystemRebuildArgs(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}
	return rebuildCheckout(systemcheckout.Path, opts, true, stdout, stderr)
}

// parseSystemRebuildArgs reads the fixed argument list systemRebuildArgs
// writes and nothing else.
func parseSystemRebuildArgs(args []string) (rebuildOptions, error) {
	opts := rebuildOptions{cleanup: true}
	for i := 0; i < len(args); i++ {
		value := func(allowed ...string) (string, error) {
			if i+1 < len(args) {
				for _, candidate := range allowed {
					if args[i+1] == candidate {
						i++
						return args[i], nil
					}
				}
			}
			return "", fmt.Errorf("%s requires %s", args[i], strings.Join(allowed, " or "))
		}
		var err error
		switch args[i] {
		case "--update":
			opts.update = true
		case "--hardware-update":
			opts.hardwareUpdate = true
		case "--channel":
			opts.channel, err = value("stable", "staging")
		case "--switch":
			opts.switchNow, err = value("0", "1")
		case "--debug":
			opts.debug = true
		case "--no-cleanup":
			opts.cleanup = false
		case "--oddc-commit":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--oddc-commit requires a commit")
			}
			i++
			opts.rebuildArgs = []string{"--override-input", "oddc", "github:JadeOpenServices/oddc/" + args[i]}
		default:
			return opts, fmt.Errorf("unknown system rebuild option %q", args[i])
		}
		if err != nil {
			return opts, err
		}
	}
	return opts, nil
}

// checkSystemSource stops the root side unless the checkout is a commit of
// the official origin with no tracked change but a checked oddc pin.
func checkSystemSource(repo string, stdout, stderr io.Writer) int {
	ctx := context.Background()
	source, err := systemcheckout.Inspect(ctx, repo)
	if err != nil {
		fmt.Fprintf(stderr, "[GjallarOS] Error: %s: %v; nothing rebuilt\n", repo, err)
		return 1
	}
	if source.ODDCMoved {
		if err := systemcheckout.OfficialODDC(ctx, oddcBranchCache(), source.ODDC); err != nil {
			fmt.Fprintf(stderr, "[GjallarOS] Error: flake.lock: %v; nothing rebuilt\n", err)
			return 1
		}
	}
	pin := source.ODDC[:12]
	if source.ODDCMoved {
		pin += " (moved from the checkout's pin, on an official ODDC branch)"
	}
	fmt.Fprintf(stdout, "[GjallarOS] Building %s (origin/%s), oddc %s\n",
		source.Revision[:12], strings.Join(source.Branches, ", origin/"), pin)
	return 0
}

// oddcBranchCache holds the commits of the official ODDC branches between
// rebuilds.
func oddcBranchCache() string {
	if os.Geteuid() == 0 {
		return "/var/cache/gjallar/oddc-official.git"
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "gjallar", "oddc-official.git")
}

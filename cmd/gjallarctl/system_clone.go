package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

// systemCloneOrigin is what system clone clones; tests point it at a local
// repository.
var systemCloneOrigin = systemcheckout.Origins[0]

// systemCloneDest is the system checkout; tests point it elsewhere.
var systemCloneDest = systemcheckout.Path

// systemMove shows what moving the system to the root-owned checkout
// leaves behind (ROADMAP section 14, checkpoint 4) and the root step. It
// runs as the user: git runs in the user's checkout only as its owner.
func systemMove(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl system move")
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "ERROR: gjallarctl system move runs as the owner of the checkout, not as root")
		return 2
	}
	pointer, err := os.ReadFile(installercheck.SystemRepositoryFile)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	dir := filepath.Clean(strings.TrimSpace(string(pointer)))
	if dir == systemcheckout.Path {
		fmt.Fprintf(stdout, "This system already builds %s\n", systemcheckout.Path)
		return 0
	}
	if _, err := installercheck.ResolveRepository(dir); err != nil {
		fmt.Fprintf(stderr, "ERROR: the system checkout %s: %v\n", dir, err)
		return 1
	}
	built, err := readBuildSource()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if _, err := systemcheckout.Commit(built.Revision); err != nil {
		fmt.Fprintf(stderr, "ERROR: the running system: %v; then run gjallarctl system move again\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "The system builds %s, at %s.\n", dir, shortRevision(built.Revision))
	fmt.Fprintf(stdout, "%s gets a fresh clone of the official repository at that commit,\n", systemcheckout.Path)
	fmt.Fprintln(stdout, "with generated/ and user.config.json copied from your checkout.")
	commits, err := userGit(dir, "log", "--oneline", "HEAD", "--not", "--remotes=origin")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	changes, err := userGit(dir, "status", "--porcelain=v1")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if commits != "" || changes != "" {
		fmt.Fprintln(stdout, "\nNot carried over (your checkout keeps them):")
		for _, line := range strings.Split(commits, "\n") {
			if line != "" {
				fmt.Fprintf(stdout, "  commit %s\n", line)
			}
		}
		for _, line := range strings.Split(changes, "\n") {
			if line != "" {
				fmt.Fprintf(stdout, "  change %s\n", line)
			}
		}
		folder, err := localFolder()
		if err == nil {
			fmt.Fprintf(stdout, "Local .nix changes belong in %s with developer mode on;\n", folder)
			fmt.Fprintln(stdout, "gjallarctl system apply builds them.")
		}
	}
	fmt.Fprintf(stdout, "\nNext, as root:\n  sudo gjallarctl system clone --from %s\n", dir)
	return 0
}

// userGit runs git in the user's checkout as the user.
func userGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w", args[0], dir, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// systemClone makes the root-owned system checkout for a system that was
// built from a user's checkout (ROADMAP section 14, checkpoint 4). It
// clones the official repository at the running commit and takes generated/
// and user.config.json from --from as data. It never runs git in --from:
// the repository config there is the user's and could run commands.
func systemClone(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "--from" || args[1] == "" {
		fmt.Fprintln(stderr, "Usage: gjallarctl system clone --from DIR")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "ERROR: gjallarctl system clone runs as root; gjallarctl system move shows the step")
		return 2
	}
	from, err := installercheck.ResolveRepository(args[1])
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	built, err := readBuildSource()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	ctx := context.Background()
	copied, err := cloneFrom(ctx, from, systemCloneDest, systemCloneOrigin, built.Revision)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v; nothing changed\n", err)
		return 1
	}
	// The checkout must pass what every system rebuild checks.
	if err := systemcheckout.CheckOwnership(systemCloneDest); err != nil {
		return cloneFailed(systemCloneDest, err, stderr)
	}
	if _, err := systemcheckout.Inspect(ctx, systemCloneDest); err != nil {
		return cloneFailed(systemCloneDest, err, stderr)
	}

	fmt.Fprintf(stdout, "%s cloned at %s, owned by root. Copied from %s:\n", systemCloneDest, shortRevision(built.Revision), from)
	for _, rel := range copied {
		fmt.Fprintf(stdout, "  %s\n", rel)
	}
	if built.ODDC != "" && built.ODDC != built.ODDCPinned {
		fmt.Fprintf(stdout, "The running system uses oddc %s given with --override-input; the checkout pins %s.\n",
			shortRevision(built.ODDC), shortRevision(built.ODDCPinned))
		fmt.Fprintf(stdout, "Pass rebuild --override-input oddc github:JadeOpenServices/oddc/%s to keep it.\n", built.ODDC)
	}
	// /etc/gjallar/repository names from until this rebuild, so a plain
	// rebuild would still build from.
	fmt.Fprintf(stdout, "Next, as root: sudo gjallar-system-rebuild\n")
	fmt.Fprintf(stdout, "After it, rebuild builds %s; %s stays as it is.\n", systemCloneDest, from)
	return 0
}

// systemCheckoutMissing names the way back when the system checkout is
// missing or empty, so a rebuild does not stop at root's ownership check
// with no hint.
func systemCheckoutMissing() error {
	if _, err := os.Lstat(filepath.Join(systemCloneDest, ".git")); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf(
		"the checkout at %s is empty or missing; with a copy of this machine's generated/ and "+
			"user.config.json, move it away and run: sudo gjallarctl system clone --from DIR",
		systemCloneDest,
	)
}

func cloneFailed(dest string, err error, stderr io.Writer) int {
	_ = os.RemoveAll(dest)
	fmt.Fprintf(stderr, "ERROR: %v; %s removed again, nothing changed\n", err, dest)
	return 1
}

func readBuildSource() (flakesource.BuildSource, error) {
	var built flakesource.BuildSource
	data, err := os.ReadFile(buildSourcePath)
	if errors.Is(err, fs.ErrNotExist) {
		return built, fmt.Errorf("%s is missing; the running system has no record of its commit", buildSourcePath)
	}
	if err != nil {
		return built, err
	}
	if err := json.Unmarshal(data, &built); err != nil {
		return built, fmt.Errorf("%s: %w", buildSourcePath, err)
	}
	return built, nil
}

// cloneFrom clones origin at revision into dest and copies the machine-local
// entries of from into it, owned by whoever runs it. dest must not exist.
// It returns the copied paths; on an error dest is gone again.
func cloneFrom(ctx context.Context, from, dest, origin, revision string) ([]string, error) {
	if _, err := os.Lstat(dest); err == nil {
		return nil, fmt.Errorf("%s exists already; move it away first", dest)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// Parse the configuration before anything is cloned.
	source, err := os.OpenRoot(from)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	userConfig, err := readRegular(source, "user.config.json")
	if err != nil {
		return nil, err
	}
	userConfig, _, err = config.ChangeText(userConfig, map[string]json.RawMessage{
		"dotfilesDir": json.RawMessage(fmt.Sprintf("%q", systemcheckout.Path)),
	})
	if err != nil {
		return nil, fmt.Errorf("%s/user.config.json: %w", from, err)
	}

	if _, err := systemcheckout.Clone(ctx, runDirect, origin, dest, revision); err != nil {
		return nil, err
	}
	copied, err := copyGenerated(source, dest)
	if err == nil {
		err = os.WriteFile(filepath.Join(dest, "user.config.json"), userConfig, 0o644)
		copied = append(copied, "user.config.json")
	}
	if err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	return copied, nil
}

// copyGenerated copies generated/ of source into dest: directories and
// regular files only, as 0755 and 0644.
func copyGenerated(source *os.Root, dest string) ([]string, error) {
	var copied []string
	err := fs.WalkDir(source.FS(), "generated", func(rel string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && rel == "generated" {
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch {
		case entry.IsDir():
			return os.Mkdir(target, 0o755)
		case entry.Type().IsRegular():
			data, err := readRegular(source, rel)
			if err != nil {
				return err
			}
			copied = append(copied, rel)
			return os.WriteFile(target, data, 0o644)
		default:
			return fmt.Errorf("%s is not a regular file or directory", rel)
		}
	})
	return copied, err
}

// readRegular reads a regular file of root without following a symlink.
func readRegular(root *os.Root, rel string) ([]byte, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	return root.ReadFile(rel)
}

// runDirect is a systemcheckout.Runner for a caller that is root already.
func runDirect(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return out, fmt.Errorf("%w: %s", err, message)
		}
	}
	return out, err
}

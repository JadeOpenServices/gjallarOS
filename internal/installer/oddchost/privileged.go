package oddchost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/oddc/pkg/oddc"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/rootfile"
)

// PrivilegedReader retrieves protected machine-local state. exists=false is
// reserved strictly for a path that does not exist.
type PrivilegedReader func(
	context.Context,
	string,
) (data []byte, exists bool, err error)

type PrivilegedCommand func(
	context.Context,
	string,
	...string,
) ([]byte, error)

func LoadPrivileged(
	ctx context.Context,
	path string,
	modelID string,
) (overlay oddc.Overlay, exists bool, err error) {
	return loadPrivileged(
		ctx,
		path,
		modelID,
		defaultPrivilegedReader,
	)
}

func loadPrivileged(
	ctx context.Context,
	path string,
	modelID string,
	read PrivilegedReader,
) (overlay oddc.Overlay, exists bool, err error) {
	data, exists, err := read(ctx, path)
	if err != nil {
		return oddc.Overlay{}, false, fmt.Errorf(
			"read protected ODDC host overlay: %w",
			err,
		)
	}
	if !exists {
		return oddc.Overlay{}, false, nil
	}

	overlay, err = oddc.DecodeOverlay(bytes.NewReader(data))
	if err != nil {
		return oddc.Overlay{}, false, err
	}

	if err := validateLoadedOverlay(overlay, modelID); err != nil {
		return oddc.Overlay{}, false, err
	}

	return overlay, true, nil
}

// SavePrivileged atomically commits complete machine-local ODDC host state
// into the protected installed-system state tree.
func SavePrivileged(
	ctx context.Context,
	path string,
	overlay oddc.Overlay,
) error {
	return savePrivileged(
		ctx,
		path,
		overlay,
		defaultPrivilegedCommand,
	)
}

func savePrivileged(
	ctx context.Context,
	path string,
	overlay oddc.Overlay,
	run PrivilegedCommand,
) error {
	if strings.TrimSpace(path) == "" ||
		!filepath.IsAbs(path) {
		return fmt.Errorf(
			"ODDC host overlay path must be absolute: %q",
			path,
		)
	}

	if err := validateLoadedOverlay(
		overlay,
		overlay.TargetModel,
	); err != nil {
		return err
	}

	data, err := json.MarshalIndent(overlay, "", "  ")
	if err != nil {
		return fmt.Errorf(
			"encode ODDC host overlay: %w",
			err,
		)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	protectedTmp := filepath.Join(
		dir,
		"."+filepath.Base(path)+".tmp",
	)

	if _, err := run(
		ctx,
		"sudo",
		"install",
		"-d",
		"-m",
		"0700",
		dir,
	); err != nil {
		return fmt.Errorf(
			"create protected ODDC host state directory: %w",
			err,
		)
	}

	// The overlay goes to root through a pipe, never through a file (L54).
	if err := writeRootFile(ctx, data, "0600", protectedTmp); err != nil {
		return fmt.Errorf(
			"install temporary ODDC host overlay: %w",
			err,
		)
	}

	if _, err := run(
		ctx,
		"sudo",
		"mv",
		"-f",
		"--",
		protectedTmp,
		path,
	); err != nil {
		_, _ = run(
			ctx,
			"sudo",
			"rm",
			"-f",
			"--",
			protectedTmp,
		)

		return fmt.Errorf(
			"replace protected ODDC host overlay: %w",
			err,
		)
	}

	return nil
}

// writeRootFile writes a root-only file from memory. Tests replace it.
var writeRootFile = rootfile.Write

// protectedExistsArgs builds the existence probe for a protected path.
//
// test(1) has no "--" end-of-options marker: "test -e -- PATH" is a syntax
// error (exit 2), not a lookup. Requiring an absolute path guarantees PATH
// cannot be parsed as an operator instead.
func protectedExistsArgs(path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("protected state path must be absolute: %q", path)
	}
	return []string{"test", "-e", path}, nil
}

func defaultPrivilegedReader(
	ctx context.Context,
	path string,
) ([]byte, bool, error) {
	existsArgs, err := protectedExistsArgs(path)
	if err != nil {
		return nil, false, err
	}
	check := exec.CommandContext(ctx, "sudo", existsArgs...)

	if err := check.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok &&
			exit.ExitCode() == 1 {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf(
			"inspect protected state %s: %w",
			path,
			err,
		)
	}

	data, err := exec.CommandContext(
		ctx,
		"sudo",
		"cat",
		"--",
		path,
	).Output()
	if err != nil {
		return nil, false, fmt.Errorf(
			"read protected state %s: %w",
			path,
			err,
		)
	}

	return data, true, nil
}

func defaultPrivilegedCommand(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	command := exec.CommandContext(
		ctx,
		name,
		args...,
	)

	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf(
			"%s failed: %w: %s",
			name,
			err,
			strings.TrimSpace(string(output)),
		)
	}

	return output, nil
}

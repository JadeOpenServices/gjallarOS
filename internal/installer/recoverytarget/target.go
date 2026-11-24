package recoverytarget

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
)

const maxSymlinks = 32

type runner interface {
	Output(context.Context, string, ...string) ([]byte, error)
	Run(context.Context, string, ...string) error
}

type execRunner struct{}

func (execRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (execRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func PrepareBoot(ctx context.Context, root string) error {
	return prepareBoot(ctx, root, execRunner{})
}

func prepareBoot(ctx context.Context, root string, r runner) error {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == "/" {
		return fmt.Errorf(
			"recovery target root must be an absolute non-root path: %q",
			root,
		)
	}

	targetRoot, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open recovery target root: %w", err)
	}
	defer targetRoot.Close()

	fstab, err := readTargetFile(targetRoot, "etc/fstab")
	if err != nil {
		return fmt.Errorf("read installed fstab: %w", err)
	}

	source, err := bootSource(fstab)
	if err != nil {
		return err
	}

	source, err = resolveDeviceSource(source)
	if err != nil {
		return err
	}

	if err := r.Run(ctx, "test", "-b", source); err != nil {
		return fmt.Errorf(
			"installed /boot source is not a block device %s: %w",
			source,
			err,
		)
	}

	if err := targetRoot.MkdirAll("boot", 0755); err != nil {
		return fmt.Errorf(
			"create installed /boot mountpoint: %w",
			err,
		)
	}

	target := filepath.Join(root, "boot")

	mounted, options, err := mountedSource(ctx, r, target)
	if err != nil {
		return err
	}

	if mounted == "" {
		if err := r.Run(ctx, "mount", "-o", diskplan.PrivateFATMasks, source, target); err != nil {
			return fmt.Errorf(
				"mount installed /boot at %s: %w",
				target,
				err,
			)
		}

		mounted, options, err = mountedSource(ctx, r, target)
		if err != nil {
			return err
		}
		if missing := diskplan.MissingFATMask(options); missing != "" {
			return fmt.Errorf(
				"installed /boot at %s is mounted without %s, so other users could read it",
				target,
				missing,
			)
		}
	}

	expected, err := canonicalDeviceSource(ctx, r, source)
	if err != nil {
		return err
	}

	actual, err := canonicalDeviceSource(ctx, r, mounted)
	if err != nil {
		return err
	}

	if !strings.Contains(","+options+",", ",rw,") {
		return fmt.Errorf(
			"installed /boot at %s is not mounted read-write",
			target,
		)
	}

	if actual != expected {
		return fmt.Errorf(
			"installed /boot at %s uses %s; expected %s",
			target,
			mounted,
			source,
		)
	}

	return nil
}

func readTargetFile(root *os.Root, name string) ([]byte, error) {
	current := filepath.Clean(name)
	if filepath.IsAbs(current) {
		return nil, fmt.Errorf(
			"target-relative path required: %q",
			name,
		)
	}

	for range maxSymlinks {
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}

		if info.Mode()&os.ModeSymlink == 0 {
			return root.ReadFile(current)
		}

		link, err := root.Readlink(current)
		if err != nil {
			return nil, err
		}

		if filepath.IsAbs(link) {
			current = strings.TrimPrefix(filepath.Clean(link), "/")
			continue
		}

		current = filepath.Clean(
			filepath.Join(filepath.Dir(current), link),
		)

		if current == ".." ||
			strings.HasPrefix(
				current,
				".."+string(filepath.Separator),
			) {
			return nil, fmt.Errorf(
				"target symlink escapes recovery root: %q",
				link,
			)
		}
	}

	return nil, fmt.Errorf(
		"too many symlinks resolving %q",
		name,
	)
}

func bootSource(fstab []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(fstab))
	source := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "/boot" {
			continue
		}

		if source != "" {
			return "", fmt.Errorf(
				"installed fstab contains multiple /boot entries",
			)
		}

		source = fields[0]
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("parse installed fstab: %w", err)
	}
	if source == "" {
		return "", fmt.Errorf(
			"installed fstab does not define /boot",
		)
	}

	return source, nil
}

func resolveDeviceSource(source string) (string, error) {
	switch {
	case strings.HasPrefix(source, "/dev/"):
		return filepath.Clean(source), nil

	case strings.HasPrefix(source, "UUID="):
		return deviceTagPath(
			"/dev/disk/by-uuid",
			strings.TrimPrefix(source, "UUID="),
			source,
		)

	case strings.HasPrefix(source, "PARTUUID="):
		return deviceTagPath(
			"/dev/disk/by-partuuid",
			strings.TrimPrefix(source, "PARTUUID="),
			source,
		)

	case strings.HasPrefix(source, "LABEL="):
		return deviceTagPath(
			"/dev/disk/by-label",
			strings.TrimPrefix(source, "LABEL="),
			source,
		)

	case strings.HasPrefix(source, "PARTLABEL="):
		return deviceTagPath(
			"/dev/disk/by-partlabel",
			strings.TrimPrefix(source, "PARTLABEL="),
			source,
		)

	default:
		return "", fmt.Errorf(
			"unsupported installed /boot source %q",
			source,
		)
	}
}

func deviceTagPath(directory, value, source string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" ||
		value == "." ||
		value == ".." ||
		strings.ContainsRune(value, filepath.Separator) {
		return "", fmt.Errorf(
			"invalid installed /boot source %q",
			source,
		)
	}

	return filepath.Join(directory, value), nil
}

func canonicalDeviceSource(
	ctx context.Context,
	r runner,
	source string,
) (string, error) {
	out, err := r.Output(
		ctx,
		"readlink",
		"-f",
		"--",
		source,
	)
	if err != nil {
		return "", fmt.Errorf(
			"resolve device source %s: %w",
			source,
			err,
		)
	}

	resolved := strings.TrimSpace(string(out))
	if resolved == "" ||
		strings.Contains(resolved, "\n") ||
		!strings.HasPrefix(resolved, "/dev/") {
		return "", fmt.Errorf(
			"unexpected resolved device source %q",
			resolved,
		)
	}

	return filepath.Clean(resolved), nil
}

func mountedSource(
	ctx context.Context,
	r runner,
	target string,
) (string, string, error) {
	out, err := r.Output(
		ctx,
		"findmnt",
		"-nvro", "SOURCE,OPTIONS",
		"--mountpoint", target,
	)
	if err != nil {
		type exitCoder interface {
			ExitCode() int
		}

		if exitErr, ok := err.(exitCoder); ok &&
			exitErr.ExitCode() == 1 {
			return "", "", nil
		}

		return "", "", fmt.Errorf(
			"inspect installed /boot mount at %s: %w",
			target,
			err,
		)
	}

	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return "", "", fmt.Errorf(
			"unexpected installed /boot mount data %q",
			strings.TrimSpace(string(out)),
		)
	}

	return fields[0], fields[1], nil
}

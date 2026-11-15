package app

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// The installer runs as the invoking user (the "nixos" user on live media)
// and reaches root-owned target paths such as /mnt/var only through sudo.
// Tests replace privilegedCommand to record the commands.
var privilegedCommand = func(ctx context.Context, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
	if err != nil {
		return output, fmt.Errorf(
			"sudo %s: %w: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return output, nil
}

// stagedTreeCheck runs as root on root's own copy of a tree the user
// staged, which the user can no longer change. cp -r keeps links, pipes
// and a source that was swapped for a link, and a later root reader would
// follow them (L55), so the copy must be a real folder holding only plain
// files and folders.
const stagedTreeCheck = `[ -d "$1" ] && [ ! -L "$1" ] || { echo "$1 is not a plain folder" >&2; exit 1; }
odd=$(find "$1" -mindepth 1 ! -type f ! -type d -print -quit) || exit 1
[ -z "$odd" ] || { echo "refusing $odd: only plain files and folders" >&2; exit 1; }`

// installTreePrivileged copies the user-staged directory source to the
// root-owned destination. The previous destination is replaced only after
// the complete copy exists beside it.
func installTreePrivileged(ctx context.Context, source, destination string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) {
		return fmt.Errorf(
			"privileged tree install needs absolute paths: %q -> %q",
			source,
			destination,
		)
	}
	destination = filepath.Clean(destination)
	parent := filepath.Dir(destination)
	incoming := filepath.Join(parent, "."+filepath.Base(destination)+".incoming")
	previous := destination + ".previous"

	steps := [][]string{
		{"mkdir", "-p", "-m", "0700", "--", parent},
		{"rm", "-rf", "--", incoming, previous},
		{"cp", "-r", "--", source, incoming},
		{"sh", "-c", stagedTreeCheck, "sh", incoming},
		{"chown", "-R", "root:root", "--", incoming},
		{"sh", "-c", `if [ -e "$1" ]; then mv -T -- "$1" "$2"; fi`, "sh", destination, previous},
		{"mv", "-T", "--", incoming, destination},
		{"rm", "-rf", "--", previous},
	}
	for _, step := range steps {
		if _, err := privilegedCommand(ctx, step...); err != nil {
			_, _ = privilegedCommand(ctx, "rm", "-rf", "--", incoming)
			return fmt.Errorf("install %s: %w", destination, err)
		}
	}
	return nil
}

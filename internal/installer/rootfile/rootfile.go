// Package rootfile writes a file as root from data the installer holds in
// memory. Root never opens a file the user's session made: the session
// could swap it for a link to a root-only file, a device that never ends
// or a pipe that never opens (L50, L54).
package rootfile

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Args is the root step. install reads the data from its stdin and
// replaces whatever is at dst, a link or a pipe included, instead of
// writing through it. The folder above dst must belong to root.
func Args(mode, dst string) []string {
	return []string{"install", "-m", mode, "-o", "root", "-g", "root", "-T", "--", "/dev/stdin", dst}
}

// Write writes data to dst as root with mode. sudo asks for a password on
// the terminal, never on stdin. A process that already runs as root, such
// as a boot unit without sudo on PATH, runs install itself.
func Write(ctx context.Context, data []byte, mode, dst string) error {
	cmd := command(ctx, mode, dst)
	cmd.Stdin = bytes.NewReader(data)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func command(ctx context.Context, mode, dst string) *exec.Cmd {
	args := Args(mode, dst)
	if geteuid() == 0 {
		return exec.CommandContext(ctx, args[0], args[1:]...)
	}
	return exec.CommandContext(ctx, "sudo", args...)
}

var geteuid = os.Geteuid

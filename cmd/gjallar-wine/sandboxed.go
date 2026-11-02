package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Some steps read a file that comes from an app or a download: its icon
// (icon.go) or its headers (scan.go). Each runs as this binary in a
// sandbox of its own and hands back a small answer the caller checks.

// stepNone is a sandboxed step's exit code for a file without what it
// looks for.
const stepNone = 3

var errStepNone = errors.New("not in the file")

// sandboxed runs "gjallar-wine STEP ARGS" in bubblewrap with src as its
// fd 3. The sandbox holds only this binary: no files, no devices, no
// network, its own user, process and IPC namespaces, no capabilities, an
// empty environment, and it ends with this process. It must answer within
// timeout and send no more than outMax bytes.
func sandboxed(timeout time.Duration, outMax int, src *os.File, step ...string) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(self, "/nix/store/") {
		return nil, fmt.Errorf("%s is not in the Nix store; start the installed gjallar-wine", self)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bwrapPath, append([]string{
		"--unshare-all", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--clearenv",
		"--ro-bind", self, self,
		"--", self}, step...)...)
	cmd.ExtraFiles = []*os.File{src}
	out := &capped{max: outMax}
	errOut := &capped{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, errOut
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == stepNone:
		return nil, errStepNone
	case err != nil:
		return nil, fmt.Errorf("%s reader: %w: %s", step[0], err, strings.TrimSpace(errOut.buf.String()))
	}
	return out.buf.Bytes(), nil
}

// capped keeps up to max bytes and fails once more come.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		return 0, errors.New("the sandboxed reader sent too much")
	}
	return c.buf.Write(p)
}

// limitStep sets a sandboxed step's own limits: mem bytes of memory and,
// if cpu is not 0, that much processor time. RLIMIT_DATA counts the
// memory the Go runtime maps for use; it leaves out the address space the
// runtime only reserves, which RLIMIT_AS counts, so that limit stopped the
// runtime growing its heap at all.
func limitStep(mem uint64, cpu time.Duration) error {
	if err := unix.Setrlimit(unix.RLIMIT_DATA, &unix.Rlimit{Cur: mem, Max: mem}); err != nil {
		return err
	}
	if cpu > 0 {
		s := uint64(cpu / time.Second)
		if err := unix.Setrlimit(unix.RLIMIT_CPU, &unix.Rlimit{Cur: s, Max: s}); err != nil {
			return err
		}
	}
	debug.SetMemoryLimit(int64(mem / 2))
	return nil
}

// Package move is the stick side of the disk move (345): the screens of
// steps 2 to 5. Root work happens in gjallar-move-helper; this package only
// asks it for fixed operations and shows what comes back.
package move

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

// Helper runs one operation of gjallar-move-helper. A refused step comes
// back as an *oldroot.Refusal error (see oldroot.AsRefusal); any other error is
// a failure of the helper.
type Helper interface {
	Run(ctx context.Context, req movehelper.Request, result any) error
}

// maxResult caps the helper's answer, maxStderr what is kept of its errors.
const (
	maxResult = 64 << 10
	maxStderr = 4096
)

// ExecHelper runs the helper as its own process. Argv is the fixed command,
// starting with an absolute path: a bare name would be looked up through
// PATH (L114). The request goes through stdin, never the command line,
// because it can carry the old disk's key.
type ExecHelper struct {
	Argv []string
}

// HelperPath is gjallar-move-helper in the installer's own store path, set
// at build time (pkgs/gjallar-installer). The move starts the helper only by
// this fixed path, never through PATH and never through sudo (Q253): the
// stick menu already runs as root.
var HelperPath string

// StoreHelper is the helper at HelperPath. It fails on a build that did not
// set the path, or set one outside the Nix store.
func StoreHelper() (ExecHelper, error) {
	if !strings.HasPrefix(HelperPath, "/nix/store/") || filepath.Clean(HelperPath) != HelperPath {
		return ExecHelper{}, fmt.Errorf("move helper path %q is not a store path: this installer was built without it", HelperPath)
	}
	return ExecHelper{Argv: []string{HelperPath}}, nil
}

// helperEnv is the whole environment the helper starts with. Nothing comes
// from the caller, so no variable set in the session reaches root's
// process (L114).
var helperEnv = []string{"PATH=/run/current-system/sw/bin", "LC_ALL=C"}

func (h ExecHelper) Run(ctx context.Context, req movehelper.Request, result any) error {
	if len(h.Argv) == 0 {
		return errors.New("no move helper set")
	}
	if !filepath.IsAbs(h.Argv[0]) {
		return fmt.Errorf("move helper %q is not an absolute path", h.Argv[0])
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	defer clear(data)
	cmd := exec.CommandContext(ctx, h.Argv[0], h.Argv[1:]...)
	cmd.Env = helperEnv
	stdout := &capped{max: maxResult}
	stderr := &capped{max: maxStderr}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(data), stdout, stderr
	if err := cmd.Run(); err != nil {
		if text := helperError(stderr.buf.String()); text != "" {
			return fmt.Errorf("move helper %s: %s", req.Op, text)
		}
		return fmt.Errorf("move helper %s: %w", req.Op, err)
	}
	if stdout.over {
		return fmt.Errorf("move helper %s: answer is over %d bytes", req.Op, maxResult)
	}
	return decode(stdout.buf.Bytes(), result)
}

// decode reads the helper's answer: a refusal, or the result of the
// operation.
func decode(raw []byte, result any) error {
	var refused movehelper.RefusalResult
	if err := json.Unmarshal(raw, &refused); err != nil {
		return fmt.Errorf("read the move helper's answer: %w", err)
	}
	if refused.Refusal != nil {
		return refused.Refusal
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("read the move helper's answer: %w", err)
	}
	return nil
}

// helperError is the last line the helper wrote to stderr, as plain text
// of at most 200 characters: it can quote text read from the old disk, so
// terminal codes and other control characters are dropped.
func helperError(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	var b strings.Builder
	for _, r := range installlog.Clean(lines[len(lines)-1]) {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	text := []rune(strings.TrimSpace(b.String()))
	if len(text) > 200 {
		text = append(text[:200], '…')
	}
	return string(text)
}

// capped keeps the first max bytes written to it and notes when more came.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.over = true
	}
	if room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

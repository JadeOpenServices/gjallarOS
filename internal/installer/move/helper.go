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

// ExecHelper runs the helper as its own process. Argv is the fixed command
// (the sudo rule and its PATH are set where the stick wires it). The
// request goes through stdin, never the command line, because it can carry
// the old disk's key.
type ExecHelper struct {
	Argv []string
}

func (h ExecHelper) Run(ctx context.Context, req movehelper.Request, result any) error {
	if len(h.Argv) == 0 {
		return errors.New("no move helper set")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	defer clear(data)
	cmd := exec.CommandContext(ctx, h.Argv[0], h.Argv[1:]...)
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

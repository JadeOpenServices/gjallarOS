package termout

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prog")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func openTerm(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, slave, err := OpenPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	return master, slave
}

// A process the command leaves behind keeps the pseudo-terminal open;
// RunInPTY returns after the drain time anyway.
func TestRunInPTYDoesNotWaitForLeftovers(t *testing.T) {
	prog := script(t, "sleep 3 &\necho started\n")
	previous := drainTime
	drainTime = 200 * time.Millisecond
	t.Cleanup(func() { drainTime = previous })
	master, term := openTerm(t)
	go io.Copy(io.Discard, master)
	var out bytes.Buffer
	start := time.Now()
	if err := RunInPTY(exec.Command(prog), term, &out); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("RunInPTY waited %v for a leftover process", took)
	}
	if !strings.Contains(out.String(), "started") {
		t.Errorf("output lacks the line: %q", out.String())
	}
}

func TestPTYIsOwnerOnly(t *testing.T) {
	_, slave := openTerm(t)
	info, err := slave.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("pseudo-terminal mode %o, want 600", mode)
	}
}

// column stands for the stick's page column: it marks each write, so the
// test sees that the output went through it, and says how wide it is.
type column struct{ term *os.File }

func (c column) Write(b []byte) (int, error) {
	if _, err := c.term.Write(append([]byte("|"), b...)); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (c column) Terminal() io.Writer { return c.term }
func (c column) PageWidth() int      { return 66 }

// A program started with Attach in a page column sees a terminal of the
// column's width, and its output goes through the column.
func TestAttachInPageColumn(t *testing.T) {
	prog := script(t, `if [ -t 1 ] && [ -t 2 ]; then echo "tty: yes"; else echo "tty: no"; fi
echo "cols: $(stty size <&2)" >&2
`)
	master, term := openTerm(t)
	if err := unix.IoctlSetWinsize(int(term.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
		t.Fatal(err)
	}
	shown := make(chan string)
	go func() {
		data, _ := io.ReadAll(master)
		shown <- string(data)
	}()
	SetOutput(column{term}, column{term})
	t.Cleanup(func() { SetOutput(nil, nil) })
	if err := Attach(exec.Command(prog)); err != nil {
		t.Fatal(err)
	}
	term.Close()
	screen := <-shown
	for _, want := range []string{"tty: yes", "cols: 40 66", "|"} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen lacks %q: %q", want, screen)
		}
	}
}

// Without SetOutput, Attach writes to the process's own stdout and stderr.
func TestOutputDefaultsToProcess(t *testing.T) {
	SetOutput(nil, nil)
	stdout, stderr := Output()
	if stdout != os.Stdout || stderr != os.Stderr {
		t.Errorf("Output() = %v, %v", stdout, stderr)
	}
}

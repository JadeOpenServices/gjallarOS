package installlog

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/termout"
)

// standIn puts a script named nixos-rebuild first on PATH. It says whether
// its stderr is a terminal, draws a bar the way Nix does and echoes the
// line it reads from stdin.
func standIn(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
if [ -t 2 ]; then echo "stderr: terminal" >&2; else echo "stderr: pipe" >&2; fi
printf '\033[1m[1/3 built]\r\033[K[3/3 built]\033[0m\n' >&2
read -r line
echo "read: $line"
`
	if err := os.WriteFile(filepath.Join(bin, "nixos-rebuild"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func openTestLog(t *testing.T) *Log {
	t.Helper()
	log, err := Open(filepath.Join(t.TempDir(), "log"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	SetCurrent(log)
	t.Cleanup(func() {
		SetCurrent(nil)
		log.Close()
	})
	return log
}

func exported(t *testing.T, log *Log) string {
	t.Helper()
	data, err := log.Export()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunRefusesOtherCommands(t *testing.T) {
	for _, argv := range [][]string{{"sudo", "chpasswd"}, {"age-keygen"}, {"sudo"}, {"sh", "-c", "nixos-rebuild"}} {
		err := Run(context.Background(), nil, io.Discard, io.Discard, argv[0], argv[1:]...)
		if err == nil || !strings.Contains(err.Error(), "not a logged build command") {
			t.Errorf("%v: got %v, want a refusal", argv, err)
		}
	}
}

func TestRunWithoutLogIsPlain(t *testing.T) {
	standIn(t)
	SetCurrent(nil)
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), strings.NewReader("typed\n"), &out, &errOut, "nixos-rebuild"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "read: typed\n" || !strings.HasPrefix(errOut.String(), "stderr: pipe\n") {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}
}

func TestRunLogsPipeOutput(t *testing.T) {
	standIn(t)
	log := openTestLog(t)
	var term bytes.Buffer
	tee := log.Tee(&term)
	if err := Run(context.Background(), strings.NewReader("typed\n"), tee, tee, "nixos-rebuild"); err != nil {
		t.Fatal(err)
	}
	text := exported(t, log)
	for _, want := range []string{"stderr: pipe", " [3/3 built]\n", "read: \n"} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	checkClean(t, text)
	if !strings.Contains(term.String(), "\x1b[1m") {
		t.Errorf("terminal did not get the output as it is: %q", term.String())
	}
}

func TestRunGivesNixATerminal(t *testing.T) {
	standIn(t)
	log := openTestLog(t)
	// A pseudo-terminal stands in for the user's terminal.
	master, slave, err := termout.OpenPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	shown := make(chan string)
	go func() {
		data, _ := io.ReadAll(master)
		shown <- string(data)
	}()
	tee := log.Tee(slave)
	if err := Run(context.Background(), strings.NewReader("typed\n"), tee, tee, "nixos-rebuild"); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	screen := <-shown
	text := exported(t, log)
	for _, want := range []string{"stderr: terminal", " [3/3 built]\n", "read: \n"} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	checkClean(t, text)
	if !strings.Contains(screen, "\x1b[1m[1/3 built]\r") {
		t.Errorf("terminal did not get the bar as it is: %q", screen)
	}
}

// checkClean fails when typed input, escape codes or bar redraws reached
// the log.
func checkClean(t *testing.T, text string) {
	t.Helper()
	for _, bad := range []string{"typed", "\x1b", "\r", "[1/3 built]"} {
		if strings.Contains(text, bad) {
			t.Errorf("log holds %q:\n%q", bad, text)
		}
	}
}

// TestRunResetsColourAfterCommand: a command that ends in the middle of a
// colour does not leave it on for the text after it; the log stays clean.
func TestRunResetsColourAfterCommand(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '\\033[31mred'\n"
	if err := os.WriteFile(filepath.Join(bin, "nix-channel"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := openTestLog(t)
	master, slave, err := termout.OpenPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	defer master.Close()
	var screen strings.Builder
	done := make(chan struct{})
	go func() { io.Copy(&screen, master); close(done) }()
	tee := log.Tee(slave)
	if err := Run(context.Background(), nil, tee, tee, "nix-channel"); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	<-done
	if got := screen.String(); !strings.HasSuffix(got, "red\x1b[0m") {
		t.Errorf("terminal got %q", got)
	}
	if text := exported(t, log); strings.Contains(text, "\x1b") || !strings.Contains(text, "red") {
		t.Errorf("log:\n%q", text)
	}
}

// paged stands for the stick's page column: it writes to the terminal and
// says how wide the column is.
type paged struct{ term *os.File }

func (p paged) Write(b []byte) (int, error) { return p.term.Write(b) }
func (p paged) Terminal() io.Writer         { return p.term }
func (p paged) PageWidth() int              { return 66 }

// A program run in a page column gets the column's width, so its output
// wraps inside the column, not at the screen edge.
func TestRunInPageColumnGetsColumnWidth(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"cols: $(stty size <&2)\" >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "nixos-rebuild"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	SetCurrent(nil)
	master, slave, err := termout.OpenPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
		t.Fatal(err)
	}
	shown := make(chan string)
	go func() {
		data, _ := io.ReadAll(master)
		shown <- string(data)
	}()
	out := paged{slave}
	if err := Run(context.Background(), nil, out, out, "nixos-rebuild"); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	if screen := <-shown; !strings.Contains(screen, "cols: 40 66") {
		t.Fatalf("program did not get the column width: %q", screen)
	}
}

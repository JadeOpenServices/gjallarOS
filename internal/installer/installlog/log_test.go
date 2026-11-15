package installlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
)

var testStart = time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)

func openTest(t *testing.T) (*Log, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gjallar-install")
	l, err := Open(dir, testStart)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, dir
}

func readLog(t *testing.T, l *Log) string {
	t.Helper()
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOpenIsOwnerOnlyAndNew(t *testing.T) {
	l, dir := openTest(t)
	for path, want := range map[string]os.FileMode{dir: 0o700, l.Path(): 0o600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", path, got, want)
		}
	}
	if _, err := Open(dir, testStart); err == nil {
		t.Error("second Open at the same second used the existing file")
	}
}

func TestOpenRefusesLinksAndOpenFolders(t *testing.T) {
	base := t.TempDir()
	planted := filepath.Join(base, "planted")
	if err := os.Mkdir(planted, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(planted, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, testStart); err == nil {
		t.Error("Open followed a link to a folder")
	}

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(open, testStart); err == nil {
		t.Error("Open used a folder others can read")
	}

	dir := filepath.Join(base, "file-link")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "victim")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, FileName(testStart))); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, testStart); err == nil {
		t.Error("Open wrote through a link planted at the log name")
	}
}

func TestTeeWritesTerminalAndFinishedLines(t *testing.T) {
	l, _ := openTest(t)
	var term bytes.Buffer
	tee := l.Tee(&term)
	if _, err := tee.Write([]byte("STAGE: one\nhalf ")); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, l); strings.Contains(got, "half") {
		t.Errorf("unfinished line logged early:\n%s", got)
	}
	tee.Write([]byte("done\n"))
	if term.String() != "STAGE: one\nhalf done\n" {
		t.Errorf("terminal got %q", term.String())
	}
	got := readLog(t, l)
	for _, want := range []string{" STAGE: one\n", " half done\n", "started 2026-11-15T09:30:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if tee.Terminal() != &term {
		t.Error("Terminal is not the writer behind the log")
	}
}

func TestCloseWritesUnfinishedLine(t *testing.T) {
	l, _ := openTest(t)
	l.Tee(&bytes.Buffer{}).Write([]byte("Hostname [example]: "))
	path := l.Path()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "Hostname [example]: ") {
		t.Errorf("unfinished line lost at Close:\n%s", raw)
	}
}

func TestClean(t *testing.T) {
	cases := map[string]string{
		"plain":                                    "plain",
		"copying 10%\rcopying 55%\rcopying 100%":   "copying 100%",
		"line with windows end\r":                  "line with windows end",
		"\x1b[1;32mPASS\x1b[0m: done":              "PASS: done",
		"\x1b]0;title\x07after title":              "after title",
		"\x1b[2K\r[3/12 built] building foo\x1b[K": "[3/12 built] building foo",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinesAreRedacted(t *testing.T) {
	l, _ := openTest(t)
	l.SetKnown(installreport.Known{Host: "examplehost"})
	tee := l.Tee(&bytes.Buffer{})
	tee.Write([]byte("fetch https://someone:hunter2@example.org/x password=hunter2 host examplehost\n"))
	got := readLog(t, l)
	for _, leak := range []string{"hunter2", "someone", "examplehost"} {
		if strings.Contains(got, leak) {
			t.Errorf("log holds %q:\n%s", leak, got)
		}
	}
}

func TestExportRedactsWithLaterKnownValues(t *testing.T) {
	l, _ := openTest(t)
	l.Tee(&bytes.Buffer{}).Write([]byte("Creating user exampleuser on examplehost\n"))
	l.SetKnown(installreport.Known{User: "exampleuser", Host: "examplehost"})
	out, err := l.Export()
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "exampleuser") || strings.Contains(text, "examplehost") {
		t.Errorf("export keeps values known later:\n%s", text)
	}
	if !strings.Contains(text, "Creating user <user> on <host>") {
		t.Errorf("export lost the line:\n%s", text)
	}
}

func TestSizeCapKeepsAnchors(t *testing.T) {
	l, _ := openTest(t)
	l.size = MaxSize - 10
	l.Tee(&bytes.Buffer{}).Write([]byte(strings.Repeat("x", 100) + "\n"))
	l.Mark(RunFinished, Count("code", 1))
	got := readLog(t, l)
	if strings.Contains(got, "xxxx") {
		t.Error("line past the cap was written")
	}
	if !strings.Contains(got, "[log cut at its size limit") || !strings.Contains(got, "GJA-1099 install.run.finished code=1") {
		t.Errorf("cap note or anchor missing:\n%s", got)
	}
}

func TestNilLogIsSafe(t *testing.T) {
	var l *Log
	l.Mark(RunStarted)
	l.SetKnown(installreport.Known{})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Export(); err == nil {
		t.Error("Export of no log gave no error")
	}
	SetCurrent(nil)
	Mark(RunStarted)
}

// TestExportReadsTheOpenFile swaps the log path for a pipe: Export must
// read the file it created instead of blocking on, or copying, the swap.
func TestExportReadsTheOpenFile(t *testing.T) {
	l, _ := openTest(t)
	l.Tee(&bytes.Buffer{}).Write([]byte("real line\n"))
	if err := os.Remove(l.Path()); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(l.Path(), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan []byte, 1)
	go func() {
		out, _ := l.Export()
		done <- out
	}()
	select {
	case out := <-done:
		if !strings.Contains(string(out), "real line") {
			t.Errorf("export lost the log:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Export blocked on a pipe planted at the log path")
	}
}

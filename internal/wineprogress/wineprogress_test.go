package wineprogress

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// pipe opens a Writer on a fresh pipe and returns a reader of its lines.
func pipe(t *testing.T) (*Writer, *os.File) {
	w, r, _ := pipeFd(t)
	return w, r
}

// pipeFd also returns the channel's fd number. Fd() on the file is not
// called: it would make the fd blocking again and end the deadlines.
func pipeFd(t *testing.T) (*Writer, *os.File, int) {
	t.Helper()
	r, wf, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(wf.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	wf.Close()
	w, err := Open(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close(); r.Close() })
	return w, r, fd
}

func lines(t *testing.T, w *Writer, r *os.File) []Event {
	t.Helper()
	w.Close()
	var out []Event
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, MaxLine+1)
	for sc.Scan() {
		if len(sc.Bytes())+1 > MaxLine {
			t.Fatalf("line of %d bytes", len(sc.Bytes())+1)
		}
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("%q: %v", sc.Text(), err)
		}
		if e.V != Version {
			t.Fatalf("line without version: %q", sc.Text())
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNilWriterWritesNothing(t *testing.T) {
	var w *Writer
	w.Start(StepBase, "x")
	w.Bytes(StepBase, 1, 2)
	w.Finish(StepBase)
	w.Choose([]string{`C:\a.exe`}, "")
	w.Fail(CodeFailed)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRefuses(t *testing.T) {
	for _, fd := range []int{0, 1, 2, -1} {
		if _, err := Open(fd); err == nil {
			t.Errorf("fd %d taken", fd)
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := Open(int(f.Fd())); err == nil {
		t.Error("plain file taken")
	}
	r, wf, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer wf.Close()
	defer r.Close()
	if _, err := Open(int(r.Fd())); err == nil {
		t.Error("read end taken")
	}
	// An fd number with nothing behind it.
	if _, err := Open(1 << 20); err == nil {
		t.Error("closed fd taken")
	}
}

func TestStepOrderAndFailure(t *testing.T) {
	w, r := pipe(t)
	w.Start(StepBase, "Example Paint")
	w.Finish(StepBase)
	w.Start(StepCopy, "")
	w.Fail(CodeCopyFailed)
	w.Fail(CodeFailed) // only the first counts
	got := lines(t, w, r)
	want := []struct {
		step  Step
		state State
	}{{StepBase, Running}, {StepBase, Done}, {StepCopy, Running}, {StepCopy, Failed}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, e := range got {
		if e.Step != want[i].step || e.State != want[i].state {
			t.Errorf("line %d = %s %s, want %s %s", i, e.Step, e.State, want[i].step, want[i].state)
		}
	}
	if got[0].App != "Example Paint" {
		t.Errorf("app = %q", got[0].App)
	}
	if got[3].Code != CodeCopyFailed || got[3].Message != messages[CodeCopyFailed] {
		t.Errorf("failure = %+v", got[3])
	}
}

func TestUnknownCodeIsGeneric(t *testing.T) {
	w, r := pipe(t)
	w.Start(StepInstaller, "")
	w.Fail(Code("made up <b>text</b>"))
	got := lines(t, w, r)
	if e := got[len(got)-1]; e.Code != CodeFailed || e.Message != messages[CodeFailed] {
		t.Errorf("failure = %+v", e)
	}
}

func TestBytesCoalesced(t *testing.T) {
	w, r := pipe(t)
	clock := time.Unix(0, 0)
	w.now = func() time.Time { return clock }
	w.Start(StepBase, "")
	for i := int64(0); i <= 100; i++ {
		w.Bytes(StepBase, i, 100)
		clock = clock.Add(10 * time.Millisecond)
	}
	w.Bytes(StepBase, 5, 2)  // done past total
	w.Bytes(StepBase, -1, 2) // negative
	got := lines(t, w, r)[1:]
	// 1 s of updates at one per 250 ms, plus the final count.
	if len(got) < 4 || len(got) > 6 {
		t.Fatalf("%d byte lines: %+v", len(got), got)
	}
	if last := got[len(got)-1]; last.Done != 100 || last.Total != 100 {
		t.Errorf("last = %+v", last)
	}
	for _, e := range got {
		if e.Done > e.Total {
			t.Errorf("bad count %+v", e)
		}
	}
}

func TestLineCapKeepsFailure(t *testing.T) {
	w, r := pipe(t)
	clock := time.Unix(0, 0)
	w.now = func() time.Time { return clock }
	// The pipe holds 64 KiB; read alongside so the writer never waits.
	done := make(chan []Event)
	go func() {
		var out []Event
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			var e Event
			json.Unmarshal(sc.Bytes(), &e)
			out = append(out, e)
		}
		done <- out
	}()
	w.Start(StepBase, "")
	for i := int64(0); i < 10*MaxLines; i++ {
		clock = clock.Add(time.Second)
		w.Bytes(StepBase, i, 10*MaxLines)
	}
	w.Fail(CodeBaseIncomplete)
	w.Close()
	got := <-done
	if len(got) > MaxLines {
		t.Fatalf("%d lines", len(got))
	}
	if e := got[len(got)-1]; e.State != Failed {
		t.Errorf("last line %+v, failure cut", e)
	}
}

func TestChooseCapsAndCleans(t *testing.T) {
	w, r := pipe(t)
	var progs []string
	for i := range MaxChoices + 5 {
		progs = append(progs, `C:\Program Files\App`+strconv.Itoa(i)+"\u202e\x1b[31m.exe")
	}
	progs[0] = `C:\` + strings.Repeat("ä", 400) + ".exe"
	w.Choose(progs, strings.ToUpper(progs[2]))
	got := lines(t, w, r)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	e := got[0]
	if e.Step != StepChoose || e.State != Ask || !e.More || len(e.Choices) != MaxChoices {
		t.Fatalf("choose = step %s state %s more %v choices %d", e.Step, e.State, e.More, len(e.Choices))
	}
	for i, c := range e.Choices {
		if c.N != i+1 {
			t.Errorf("choice %d numbered %d", i, c.N)
		}
		if strings.ContainsAny(c.Path, "\u202e\x1b") || utf8.RuneCountInString(c.Path) > MaxText+1 {
			t.Errorf("choice %d not cleaned: %q", i, c.Path)
		}
		if c.Current != (i == 2) {
			t.Errorf("choice %d current = %v", i, c.Current)
		}
	}
}

func TestChooseLongLinesCut(t *testing.T) {
	w, r := pipe(t)
	var progs []string
	for range MaxChoices {
		// Quotes and backslashes double in JSON.
		progs = append(progs, strings.Repeat(`"\`, 200))
	}
	w.Choose(progs, "")
	got := lines(t, w, r)
	if len(got) != 1 || len(got[0].Choices) == 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestText(t *testing.T) {
	cases := map[string]string{
		"Example Paint":                "Example Paint",
		"  two   spaces\t\nand tabs  ": "two spaces and tabs",
		"right\u202eleft":              "rightleft",
		"zero\u200bwidth\ufeff":        "zerowidth",
		"bell\a esc\x1b[2J":            "bell esc[2J",
		"line\u2028sep":                "line sep",
		"bad\xffutf8":                  "badutf8",
		"private\ue000use":             "privateuse",
		"e\u0301\u0301\u0301\u0301tc":  "e\u0301\u0301tc",
		"\u0301leading mark":           "leading mark",
		"<b>markup</b> & \"quotes\"":   "<b>markup</b> & \"quotes\"",
	}
	for in, want := range cases {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
	long := Text(strings.Repeat("x", 500))
	if utf8.RuneCountInString(long) != MaxText+1 || !strings.HasSuffix(long, "…") {
		t.Errorf("long text = %d chars", utf8.RuneCountInString(long))
	}
}

// A child process, and so Wine, never holds the channel.
func TestChildDoesNotInherit(t *testing.T) {
	w, r, fd := pipeFd(t)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	out, err := exec.Command(sh, "-c", "[ -e /proc/self/fd/"+strconv.Itoa(fd)+" ] && echo open || echo closed").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "closed" {
		t.Errorf("child sees fd %d", fd)
	}
	w.Close()
	if b, _ := io.ReadAll(r); len(b) != 0 {
		t.Errorf("unexpected output %q", b)
	}
}

// A window that stops reading does not hold the command up.
func TestStuckReaderDropped(t *testing.T) {
	w, _ := pipe(t)
	start := time.Now()
	big := strings.Repeat("y", MaxText)
	var progs []string
	for range MaxChoices {
		progs = append(progs, big)
	}
	for range 200 {
		w.Choose(progs, "")
	}
	if took := time.Since(start); took > 3*writeWait {
		t.Errorf("writes took %v", took)
	}
	if w.f != nil {
		t.Error("stuck channel kept")
	}
}

func TestClosedReaderDropped(t *testing.T) {
	w, r := pipe(t)
	r.Close()
	w.Start(StepBase, "")
	if w.f != nil {
		t.Error("broken channel kept")
	}
	w.Finish(StepBase)
}

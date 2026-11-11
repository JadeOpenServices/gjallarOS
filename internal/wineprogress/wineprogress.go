// Package wineprogress writes gjallar-wine's progress lines for the
// prepare window (Plane 315). The window hands gjallar-wine the write end
// of a pipe with --progress-fd N and only draws what it reads: one JSON
// object per line, steps from a fixed set, error texts from a fixed list.
// Text that comes from an installer or a program is plain text, cut short
// and cleaned of control and format characters. The end of a command is
// its exit code, not a line.
package wineprogress

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Version is the protocol version every line carries.
const Version = 1

const (
	// MaxLine bounds one line, newline included.
	MaxLine = 16 << 10
	// MaxText bounds a text field, in characters.
	MaxText = 120
	// MaxChoices bounds the program list of one line.
	MaxChoices = 16
	// MaxLines bounds the lines of one command.
	MaxLines = 512
	// byteEvery is the least time between two byte counts of a step.
	byteEvery = 250 * time.Millisecond
	// writeWait is how long a line may wait for the window to read.
	writeWait = 2 * time.Second
)

// Step is one part of setting up an app.
type Step string

const (
	// StepBase is Wine's one-time setup, shown as the app space the
	// first time only.
	StepBase Step = "base"
	// StepCopy makes the app's own space from the base.
	StepCopy Step = "copy"
	// StepInstaller runs the installer in the app's sandbox.
	StepInstaller Step = "installer"
	// StepChoose asks which program the app starts; the window answers
	// with gjallar-wine program ID N.
	StepChoose Step = "choose"
)

// State is where a step is.
type State string

const (
	Running State = "running"
	Done    State = "done"
	Failed  State = "failed"
	// Ask is a step that waits for the user's answer.
	Ask State = "ask"
)

// Code names an error from the fixed list.
type Code string

const (
	CodeAppExists           Code = "app-exists"
	CodeBaseIncomplete      Code = "base-incomplete"
	CodeCopyFailed          Code = "copy-failed"
	CodeInstallerNotStarted Code = "installer-not-started"
	CodeFailed              Code = "failed"
)

// messages is the fixed list of error texts. Nothing from an installer or
// a program goes into them.
var messages = map[Code]string{
	CodeAppExists:           "An app with this name exists already.",
	CodeBaseIncomplete:      "Wine's setup did not finish. The next try picks up where it stopped.",
	CodeCopyFailed:          "The app's space could not be made.",
	CodeInstallerNotStarted: "The installer could not be started.",
	CodeFailed:              "Setting up the app stopped. The log says why.",
}

// Choice is one program the app can start.
type Choice struct {
	N       int    `json:"n"`
	Path    string `json:"path"`
	Current bool   `json:"current,omitempty"`
}

// Event is one line.
type Event struct {
	V       int      `json:"v"`
	Step    Step     `json:"step"`
	State   State    `json:"state"`
	App     string   `json:"app,omitempty"`
	Done    int64    `json:"done,omitempty"`
	Total   int64    `json:"total,omitempty"`
	Code    Code     `json:"code,omitempty"`
	Message string   `json:"message,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	// More says the program list was cut; the terminal command lists all.
	More bool `json:"more,omitempty"`
}

// Writer writes the lines of one command. A nil Writer writes nothing, so
// commands call it whether or not a window listens.
type Writer struct {
	mu        sync.Mutex
	f         *os.File
	lines     int
	cur       Step
	failed    bool
	lastBytes time.Time
	now       func() time.Time
}

// Open takes fd as the progress channel. It must be a pipe or socket open
// for writing, above stderr. The fd is closed on exec, so no child, and
// no Wine program, ever holds it.
func Open(fd int) (*Writer, error) {
	if fd < 3 {
		return nil, fmt.Errorf("progress fd %d must be 3 or above", fd)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("progress fd %d is not open", fd)
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFIFO && kind != unix.S_IFSOCK {
		return nil, fmt.Errorf("progress fd %d is not a pipe", fd)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, err
	}
	if mode := flags & unix.O_ACCMODE; mode != unix.O_WRONLY && mode != unix.O_RDWR {
		return nil, fmt.Errorf("progress fd %d is not open for writing", fd)
	}
	syscall.CloseOnExec(fd)
	// Non-blocking, so the file takes write deadlines: a window that stops
	// reading never holds the setup up.
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	return &Writer{f: os.NewFile(uintptr(fd), "progress"), now: time.Now}, nil
}

// Close closes the channel.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Start says step is running. app, when given, is the app's name.
func (w *Writer) Start(step Step, app string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cur = step
	w.lastBytes = time.Time{}
	w.write(Event{Step: step, State: Running, App: Text(app)})
}

// Bytes says how far step is; it writes at most every byteEvery, and
// always when done reaches total.
func (w *Writer) Bytes(step Step, done, total int64) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Counts stop short of the line cap, so the end of the step and a
	// failure still get through.
	if done < 0 || total < 0 || total > 0 && done > total || w.lines >= MaxLines-8 {
		return
	}
	now := w.now()
	if done != total && !w.lastBytes.IsZero() && now.Sub(w.lastBytes) < byteEvery {
		return
	}
	w.lastBytes = now
	w.write(Event{Step: step, State: Running, Done: done, Total: total})
}

// Finish says step is done.
func (w *Writer) Finish(step Step) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cur == step {
		w.cur = ""
	}
	w.write(Event{Step: step, State: Done})
}

// Fail says the running step failed, with the fixed text for code. Only
// the first failure is written.
func (w *Writer) Fail(code Code) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed {
		return
	}
	w.failed = true
	msg, ok := messages[code]
	if !ok {
		code, msg = CodeFailed, messages[CodeFailed]
	}
	w.write(Event{Step: w.cur, State: Failed, Code: code, Message: msg})
}

// Choose asks which of programs the app starts; current is the one it
// starts now, if any. The numbers are those gjallar-wine program takes.
func (w *Writer) Choose(programs []string, current string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cur = StepChoose
	e := Event{Step: StepChoose, State: Ask}
	for i, p := range programs {
		if len(e.Choices) == MaxChoices {
			e.More = true
			break
		}
		e.Choices = append(e.Choices, Choice{N: i + 1, Path: Text(p), Current: current != "" && strings.EqualFold(p, current)})
	}
	w.write(e)
}

// write sends e, cutting the program list when the line is too long. A
// channel the window stopped reading is dropped; the command goes on.
func (w *Writer) write(e Event) {
	if w.f == nil || w.lines >= MaxLines {
		return
	}
	e.V = Version
	line, err := encode(e)
	for err == nil && len(line) > MaxLine && len(e.Choices) > 0 {
		e.Choices = e.Choices[:len(e.Choices)-1]
		e.More = true
		line, err = encode(e)
	}
	if err != nil || len(line) > MaxLine {
		return
	}
	w.lines++
	// Without a deadline a write could wait for good, so no line goes out.
	if err := w.f.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		w.drop()
		return
	}
	if _, err := w.f.Write(line); err != nil {
		w.drop()
	}
}

func (w *Writer) drop() {
	w.f.Close()
	w.f = nil
}

func encode(e Event) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	// The window shows text with markup off; the HTML escapes only
	// lengthen the line.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// Text makes s safe to show as one line of plain text: invalid UTF-8,
// control, format (direction marks, zero-width), separator and private
// characters go, runs of space become one, at most two combining marks
// follow a character, and it is cut to MaxText characters.
func Text(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	n, marks := 0, 0
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = n > 0
			continue
		case !unicode.IsGraphic(r) || r == utf8.RuneError:
			continue
		case unicode.Is(unicode.M, r):
			if n == 0 || marks == 2 {
				continue
			}
			marks++
			b.WriteRune(r)
			continue
		}
		if n == MaxText {
			return strings.TrimRightFunc(b.String(), unicode.IsSpace) + "…"
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		marks = 0
		n++
		b.WriteRune(r)
	}
	return b.String()
}

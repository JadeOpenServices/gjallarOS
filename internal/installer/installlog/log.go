// Package installlog keeps a log of one installer run (Plane 343).
//
// The log lives in RAM while the installer runs and holds what the
// installer printed, never what the user typed. Secrets never reach it:
// prompt.UI shows recovery material on the terminal behind the log
// (Terminal), and every line passes the install report's redaction
// before it is written. A copy for the installed system or a drive is
// redacted again with everything known about the user by then (Export).
//
// Anchors (anchors.go) mark fixed points of the run with a code that never
// changes, so a log line can be found again in code and docs.
package installlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
)

// MaxSize caps the log. Past it only anchor lines are written, up to
// anchorReserve more bytes, so the end of the run still shows.
const (
	MaxSize       = 8 << 20
	anchorReserve = 64 << 10
)

// FileName is the log file's name for an installer run started at now.
func FileName(now time.Time) string {
	return "install-" + now.UTC().Format("20060102-150405") + ".log"
}

// StickFileName is the log file's name for a stick menu started at now.
func StickFileName(now time.Time) string {
	return "stick-" + now.UTC().Format("20060102-150405") + ".log"
}

// RuntimeDir is the RAM folder for the log of the running user. On live
// media the installer runs as the live user, who cannot create folders in
// /run; its own runtime folder is RAM as well and belongs to it alone.
func RuntimeDir() (string, error) {
	if os.Geteuid() == 0 {
		return "/run/gjallar-install", nil
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" || !filepath.IsAbs(runtime) {
		return "", errors.New("XDG_RUNTIME_DIR is not set; no install log this run")
	}
	return filepath.Join(filepath.Clean(runtime), "gjallar-install"), nil
}

// Log is one run's log file.
type Log struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	size    int64
	cut     bool
	known   installreport.Known
	now     func() time.Time
	pending map[*Tee][]byte
}

// Open creates the installer's log in dir. dir is created owner-only when
// missing and must belong to the running user with no access for anyone
// else. The file is new: an existing file or link at its name is never
// used.
func Open(dir string, now time.Time) (*Log, error) {
	return open(dir, FileName(now), "gjallarOS install log", now)
}

// OpenStick creates the stick menu's log in dir, under the same rules as
// Open.
func OpenStick(dir string, now time.Time) (*Log, error) {
	return open(dir, StickFileName(now), "gjallarOS installer stick log", now)
}

func open(dir, name, title string, now time.Time) (*Log, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("install log folder %q is not absolute", dir)
	}
	if err := unix.Mkdir(dir, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	dirFD, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	defer unix.Close(dirFD)
	var st unix.Stat_t
	if err := unix.Fstat(dirFD, &st); err != nil {
		return nil, fmt.Errorf("check %s: %w", dir, err)
	}
	if int(st.Uid) != os.Geteuid() || st.Mode&0o077 != 0 {
		return nil, fmt.Errorf("%s belongs to someone else or is open to others; no install log this run", dir)
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_APPEND|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create install log in %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	l := &Log{
		file:    os.NewFile(uintptr(fd), path),
		path:    path,
		now:     time.Now,
		pending: map[*Tee][]byte{},
	}
	l.writeLine(title + ", started " + now.UTC().Format(time.RFC3339))
	return l, nil
}

// Path is where the log file is.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// SetKnown sets the user's and machine's values that later lines hide.
// The installer learns them while it runs; Export hides them everywhere.
func (l *Log) SetKnown(known installreport.Known) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.known = known
	l.mu.Unlock()
}

// Known returns the values set with SetKnown.
func (l *Log) Known() installreport.Known {
	if l == nil {
		return installreport.Known{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.known
}

// Tee copies what is written to term into the log.
type Tee struct {
	term io.Writer
	log  *Log
}

// Tee returns a writer that writes to term and logs each finished line.
func (l *Log) Tee(term io.Writer) *Tee {
	return &Tee{term: term, log: l}
}

func (t *Tee) Write(p []byte) (int, error) {
	n, err := t.term.Write(p)
	if n > 0 && t.log != nil {
		t.log.feed(t, p[:n])
	}
	return n, err
}

// Terminal is the writer behind the log. Secrets go there directly, and
// prompt.New looks at it to decide whether a desktop dialog can open.
func (t *Tee) Terminal() io.Writer { return t.term }

func (l *Log) feed(t *Tee, p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	buf := append(l.pending[t], p...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		l.writeLineLocked(Clean(string(buf[:i])))
		buf = buf[i+1:]
	}
	// A line without an end that grows too long is written as it is.
	if len(buf) > 64<<10 {
		l.writeLineLocked(Clean(string(buf)))
		buf = nil
	}
	l.pending[t] = append([]byte(nil), buf...)
}

func (l *Log) writeLine(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeLineLocked(line)
}

func (l *Log) writeLineLocked(line string) {
	l.write(installreport.Redact(line, l.known), false)
}

func (l *Log) write(line string, anchor bool) {
	if l.file == nil {
		return
	}
	text := l.now().UTC().Format("15:04:05") + " " + line + "\n"
	limit := int64(MaxSize)
	if anchor {
		limit += anchorReserve
	}
	if l.size+int64(len(text)) > limit {
		if !l.cut {
			l.cut = true
			note := "[log cut at its size limit; only anchors follow]\n"
			n, _ := l.file.WriteString(note)
			l.size += int64(n)
		}
		return
	}
	n, _ := l.file.WriteString(text)
	l.size += int64(n)
}

// Close writes unfinished lines and closes the file. The file stays.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for t, buf := range l.pending {
		if len(buf) > 0 {
			l.writeLineLocked(Clean(string(buf)))
		}
		delete(l.pending, t)
	}
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Export returns the log redacted again with every value known by now, for
// a copy that leaves RAM. Unfinished lines are included.
func (l *Log) Export() ([]byte, error) {
	if l == nil {
		return nil, errors.New("no install log this run")
	}
	l.mu.Lock()
	for t, buf := range l.pending {
		if len(buf) > 0 {
			l.writeLineLocked(Clean(string(buf)))
		}
		delete(l.pending, t)
	}
	// Read the file this log created, never the path again: something in
	// the same session could have put a pipe or another file there.
	if l.file == nil {
		l.mu.Unlock()
		return nil, errors.New("install log is closed")
	}
	raw, err := io.ReadAll(io.NewSectionReader(l.file, 0, MaxSize+anchorReserve+4096))
	known := l.known
	l.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("read install log: %w", err)
	}
	return []byte(installreport.Redact(string(raw), known)), nil
}

var (
	csiSequence = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	oscSequence = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	escapeByte  = regexp.MustCompile(`\x1b.?`)
)

// Clean turns one line of terminal output into plain text. A progress bar
// redraws itself after a carriage return; only its last state stays.
// Escape codes are dropped; redaction removes any other control byte.
func Clean(line string) string {
	line = oscSequence.ReplaceAllString(line, "")
	line = csiSequence.ReplaceAllString(line, "")
	line = escapeByte.ReplaceAllString(line, "")
	for len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	return line
}

var current atomic.Pointer[Log]

// SetCurrent makes l the log that Mark writes to. nil stops marking.
func SetCurrent(l *Log) { current.Store(l) }

// Current is the log Mark writes to, or nil.
func Current() *Log { return current.Load() }

package winesandbox

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// StartLogCap is the most one start log file holds. When it is full it
// becomes the older file and a new one starts, so a start keeps at most
// twice this: the last part of what Wine and the program wrote.
const StartLogCap = 128 << 10

// StartLogDir holds the apps' start logs. It is outside every folder a
// sandbox can write, so a program can put lines into its log only by
// writing them, never replace the file or point it elsewhere.
func StartLogDir(home string) string {
	return filepath.Join(home, ".local", "state", "gjallar-wine", "logs")
}

func startLogNames(id string) (string, string) {
	return id + ".log", id + ".log.1"
}

// StartLog keeps what one start of app id writes, besides passing it on.
// It never fails the start: when the log cannot be written, it stops
// keeping, and what was kept so far stays.
type StartLog struct {
	mu        sync.Mutex
	root      *os.Root
	cur, old  string
	f         *os.File
	n         int
	keepError error
}

// OpenStartLog starts app id's log anew; the last start's log goes.
func OpenStartLog(home, id string) (*StartLog, error) {
	if !ValidID(id) {
		return nil, errors.New("not an app id")
	}
	dir := StartLogDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := RealDir(dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	l := &StartLog{root: root}
	l.cur, l.old = startLogNames(id)
	if err := root.Remove(l.old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		root.Close()
		return nil, err
	}
	if l.f, err = l.create(); err != nil {
		root.Close()
		return nil, err
	}
	return l, nil
}

func (l *StartLog) create() (*os.File, error) {
	return l.root.OpenFile(l.cur, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_APPEND, 0o600)
}

// keep adds p to the log, starting the next file when this one is full.
func (l *StartLog) keep(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if len(p) > StartLogCap {
		p = p[len(p)-StartLogCap:]
	}
	if l.n+len(p) > StartLogCap {
		err := l.f.Close()
		if err == nil {
			err = l.root.Rename(l.cur, l.old)
		}
		if err == nil {
			l.f, err = l.create()
		}
		if err != nil {
			l.f, l.keepError = nil, err
			return
		}
		l.n = 0
	}
	n, err := l.f.Write(p)
	l.n += n
	if err != nil {
		l.f.Close()
		l.f, l.keepError = nil, err
	}
}

// Stream passes what is written on to out and keeps it in the log. It
// takes all of every write, even when out or the log fails: the program
// must not stop on its output.
func (l *StartLog) Stream(out io.Writer) io.Writer {
	return streamWriter{l, out}
}

type streamWriter struct {
	l   *StartLog
	out io.Writer
}

func (w streamWriter) Write(p []byte) (int, error) {
	w.out.Write(p)
	w.l.keep(p)
	return len(p), nil
}

// Close ends the log and tells why it stopped keeping, if it did.
func (l *StartLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.keepError
	if l.f != nil {
		if cerr := l.f.Close(); err == nil {
			err = cerr
		}
		l.f = nil
	}
	l.root.Close()
	return err
}

// ReadStartLog gives the log of app id's last start, older part first, and
// when it was last written. It reads at most twice StartLogCap.
func ReadStartLog(home, id string) ([]byte, time.Time, error) {
	if !ValidID(id) {
		return nil, time.Time{}, errors.New("not an app id")
	}
	dir := StartLogDir(home)
	if err := RealDir(dir); err != nil {
		return nil, time.Time{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer root.Close()
	cur, old := startLogNames(id)
	var out []byte
	var when time.Time
	found := false
	for _, name := range []string{old, cur} {
		b, t, err := readCapped(root, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, time.Time{}, err
		}
		found = true
		out = append(out, b...)
		if t.After(when) {
			when = t
		}
	}
	if !found {
		return nil, time.Time{}, fs.ErrNotExist
	}
	return out, when, nil
}

// readCapped reads the last StartLogCap bytes of a plain file.
func readCapped(root *os.Root, name string) ([]byte, time.Time, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, time.Time{}, errors.New(name + " is not a plain file")
	}
	if size := info.Size(); size > StartLogCap {
		if _, err := f.Seek(size-StartLogCap, io.SeekStart); err != nil {
			return nil, time.Time{}, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, StartLogCap))
	return b, info.ModTime(), err
}

// DropStartLog removes app id's start log.
func DropStartLog(home, id string) error {
	if !ValidID(id) {
		return errors.New("not an app id")
	}
	dir := StartLogDir(home)
	switch err := RealDir(dir); {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	cur, old := startLogNames(id)
	for _, name := range []string{cur, old} {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

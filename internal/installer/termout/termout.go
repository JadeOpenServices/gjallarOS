// Package termout runs programs on the installer's terminal output.
//
// The installer sets its output writers once (SetOutput). In stick page
// mode they place every line in the page column; a program started with
// Attach then writes to a pseudo-terminal whose output goes through the
// column, so it still sees a terminal and its lines stay in the block.
// Nothing here writes to the install log; installlog.Run uses RunInPTY
// for the logged build commands.
package termout

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
)

// drainTime is how long RunInPTY keeps reading after the command ended. A
// process the command left behind could hold the terminal open for good.
var drainTime = 2 * time.Second

// output is where the installer's own text goes, set once its writers
// are known. Programs started with Attach write there.
var output struct {
	sync.Mutex
	stdout, stderr io.Writer
}

// SetOutput sets the writers Attach gives to programs. The installer sets
// its page writers here (stick page mode), never the log's: what such a
// program prints stays out of the log, as it did when it wrote to the
// terminal directly. nil means the process's own stdout or stderr.
func SetOutput(stdout, stderr io.Writer) {
	output.Lock()
	defer output.Unlock()
	output.stdout, output.stderr = stdout, stderr
}

// Output returns the writers set with SetOutput.
func Output() (stdout, stderr io.Writer) {
	output.Lock()
	defer output.Unlock()
	stdout, stderr = output.stdout, output.stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return stdout, stderr
}

// Attach runs cmd with its output on the installer's output (Output).
// Stdin is the caller's. Nothing goes into the log. In a page column the
// program writes to a pseudo-terminal whose output goes through the
// column, so it still sees a terminal and its lines stay in the block.
func Attach(cmd *exec.Cmd) error {
	stdout, stderr := Output()
	return AttachTo(cmd, stdout, stderr)
}

// AttachTo is Attach with the given writers.
func AttachTo(cmd *exec.Cmd, stdout, stderr io.Writer) error {
	// A page column around the terminal (stick page mode) would give the
	// program a pipe: it gets a pseudo-terminal, as with a log.
	if _, plain := stderr.(*os.File); !plain {
		if term, ok := TerminalFile(stderr); ok {
			// RunInPTY returns ErrNoPTY before it starts cmd.
			err := RunInPTY(cmd, term, stderr)
			if !errors.Is(err, ErrNoPTY) {
				return err
			}
		}
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// TerminalFile finds the terminal file under writers that place or copy the
// output, such as the stick's page column.
func TerminalFile(w io.Writer) (*os.File, bool) {
	for {
		t, ok := w.(interface{ Terminal() io.Writer })
		if !ok {
			break
		}
		w = t.Terminal()
	}
	f, ok := w.(*os.File)
	if !ok {
		return nil, false
	}
	if _, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS); err != nil {
		return nil, false
	}
	return f, true
}

var ErrNoPTY = errors.New("no pseudo-terminal")

// RunInPTY runs cmd with stdout and stderr on a new pseudo-terminal of the
// running user and copies its output to out. Its controlling terminal
// stays term's. It returns ErrNoPTY, before cmd starts, when no
// pseudo-terminal can be opened.
func RunInPTY(cmd *exec.Cmd, term *os.File, out io.Writer) error {
	master, slave, err := OpenPTY()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoPTY, err)
	}
	defer master.Close()

	width := calmmenu.PageWidth(out)
	resize := func() {
		if size, err := unix.IoctlGetWinsize(int(term.Fd()), unix.TIOCGWINSZ); err == nil {
			// In a page column the program gets the column's width, so its
			// lines and bars wrap inside the column, not at the screen edge.
			if width > 0 && int(size.Col) > width {
				size.Col = uint16(width)
			}
			_ = control(master, func(fd int) error { return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, size) })
		}
	}
	resize()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, unix.SIGWINCH)
	stopWinch := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-winch:
				resize()
			case <-stopWinch:
				return
			}
		}
	}()
	defer func() {
		signal.Stop(winch)
		close(stopWinch)
		wg.Wait()
	}()

	cmd.Stdout = slave
	cmd.Stderr = slave
	err = cmd.Start()
	slave.Close()
	if err != nil {
		return err
	}
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		// The read ends with EIO once no process holds the terminal any
		// more, or at the deadline set below.
		_, _ = io.Copy(out, master)
	}()
	err = cmd.Wait()
	_ = master.SetReadDeadline(time.Now().Add(drainTime))
	<-copied
	// A program that stopped in the middle of a colour would leave it on
	// for the text after it.
	_, _ = io.WriteString(term, "\x1b[0m")
	return err
}

// OpenPTY opens a new pseudo-terminal pair. The child side is opened from
// the master (TIOCGPTPEER), never by its path in /dev/pts, and never
// becomes the controlling terminal of this process.
func OpenPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var peer uintptr
	err = control(master, func(fd int) error {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return fmt.Errorf("unlock pseudo-terminal: %w", err)
		}
		p, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCGPTPEER, uintptr(unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC))
		if errno != 0 {
			return fmt.Errorf("open pseudo-terminal peer: %w", errno)
		}
		// devpts gives the tty group write access, for wall and write. A
		// message from another user would land in the log; nobody but this
		// user writes to the build output.
		if err := unix.Fchmod(int(p), 0o600); err != nil {
			unix.Close(int(p))
			return fmt.Errorf("close pseudo-terminal to others: %w", err)
		}
		peer = p
		return nil
	})
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, os.NewFile(peer, "pty-peer"), nil
}

// control runs fn on f's descriptor. f.Fd would switch f to blocking mode
// and turn off the read deadline the drain relies on.
func control(f *os.File, fn func(fd int) error) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var fnErr error
	if err := raw.Control(func(fd uintptr) { fnErr = fn(int(fd)) }); err != nil {
		return err
	}
	return fnErr
}

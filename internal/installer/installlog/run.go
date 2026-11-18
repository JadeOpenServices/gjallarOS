package installlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// logged are the commands whose output Run copies into the log. Their
// output is build progress and errors; commands that handle secrets
// (chpasswd, age-keygen and the like) never go through Run.
var logged = map[string]bool{
	"nixos-install": true,
	"nixos-rebuild": true,
	"nix-channel":   true,
}

// drainTime is how long Run keeps reading after the command ended. A
// process the command left behind could hold the terminal open for good.
var drainTime = 2 * time.Second

// Run runs a Nix build command and copies its output into the current log.
// name is the command or "sudo" with the command as its first argument;
// any other command is refused.
//
// Without a log it runs as plain exec with the given stdin, stdout and
// stderr. With a log, Nix's progress bar still shows: when stderr is a
// terminal, the command writes to a pseudo-terminal whose output goes to
// that terminal as it is and into the log as clean lines. Only output
// passes it. Stdin is not passed on then, so nothing typed can come back
// through an echo; sudo asks for its password on the controlling terminal,
// which stays the real one, as does Ctrl-C.
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	if err := allowed(name, args); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	log := Current()
	if log == nil {
		// A page column around the terminal (stick page mode) would give
		// the program a pipe: it gets a pseudo-terminal, as with a log.
		if _, plain := stderr.(*os.File); !plain {
			if term, ok := terminalFile(stderr); ok {
				err := runInPTY(cmd, term, stderr)
				if !errors.Is(err, errNoPTY) {
					return err
				}
				cmd = exec.CommandContext(ctx, name, args...)
			}
		}
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		return cmd.Run()
	}
	tee := teeFor(log, stderr)
	if term, ok := terminalFile(tee.Terminal()); ok {
		err := runInPTY(cmd, term, tee)
		if !errors.Is(err, errNoPTY) {
			return err
		}
		cmd = exec.CommandContext(ctx, name, args...)
	}
	cmd.Stdout = teeFor(log, stdout)
	cmd.Stderr = tee
	return cmd.Run()
}

// Logs reports whether Run takes the command: name, or sudo with the
// command as its first argument, is one of the logged build commands.
func Logs(name string, args []string) bool {
	return logged[command(name, args)]
}

func command(name string, args []string) string {
	if name == "sudo" && len(args) > 0 {
		return args[0]
	}
	return name
}

func allowed(name string, args []string) error {
	if !Logs(name, args) {
		return fmt.Errorf("installlog.Run: %s is not a logged build command", command(name, args))
	}
	return nil
}

// teeFor logs through w when it is already a Tee, so nothing is logged twice.
func teeFor(log *Log, w io.Writer) *Tee {
	if t, ok := w.(*Tee); ok {
		return t
	}
	return log.Tee(w)
}

// terminalFile finds the terminal under writers that place or copy the
// output, such as the stick's page column.
func terminalFile(w io.Writer) (*os.File, bool) {
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

// pageWidth is the width of the page column out writes into, or 0 when
// out is not in one.
func pageWidth(w io.Writer) int {
	for {
		if p, ok := w.(interface{ PageWidth() int }); ok {
			return p.PageWidth()
		}
		t, ok := w.(interface{ Terminal() io.Writer })
		if !ok {
			return 0
		}
		w = t.Terminal()
	}
}

var errNoPTY = errors.New("no pseudo-terminal")

// runInPTY runs cmd with stdout and stderr on a new pseudo-terminal of the
// running user. Its controlling terminal stays term's.
func runInPTY(cmd *exec.Cmd, term *os.File, out io.Writer) error {
	master, slave, err := openPTY()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoPTY, err)
	}
	defer master.Close()

	width := pageWidth(out)
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
	return err
}

// openPTY opens a new pseudo-terminal pair. The child side is opened from
// the master (TIOCGPTPEER), never by its path in /dev/pts, and never
// becomes the controlling terminal of this process.
func openPTY() (master, slave *os.File, err error) {
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

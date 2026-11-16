// Package stick is the installer stick's menu (Plane 344/350): keyboard,
// network, then install, recovery or a shell. It runs as root on the stick's
// screen, in a cage window or on the text console, and hands the install to
// gjallar-recover, which starts the Go installer.
//
// What it shows on the screen of a stranger's network (names of nearby
// Wi-Fi networks) never goes into its log: the log has anchors and fixed
// error lines only.
package stick

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/input/xkb"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/region"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickkeyboard"
)

// Runner runs the programs the menu needs. Tests use a fake.
type Runner interface {
	// Output runs a program and returns what it printed.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Interactive runs a program on the menu's terminal.
	Interactive(ctx context.Context, name string, args ...string) error
}

// Exec is the real Runner.
type Exec struct{}

func (Exec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (Exec) Interactive(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Display modes, from the host that started the menu: a cage window, or
// the text console when no window could start.
const (
	ModeWindow  = "window"
	ModeConsole = "console"
)

// ErrRestartDisplay asks the host to start the window again, so a new
// keyboard layout takes effect there. The command exits with ExitRestart.
var ErrRestartDisplay = errors.New("keyboard layout changed; the window starts again")

// ExitRestart is the exit code for ErrRestartDisplay (EX_TEMPFAIL).
const ExitRestart = 75

// Channels the Advanced entry offers; stable is the default.
var channels = []string{"stable", "staging", "dev"}

// Stick is one run of the menu.
type Stick struct {
	UI  prompt.UI
	Cmd Runner
	// Mode is ModeWindow or ModeConsole.
	Mode string
	// TTY is the terminal the menu reads; nil in tests. Keys typed while
	// no question is up are dropped from it before each question.
	TTY *os.File
	// Sleep waits between network checks: a second in real use.
	Sleep func()
	// Keyboard reads and writes the layout handed to the installer;
	// stickkeyboard in real use.
	ReadLayout  func() (string, error)
	WriteLayout func(string) error
	// ErrorLog takes the fixed error lines for the log; nil for none.
	ErrorLog io.Writer

	channel   string
	offline   bool
	lookAgain int
	markedVia string
}

// New returns a Stick for the real stick.
func New(mode string) *Stick {
	return &Stick{
		UI:          prompt.UI{Reader: bufio.NewReader(os.Stdin), Out: os.Stdout},
		Cmd:         Exec{},
		Mode:        mode,
		TTY:         os.Stdin,
		Sleep:       func() { time.Sleep(time.Second) },
		ReadLayout:  stickkeyboard.Read,
		WriteLayout: stickkeyboard.Write,
	}
}

// Run shows the menu until the user restarts or powers off, or a new
// keyboard layout needs the window started again.
func (s *Stick) Run(ctx context.Context) error {
	if s.channel == "" {
		s.channel = channels[0]
	}
	s.markDisplay()
	if _, err := s.ReadLayout(); errors.Is(err, stickkeyboard.ErrNone) {
		if err := s.keyboardStep(ctx); err != nil {
			return err
		}
	} else if err == nil && s.Mode == ModeConsole {
		// The console forgets its keymap when the menu starts again on
		// another tty; set it from the file each time.
		s.applyConsoleLayout(ctx)
	}
	if err := s.networkStep(ctx); err != nil {
		return err
	}
	return s.menu(ctx)
}

func (s *Stick) markDisplay() {
	switch s.Mode {
	case ModeWindow:
		installlog.Mark(installlog.StickDisplayStarted, installlog.Choice("mode", "window"))
	case ModeConsole:
		installlog.Mark(installlog.StickDisplayStarted, installlog.Choice("mode", "console"))
	}
}

// Menu entries.
const (
	menuInstall  = "Install gjallarOS"
	menuRecovery = "Recovery (unlock an installed gjallarOS with its recovery key)"
	menuShell    = "Shell"
	menuKeyboard = "Keyboard layout"
	menuNetwork  = "Network"
	menuAdvanced = "Advanced"
	menuRestart  = "Restart"
	menuPowerOff = "Power off"
)

func (s *Stick) menu(ctx context.Context) error {
	for {
		fmt.Fprintln(s.UI.Out)
		fmt.Fprintln(s.UI.Out, "gjallarOS installer")
		if s.channel != channels[0] {
			fmt.Fprintf(s.UI.Out, "Release channel: %s\n", s.channel)
		}
		choice, err := s.choose(ctx, "Choice", []string{
			menuInstall, menuAdvanced, menuRecovery, menuShell,
			menuKeyboard, menuNetwork, menuRestart, menuPowerOff,
		})
		if err != nil {
			return err
		}
		switch choice {
		case menuInstall:
			if s.offline {
				fmt.Fprintln(s.UI.Out, "Installing needs a network connection.")
				if err := s.networkStep(ctx); err != nil {
					return err
				}
				if s.offline {
					continue
				}
			}
			installlog.Mark(installlog.StickMenuChosen, installlog.Choice("choice", "install"))
			args := []string{"guided", "fresh"}
			if s.channel != channels[0] {
				args = append(args, "--channel", s.channel)
			}
			s.runStep(ctx, "the installer", "gjallar-recover", args...)
		case menuAdvanced:
			if err := s.advanced(ctx); err != nil {
				return err
			}
		case menuRecovery:
			installlog.Mark(installlog.StickMenuChosen, installlog.Choice("choice", "recovery"))
			s.runStep(ctx, "the recovery console", "gjallar-recovery-console", "--from-stick")
		case menuShell:
			installlog.Mark(installlog.StickMenuChosen, installlog.Choice("choice", "shell"))
			fmt.Fprintln(s.UI.Out, "Type exit to come back to this menu.")
			s.runStep(ctx, "the shell", "runuser", "-l", "nixos")
		case menuKeyboard:
			if err := s.keyboardStep(ctx); err != nil {
				return err
			}
		case menuNetwork:
			if err := s.networkStep(ctx); err != nil {
				return err
			}
		case menuRestart:
			s.runStep(ctx, "the restart", "systemctl", "reboot")
		case menuPowerOff:
			s.runStep(ctx, "the power off", "systemctl", "poweroff")
		}
	}
}

// runStep runs a program on the terminal and reports a failure without
// its output: the program printed its own reasons on the screen.
func (s *Stick) runStep(ctx context.Context, what, name string, args ...string) {
	err := s.Cmd.Interactive(ctx, name, args...)
	if err == nil {
		return
	}
	code := -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	installlog.Mark(installlog.StickFlowFailed)
	s.logError(fmt.Sprintf("%s ended with exit code %d", what, code))
	fmt.Fprintf(s.UI.Out, "%s ended with an error (exit code %d). You are back in the stick menu.\n", upperFirst(what), code)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// logError puts a fixed error line into the log, under the anchor.
func (s *Stick) logError(line string) {
	if s.ErrorLog != nil {
		fmt.Fprintf(s.ErrorLog, "ERROR: %s\n", line)
	}
}

func (s *Stick) advanced(ctx context.Context) error {
	fmt.Fprintln(s.UI.Out, "Pick a release channel. Stable gets tested updates. Staging and dev get them earlier and break more often.")
	choice, err := s.choose(ctx, "Channel", channels)
	if err != nil || choice == s.channel {
		return err
	}
	s.channel = choice
	switch choice {
	case "stable":
		installlog.Mark(installlog.StickChannelAdvanced, installlog.Choice("channel", "stable"))
	case "staging":
		installlog.Mark(installlog.StickChannelAdvanced, installlog.Choice("channel", "staging"))
	case "dev":
		installlog.Mark(installlog.StickChannelAdvanced, installlog.Choice("channel", "dev"))
	}
	return nil
}

// keyboardStep asks for the layout, stores it for the installer and sets
// it. A window has to start again for a new layout; the console takes it
// at once.
func (s *Stick) keyboardStep(ctx context.Context) error {
	current, err := s.ReadLayout()
	if err != nil {
		current = region.DefaultKeyboard
	}
	options := []string{}
	for _, layout := range region.KeyboardLayouts() {
		if stickkeyboard.Valid(layout) {
			options = append(options, layout)
		}
	}
	s.flush()
	fmt.Fprintln(s.UI.Out, "Type a keyboard layout such as us, gb or fr. Part of a name shows a list.")
	picked, err := s.UI.Pick(ctx, "Keyboard layout", current, options)
	if err != nil {
		return err
	}
	normal, err := xkb.Normalize(picked)
	if err != nil || !stickkeyboard.Valid(normal.Name) {
		fmt.Fprintf(s.UI.Out, "That layout cannot be used. Still using %s.\n", current)
		return nil
	}
	if err := s.WriteLayout(normal.Name); err != nil {
		installlog.Mark(installlog.StickFlowFailed)
		s.logError("could not store the keyboard layout")
		fmt.Fprintln(s.UI.Out, "Could not store the keyboard layout. The installer will ask again.")
		return nil
	}
	if normal.Name == current {
		return nil
	}
	if s.Mode == ModeConsole {
		s.applyConsoleLayout(ctx)
		return nil
	}
	return ErrRestartDisplay
}

// applyConsoleLayout loads the stored layout into the text console.
func (s *Stick) applyConsoleLayout(ctx context.Context) {
	layout, err := s.ReadLayout()
	if err != nil {
		return
	}
	// ckbcomp turns the X layout into a console keymap; loadkeys reads it
	// from its standard input.
	if err := s.Cmd.Interactive(ctx, "sh", "-c", `ckbcomp -layout "$1" | loadkeys -q`, "sh", layout); err != nil {
		fmt.Fprintf(s.UI.Out, "The console keeps its old layout. The installed system gets %s.\n", layout)
	}
}

// choose asks for one of options by its number or its text.
func (s *Stick) choose(ctx context.Context, label string, options []string) (string, error) {
	s.flush()
	return s.UI.Choice(ctx, label, "", options)
}

// chooseIndex asks for one of options by number only: names of nearby
// networks are not unique once made printable.
func (s *Stick) chooseIndex(ctx context.Context, label string, options []string) (int, error) {
	s.flush()
	for i, v := range options {
		fmt.Fprintf(s.UI.Out, "  %d) %s\n", i+1, v)
	}
	for {
		value, err := s.UI.Value(ctx, label+" (number)", "")
		if err != nil {
			return 0, err
		}
		if n, e := strconv.Atoi(value); e == nil && n > 0 && n <= len(options) {
			return n - 1, nil
		}
		fmt.Fprintln(s.UI.Out, "Type one of the numbers.")
	}
}

// flush drops keys typed while no question was up, so a key pressed during
// a scan or after the installer ended cannot answer the next question.
func (s *Stick) flush() {
	if s.TTY == nil {
		return
	}
	_ = unix.IoctlSetInt(int(s.TTY.Fd()), unix.TCFLSH, unix.TCIFLUSH)
	s.UI.Reader.Reset(s.TTY)
}

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
	"github.com/JadeOpenServices/gjallarOS/internal/installer/bootnext"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/region"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickkeyboard"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickrelease"
	"github.com/JadeOpenServices/gjallarOS/internal/tty"
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

// Interactive passes the page variables on only when ctx asks for pages
// (prompt.WithPage): the installer steps get them, a shell or nmcli never.
func (Exec) Interactive(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = prompt.PageEnviron(ctx, os.Environ())
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
	// Size reports the terminal in cells and pixels; nil means 80 by 24
	// with no pixel size (no logo).
	Size func() calmmenu.Size
	// Logos are the logo in sixel for 32 and 64 pixel cells, shown in
	// the window only; empty for none.
	Logos []calmmenu.Sixel
	// CheckRelease compares the stick with the newest signed release
	// before an install (Q233). nil refuses to install, as a stick built
	// without the release key.
	CheckRelease func(context.Context) stickrelease.Outcome
	// ShimStarted reports whether this start went through shim. Queue the
	// key again is shown only then: without shim no MokManager runs. nil
	// hides it.
	ShimStarted func() (bool, error)

	channel   string
	offline   bool
	lookAgain int
	markedVia string
	// lookFailed is set once the look for an installed system failed.
	lookFailed bool
	// note is one line for the next page, such as an error: a page clears
	// the screen, so text printed before it is gone. The next draw shows
	// it and clears it.
	note string
	// col keeps running text in the block of the last page; nil before
	// the first one.
	col *calmmenu.Column
}

// out is where running text goes: the column of the last page.
func (s *Stick) out() io.Writer {
	if s.col != nil {
		return s.col
	}
	return s.UI.Out
}

// say prints text in the page's column, which wraps it.
func (s *Stick) say(text string) {
	fmt.Fprint(s.out(), calmmenu.WrapFor(s.out(), text+"\n"))
}

// size is the terminal in cells and pixels.
func (s *Stick) size() calmmenu.Size {
	if s.Size == nil {
		return calmmenu.Size{Cols: 80, Rows: 24}
	}
	return s.Size()
}

// head starts a page for a step that prints as it works: the title on
// top, then the step's output in the column below it.
func (s *Stick) head(title string) {
	c, err := calmmenu.Head(s.UI.Out, title, s.size(), true)
	if err == nil {
		s.col = c
	}
}

// ask shows m as a page and reads the answer.
func (s *Stick) ask(ctx context.Context, m calmmenu.Menu) (string, error) {
	s.flush()
	if err := s.draw(m, false); err != nil {
		return "", err
	}
	return s.UI.Line(ctx)
}

// pick shows options as a numbered list on a page and returns the index
// of the one chosen. byText also takes the option's text, ignoring case;
// otherwise only numbers count, as for names of nearby networks, which are
// not unique once made printable.
func (s *Stick) pick(ctx context.Context, title string, body []string, label string, options []string, byText bool) (int, error) {
	items := make([]calmmenu.Item, len(options))
	for i, o := range options {
		items[i] = calmmenu.Item{Key: strconv.Itoa(i + 1), Label: o}
	}
	prompt, again := label+": ", "Choose one listed value."
	if !byText {
		prompt, again = label+" (number): ", "Type one of the numbers."
	}
	for {
		value, err := s.ask(ctx, calmmenu.Menu{
			Title:  title,
			Body:   body,
			Groups: [][]calmmenu.Item{items},
			Hint:   "Type a number and press Enter.",
			Prompt: prompt,
		})
		if err != nil {
			return 0, err
		}
		if n, e := strconv.Atoi(value); e == nil && n > 0 && n <= len(options) {
			return n - 1, nil
		}
		for i, o := range options {
			if byText && strings.EqualFold(o, value) {
				return i, nil
			}
		}
		s.note = again
	}
}

// New returns a Stick for the real stick.
func New(mode string) *Stick {
	return &Stick{
		UI:          prompt.UI{Reader: bufio.NewReader(os.Stdin), Out: os.Stdout, Wrap: true},
		Cmd:         Exec{},
		Mode:        mode,
		TTY:         os.Stdin,
		Sleep:       func() { time.Sleep(time.Second) },
		ReadLayout:  stickkeyboard.Read,
		WriteLayout: stickkeyboard.Write,
		Size:        func() calmmenu.Size { return tty.Size(os.Stdout) },
		ShimStarted: secureboot.ShimStarted,
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
	if err := s.networkStep(ctx, ""); err != nil {
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

// The menu's entries. The numbers follow from where an entry sits, so they
// shift when Start installed gjallarOS is on top (Q235).
type action int

const (
	actStart action = iota
	actInstall
	actRecovery
	actRequeue
	actShell
	actNetwork
	actKeyboard
	actAdvanced
	actRestart
	actPowerOff
)

type entry struct {
	act         action
	label, desc string
}

// menuLayout groups the entries as THEMEBRAIN section 18 shows them: the
// task, ways out of trouble, settings, the machine.
var menuLayout = [][]entry{
	{
		{actStart, "Start installed gjallarOS", "Leave the installer and start it"},
		{actInstall, "Install gjallarOS", "Set up gjallarOS on this computer"},
	},
	{
		{actRecovery, "Recovery", "Unlock an installed gjallarOS with its recovery key"},
		{actRequeue, "Queue the key again", "When the blue MokManager screen after the install was missed"},
		{actShell, "Shell", "A command line. Type exit to come back here"},
	},
	{{act: actNetwork, label: "Network"}, {act: actKeyboard, label: "Keyboard layout"}, {act: actAdvanced, label: "Advanced"}},
	{{act: actRestart, label: "Restart"}, {act: actPowerOff, label: "Power off"}},
}

// numbered is the menu as shown: Start installed gjallarOS only when an
// installed system was found, Queue the key again only when shim started
// this stick, the numbers counted from 1.
func numbered(installed, shim bool) ([][]calmmenu.Item, map[string]action) {
	keys := map[string]action{}
	var groups [][]calmmenu.Item
	for _, g := range menuLayout {
		var items []calmmenu.Item
		for _, e := range g {
			if e.act == actStart && !installed || e.act == actRequeue && !shim {
				continue
			}
			key := strconv.Itoa(len(keys) + 1)
			keys[key] = e.act
			items = append(items, calmmenu.Item{Key: key, Label: e.label, Desc: e.desc})
		}
		groups = append(groups, items)
	}
	return groups, keys
}

// installed reports whether a firmware entry starts an installed gjallarOS.
// A failed look shows the menu without Start installed gjallarOS and logs
// once.
func (s *Stick) installed(ctx context.Context) bool {
	found, err := bootnext.Find(ctx, s.Cmd)
	if err != nil {
		if !s.lookFailed {
			s.lookFailed = true
			s.logError("could not look for an installed gjallarOS")
		}
		return false
	}
	return len(found) > 0
}

// shim reports whether this start went through shim. A failed look hides
// Queue the key again.
func (s *Stick) shim() bool {
	if s.ShimStarted == nil {
		return false
	}
	started, err := s.ShimStarted()
	return err == nil && started
}

// startInstalled points the next start at the installed system, looked up
// again now, and restarts.
func (s *Stick) startInstalled(ctx context.Context) {
	found, err := bootnext.Find(ctx, s.Cmd)
	if err == nil && len(found) == 0 {
		installlog.Mark(installlog.StickInstalledStart, installlog.Choice("result", "none"))
		s.note = "No installed gjallarOS was found anymore."
		return
	}
	if err == nil {
		err = bootnext.Set(ctx, s.Cmd, found[0].Entry)
	}
	if err != nil {
		installlog.Mark(installlog.StickInstalledStart, installlog.Choice("result", "failed"))
		s.logError("could not set the next start to the installed gjallarOS")
		s.note = "The next start could not be set. Use the firmware's boot menu."
		return
	}
	installlog.Mark(installlog.StickInstalledStart, installlog.Choice("result", "set"))
	s.head("Start installed gjallarOS")
	s.say("Restarting into the installed gjallarOS...")
	s.runStep(ctx, "the restart", "systemctl", "reboot")
}

func (s *Stick) menu(ctx context.Context) error {
	for {
		s.flush()
		groups, keys := numbered(s.installed(ctx), s.shim())
		if err := s.draw(s.screen(groups), true); err != nil {
			return err
		}
		choice, err := s.UI.Line(ctx)
		if err != nil {
			return err
		}
		act, ok := keys[choice]
		if !ok {
			s.note = fmt.Sprintf("Type one of the numbers 1 to %d.", len(keys))
			continue
		}
		switch act {
		case actStart:
			s.startInstalled(ctx)
		case actInstall:
			if s.offline {
				if err := s.networkStep(ctx, "Installing needs a network connection."); err != nil {
					return err
				}
				if s.offline {
					continue
				}
			}
			installlog.Mark(installlog.StickMenuChosen, installlog.Choice("choice", "install"))
			allowed, err := s.releaseAllows(ctx)
			if err != nil {
				return err
			}
			if !allowed {
				continue
			}
			args := []string{"guided", "fresh"}
			if s.channel != channels[0] {
				args = append(args, "--channel", s.channel)
			}
			s.runStep(prompt.WithPage(ctx, "Install gjallarOS"), "the installer", "gjallar-recover", args...)
			if err := s.pause(ctx); err != nil {
				return err
			}
		case actRecovery:
			installlog.Mark(installlog.StickMenuChosen, installlog.Choice("choice", "recovery"))
			s.head("Recovery")
			s.runStep(ctx, "the recovery console", "gjallar-recovery-console", "--from-stick")
			if err := s.pause(ctx); err != nil {
				return err
			}
		case actRequeue:
			installlog.Mark(installlog.StickMenuChosen, installlog.Choice("choice", "requeue-key"))
			s.head("Queue the key again")
			s.runStep(prompt.WithPage(ctx, "Queue the key again"), "queueing the key", "gjallar-stick", "requeue-key")
			if err := s.pause(ctx); err != nil {
				return err
			}
		case actShell:
			installlog.Mark(installlog.StickMenuChosen, installlog.Choice("choice", "shell"))
			s.runStep(ctx, "the shell", "runuser", "-l", "nixos", "-c", shellCommand(s.size()))
		case actNetwork:
			if err := s.networkStep(ctx, ""); err != nil {
				return err
			}
		case actKeyboard:
			if err := s.keyboardStep(ctx); err != nil {
				return err
			}
		case actAdvanced:
			if err := s.advanced(ctx); err != nil {
				return err
			}
		case actRestart:
			s.head("Restart")
			s.runStep(ctx, "the restart", "systemctl", "reboot")
		case actPowerOff:
			s.head("Power off")
			s.runStep(ctx, "the power off", "systemctl", "poweroff")
		}
	}
}

// screen is the menu with the state as of now.
func (s *Stick) screen(groups [][]calmmenu.Item) calmmenu.Menu {
	network := "Not connected"
	if !s.offline && s.markedVia != "" {
		network = upperFirst(viaName(s.markedVia)) + ", connected"
	}
	keyboard, err := s.ReadLayout()
	if err != nil {
		keyboard = "not set"
	}
	return calmmenu.Menu{
		Title: "gjallarOS installer",
		Status: []calmmenu.Field{
			{Name: "Network", Value: network},
			{Name: "Keyboard", Value: keyboard},
			{Name: "Channel", Value: s.channel},
		},
		Groups: groups,
		Hint:   "Type a number and press Enter.",
		Prompt: "Choice: ",
	}
}

// draw shows m as a page centred on the screen, with the pending note,
// and with the logo on top when withLogo is set (the main menu only) in
// the window when the terminal reports its pixel size and Render finds
// room for it.
// Text printed after it goes into the page's column.
func (s *Stick) draw(m calmmenu.Menu, withLogo bool) error {
	sz := s.size()
	if m.Note == "" {
		m.Note = s.note
	}
	s.note = ""
	var logo calmmenu.Logo
	var picture calmmenu.Sixel
	if withLogo && s.Mode == ModeWindow && len(s.Logos) > 0 && sz.XPixel > 0 && sz.YPixel > 0 {
		cw, ch := sz.XPixel/sz.Cols, sz.YPixel/sz.Rows
		picture = s.Logos[0]
		if ch >= 48 && len(s.Logos) > 1 {
			// 4K cells are about 62 pixels high: the 64 pixel logo.
			picture = s.Logos[1]
		}
		logo = picture.Cells(cw, ch)
	}
	p, err := calmmenu.Render(s.UI.Out, m, logo, sz, true)
	s.col = calmmenu.ColumnIn(s.UI.Out, p.Frame)
	if err != nil || p.LogoRow == 0 {
		return err
	}
	return picture.Draw(s.UI.Out, p)
}

// pause keeps what a program printed on the screen until the user has
// read it: the menu clears the screen when it comes back.
func (s *Stick) pause(ctx context.Context) error {
	s.flush()
	fmt.Fprint(s.out(), "\nPress Enter to go back to the menu. ")
	_, err := s.UI.Line(ctx)
	return err
}

// releaseAllows checks the stick against the newest signed release and
// shows why on a page when it may not install. Only release names that
// passed the strict pattern reach the screen; the network's error text
// does not.
func (s *Stick) releaseAllows(ctx context.Context) (bool, error) {
	s.head("Install gjallarOS")
	s.say("Checking that this stick is still safe to install from...")
	o := stickrelease.Outcome{Kind: stickrelease.NoKey}
	if s.CheckRelease != nil {
		o = s.CheckRelease(ctx)
	}
	refuse := func(log, text, note string) (bool, error) {
		s.logError(log)
		_, err := s.ask(ctx, calmmenu.Menu{
			Title:  "Install gjallarOS",
			Body:   []string{text + " Nothing on this computer was changed."},
			Prompt: "Press Enter to go back to the menu. ",
		})
		s.note = note
		return false, err
	}
	switch o.Kind {
	case stickrelease.Current:
		return true, nil
	case stickrelease.Older:
		installlog.Mark(installlog.StickReleaseOlder)
		s.say(fmt.Sprintf("A newer release, %s, is out. This stick installs %s; update after the install.", o.Newest.Version, o.Stick))
		return true, nil
	case stickrelease.BelowFloor:
		installlog.Mark(installlog.StickReleaseRefused, installlog.Choice("reason", "floor"))
		return refuse("the stick is below the release floor",
			fmt.Sprintf("This stick installs gjallarOS %s, which is older than %s, the oldest release that is still safe to install. Make a new stick with the USB maker from the gjallarOS releases page.", o.Stick, o.Newest.Floor),
			"This stick is too old to install from. Make a new stick.")
	case stickrelease.Unreachable:
		installlog.Mark(installlog.StickReleaseRefused, installlog.Choice("reason", "unreachable"))
		return refuse("the release check could not fetch the release files",
			"Could not reach the gjallarOS releases to check that this stick is still safe to install from. Check the network and try again.",
			"Could not reach the gjallarOS releases. Check the network.")
	case stickrelease.Bad:
		installlog.Mark(installlog.StickReleaseRefused, installlog.Choice("reason", "bad"))
		return refuse("the release files did not match the release key",
			"The release files from the internet did not match the gjallarOS signature: something on the way may have changed them. Try another network, and report this with the time if it happens again.",
			"The release check failed: the files did not match.")
	default:
		installlog.Mark(installlog.StickReleaseRefused, installlog.Choice("reason", "no-key"))
		return refuse("the stick was built without the release key",
			"This stick was built without the gjallarOS release key, so it cannot check that it is still safe to install from. Use a stick made with the USB maker from a gjallarOS release.",
			"This stick cannot check its release. Use a released stick.")
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
	// The note stays on the menu page: it leaves out the last part, to keep it short.
	s.note = fmt.Sprintf("%s ended with an error (exit code %d).", upperFirst(what), code)
	s.say(s.note + " You are back in the stick menu.")
}

// shellCommand starts the nixos user's login shell on a screen of size
// sz, in the page frame like every other page:
// gjallar-stick-shell (system/installer-media/stick.nix) takes the frame's
// first column, first row, width and height.
func shellCommand(sz calmmenu.Size) string {
	f := calmmenu.FrameFor(sz)
	return fmt.Sprintf("exec gjallar-stick-shell %d %d %d %d", f.Col, f.Row, f.Width, f.Height)
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
	i, err := s.pick(ctx, "Advanced", []string{"Pick a release channel. Stable gets tested updates. Staging and dev get them earlier and break more often."}, "Channel", channels, true)
	if err != nil || channels[i] == s.channel {
		return err
	}
	choice := channels[i]
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
	picked, err := s.pickLayout(ctx, current, options)
	if err != nil {
		return err
	}
	normal, err := xkb.Normalize(picked)
	if err != nil || !stickkeyboard.Valid(normal.Name) {
		s.note = fmt.Sprintf("That layout cannot be used. Still using %s.", current)
		return nil
	}
	if err := s.WriteLayout(normal.Name); err != nil {
		installlog.Mark(installlog.StickFlowFailed)
		s.logError("could not store the keyboard layout")
		s.note = "Could not store the keyboard layout. The installer will ask again."
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

// pickLayout is prompt.Pick as a page: part of a name lists the matches,
// a number picks from that list, Enter keeps current.
func (s *Stick) pickLayout(ctx context.Context, current string, options []string) (string, error) {
	var shown []string
	for {
		var groups [][]calmmenu.Item
		if len(shown) > 0 {
			items := make([]calmmenu.Item, len(shown))
			for i, v := range shown {
				items[i] = calmmenu.Item{Key: strconv.Itoa(i + 1), Label: v}
			}
			groups = [][]calmmenu.Item{items}
		}
		value, err := s.ask(ctx, calmmenu.Menu{
			Title:  "Keyboard layout",
			Body:   []string{"Type a keyboard layout such as us, gb or fr. Part of a name shows a list."},
			Groups: groups,
			Prompt: fmt.Sprintf("Keyboard layout [%s]: ", current),
		})
		if err != nil {
			return "", err
		}
		if value == "" {
			value = current
		}
		if n, e := strconv.Atoi(value); e == nil && n > 0 && n <= len(shown) {
			return shown[n-1], nil
		}
		matches := prompt.Matches(options, value)
		switch {
		case len(matches) == 1:
			return matches[0], nil
		case len(matches) == 0:
			shown = nil
			s.note = fmt.Sprintf("Nothing matches %q. Type part of a name, such as a city or country code.", value)
		default:
			shown = matches[:min(len(matches), prompt.PickShown)]
			if len(matches) > len(shown) {
				s.note = fmt.Sprintf("... %d more; type more letters to narrow it down.", len(matches)-len(shown))
			}
		}
	}
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
		s.note = fmt.Sprintf("The console keeps its old layout. The installed system gets %s.", layout)
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

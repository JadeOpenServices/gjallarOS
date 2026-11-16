package stick

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/region"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickkeyboard"
)

// LogDir is the RAM folder for the menu's logs. systemd-tmpfiles creates
// it root-only before the menu starts; installlog refuses it otherwise.
const LogDir = "/run/gjallar/stick/logs"

// secureBootVar is the firmware's SecureBoot variable: four attribute
// bytes, then 1 when Secure Boot is on.
const secureBootVar = "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

const usage = `usage:
  gjallar-stick menu --mode window|console
  gjallar-stick layout
`

// Main runs gjallar-stick and returns its exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "layout" {
		// The display host reads the layout for its window from here.
		layout, err := stickkeyboard.Read()
		if err != nil {
			layout = region.DefaultKeyboard
		}
		fmt.Fprintln(stdout, layout)
		return 0
	}
	if len(args) != 3 || args[0] != "menu" || args[1] != "--mode" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	mode := args[2]
	if mode != ModeWindow && mode != ModeConsole {
		fmt.Fprint(stderr, usage)
		return 2
	}

	// Ctrl-C, Ctrl-\ and Ctrl-Z must not end the menu. Caught, not
	// ignored: programs it starts get them back.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)
	go func() {
		for range signals {
		}
	}()

	s := New(mode)
	first := firstStart(LogDir)
	if l, err := installlog.OpenStick(LogDir, time.Now()); err != nil {
		fmt.Fprintln(stderr, "No log this run:", err)
	} else {
		installlog.SetCurrent(l)
		s.ErrorLog = l.Tee(io.Discard)
		defer l.Close()
	}
	if first {
		installlog.Mark(installlog.StickBootStarted, installlog.Bool("secureboot", secureBootOn(secureBootVar)))
	}

	err := s.Run(context.Background())
	switch {
	case errors.Is(err, ErrRestartDisplay):
		return ExitRestart
	case err != nil:
		installlog.Mark(installlog.StickFlowFailed)
		s.logError("the menu stopped: cannot read the keyboard")
		fmt.Fprintln(stderr, "The menu stopped:", err)
		return 1
	}
	return 0
}

// firstStart reports whether no menu log exists yet this boot.
func firstStart(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

func secureBootOn(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && len(data) == 5 && bytes.Equal(data[4:], []byte{1})
}

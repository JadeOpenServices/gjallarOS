package installlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/termout"
)

// logged are the commands whose output Run copies into the log. Their
// output is build progress and errors; commands that handle secrets
// (chpasswd, age-keygen and the like) never go through Run.
var logged = map[string]bool{
	"nixos-install": true,
	"nixos-rebuild": true,
	"nix-channel":   true,
}

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
		cmd.Stdin = stdin
		return termout.AttachTo(cmd, stdout, stderr)
	}
	tee := teeFor(log, stderr)
	if term, ok := termout.TerminalFile(tee.Terminal()); ok {
		err := termout.RunInPTY(cmd, term, tee)
		if !errors.Is(err, termout.ErrNoPTY) {
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

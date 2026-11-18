package move

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

// Modes of the move, as the stick menu offers them (350).
const (
	// NewDisk moves the system to a new disk in this computer.
	NewDisk = "new-disk"
	// BringOver brings an old disk's system to this computer.
	BringOver = "bring-over"
)

// keyTries is how often the old disk's key is asked before the move stops.
const keyTries = 3

// maxKey is the longest key the helper takes (movehelper).
const maxKey = 1024

// ErrStopped means the move stopped and the screen already said why: the
// old disk was refused, the key was wrong too often, or the user went back.
var ErrStopped = errors.New("the move stopped")

// Flow runs the screens of the disk move.
type Flow struct {
	UI prompt.UI
	// Term is the terminal the key is typed on.
	Term   *os.File
	Helper Helper
	Mode   string
}

// Old is the opened old disk.
type Old struct {
	Disk oldroot.Part
	Info oldroot.Info
	// PINUnchecked: the old disk has a startup PIN that could not be
	// checked, and the user went on. Identity items then start as "make
	// new" (1133, open question 1).
	PINUnchecked bool
}

// OpenOld runs steps 2 to 5: pick the old disk, check it, unlock it and
// show what is on it. On success the old disk is open read-only; Close
// closes it.
func (f Flow) OpenOld(ctx context.Context) (*Old, error) {
	switch f.Mode {
	case NewDisk:
		installlog.Mark(installlog.StickMoveStarted, installlog.Choice("mode", "new-disk"))
	case BringOver:
		installlog.Mark(installlog.StickMoveStarted, installlog.Choice("mode", "bring-over"))
	default:
		return nil, fmt.Errorf("unknown move mode %q", f.Mode)
	}
	disk, err := f.pickOld(ctx)
	if err != nil {
		return nil, err
	}
	f.say("Checking the old disk...")
	var check movehelper.CheckResult
	if err := f.Helper.Run(ctx, movehelper.Request{Op: "check", Disk: disk.Disk}, &check); err != nil {
		return nil, f.refused(err)
	}
	if check.Encrypted != disk.Encrypted {
		return nil, errors.New("the old disk changed while it was checked; restart from the stick")
	}
	res, err := f.open(ctx, disk)
	if err != nil {
		return nil, err
	}
	old := &Old{Disk: disk, Info: res.Info}
	f.say(fmt.Sprintf("Found gjallarOS (%s channel, last updated %s).",
		res.Channel, res.LastUpdated.UTC().Format("2 January 2006")))
	// The record of "gjallarctl move prepare" is inside the encrypted
	// root, so the PIN notice can only come after the unlock.
	if res.PINToken {
		goOn, err := f.pinNotice(ctx)
		if err != nil || !goOn {
			if cerr := f.Close(context.WithoutCancel(ctx)); cerr != nil {
				return nil, errors.Join(err, cerr)
			}
			if err != nil {
				return nil, err
			}
			return nil, ErrStopped
		}
		installlog.Mark(installlog.StickMovePINUncheckedYes)
		old.PINUnchecked = true
	}
	return old, nil
}

// Close closes the old disk.
func (f Flow) Close(ctx context.Context) error {
	return f.Helper.Run(ctx, movehelper.Request{Op: "close"}, nil)
}

// pickOld is step 2.
func (f Flow) pickOld(ctx context.Context) (oldroot.Part, error) {
	for {
		var found movehelper.FindResult
		if err := f.Helper.Run(ctx, movehelper.Request{Op: "find"}, &found); err != nil {
			return oldroot.Part{}, f.refused(err)
		}
		switch len(found.Disks) {
		case 0:
			f.say("No disk with gjallarOS was found. If the old disk is not connected, connect it now.")
			again, err := f.UI.Confirm(ctx, "Look again?", true)
			if err != nil {
				return oldroot.Part{}, err
			}
			if !again {
				return oldroot.Part{}, ErrStopped
			}
			continue
		case 1:
			d := found.Disks[0]
			ok, err := f.UI.Confirm(ctx, "Your old gjallarOS is on "+diskLine(d)+". Use this disk?", true)
			if err != nil {
				return oldroot.Part{}, err
			}
			if !ok {
				return oldroot.Part{}, ErrStopped
			}
			return d, nil
		}
		f.say("Which disk has your old gjallarOS?")
		lines := make([]string, len(found.Disks))
		for i, d := range found.Disks {
			lines[i] = diskLine(d)
		}
		n, err := f.choose(ctx, lines, "")
		if err != nil {
			return oldroot.Part{}, err
		}
		return found.Disks[n], nil
	}
}

// choose lists options by number, as the stick menus do, and returns the
// index of the one picked. Enter takes def when it is not empty.
func (f Flow) choose(ctx context.Context, options []string, def string) (int, error) {
	for i, o := range options {
		fmt.Fprintf(f.UI.Out, "  %d) %s\n", i+1, o)
	}
	fmt.Fprintln(f.UI.Out, "Type a number and press Enter.")
	for {
		line, err := f.UI.Value(ctx, "Choice", def)
		if err != nil {
			return 0, err
		}
		if def != "" && line == def {
			if i := slices.Index(options, def); i >= 0 {
				return i, nil
			}
		}
		n, err := strconv.Atoi(line)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		fmt.Fprintf(f.UI.Out, "Type a number from 1 to %d.\n", len(options))
	}
}

// open is steps 4 and 5: the key, then the read-only mount and the
// gjallarOS check inside the helper.
func (f Flow) open(ctx context.Context, disk oldroot.Part) (movehelper.OpenResult, error) {
	var res movehelper.OpenResult
	req := movehelper.Request{Op: "open", Disk: disk.Disk}
	if !disk.Encrypted {
		if err := f.Helper.Run(ctx, req, &res); err != nil {
			return res, f.refused(err)
		}
		return res, nil
	}
	tries := 0
	for tries < keyTries {
		line, err := f.UI.Secret(ctx, "Recovery key or disk passphrase of the old disk", f.Term)
		if err != nil {
			return res, err
		}
		if line == "" {
			continue
		}
		if len(line) > maxKey {
			f.say("That is longer than any key or passphrase gjallarOS sets up.")
			continue
		}
		tries++
		key := []byte(line)
		req.Key = key
		err = f.Helper.Run(ctx, req, &res)
		clear(key)
		req.Key = nil
		if r, ok := oldroot.AsRefusal(err); ok && r.Reason == oldroot.WrongKey {
			left := keyTries - tries
			switch left {
			case 0:
				f.say(r.Text)
			case 1:
				f.say(r.Text + " One try left.")
			default:
				f.say(fmt.Sprintf("%s %d tries left.", r.Text, left))
			}
			continue
		}
		if err != nil {
			return res, f.refused(err)
		}
		installlog.Mark(installlog.StickMoveUnlockOK, installlog.Count("tries", tries))
		return res, nil
	}
	installlog.Mark(installlog.StickMoveUnlockFailed, installlog.Count("tries", tries))
	f.say("The old disk stays locked. The recovery key was shown at the first startup of the old computer, so look where you wrote it down. If the old computer still starts, gjallarctl luks recovery-key makes a new one there. Start the move again when you have the key or passphrase.")
	return res, ErrStopped
}

// pinNotice is the warning when the old disk has a startup PIN (F1). It
// tells whether the user goes on.
func (f Flow) pinNotice(ctx context.Context) (bool, error) {
	f.say("The old computer also asks for a PIN at startup. The stick cannot check that PIN. Only the chip in the old computer can, and too many wrong tries there can lock it.\n\n" +
		"If you go on, the new disk gets new VPN keys, a new Tailscale login, new SSH host keys and a new machine ID. You can choose to keep each one later in the move.\n\n" +
		"To keep them as they are, go back, start the old computer and run gjallarctl move prepare. It checks the PIN there.")
	choice, err := f.choose(ctx, []string{"Go back", "Continue"}, "Go back")
	if err != nil {
		return false, err
	}
	return choice == 1, nil
}

// refused shows a refusal and marks it; other errors pass through.
func (f Flow) refused(err error) error {
	r, ok := oldroot.AsRefusal(err)
	if !ok {
		return err
	}
	if err := markRefusal(r.Reason); err != nil {
		return err
	}
	f.say(r.Text)
	return ErrStopped
}

// markRefusal writes the anchor for a refusal. Each choice is a literal so
// the anchor test can check it against the registry.
func markRefusal(reason oldroot.Reason) error {
	switch reason {
	case oldroot.Reencrypt:
		installlog.Mark(installlog.StickMoveHeaderRefused, installlog.Choice("reason", "reencrypt"))
	case oldroot.UnknownToken:
		installlog.Mark(installlog.StickMoveHeaderRefused, installlog.Choice("reason", "unknown-token"))
	case oldroot.KDFMemory:
		installlog.Mark(installlog.StickMoveHeaderRefused, installlog.Choice("reason", "kdf-memory"))
	case oldroot.NotLUKS2:
		installlog.Mark(installlog.StickMoveHeaderRefused, installlog.Choice("reason", "not-luks2"))
	case oldroot.Unsupported:
		installlog.Mark(installlog.StickMoveHeaderRefused, installlog.Choice("reason", "unsupported"))
	case oldroot.NotBtrfs:
		installlog.Mark(installlog.StickMoveLayoutRefused, installlog.Choice("reason", "not-btrfs"))
	case oldroot.NoEvidence:
		installlog.Mark(installlog.StickMoveLayoutRefused, installlog.Choice("reason", "no-evidence"))
	case oldroot.Managed:
		installlog.Mark(installlog.StickMoveLayoutRefused, installlog.Choice("reason", "managed"))
	case oldroot.Busy:
		installlog.Mark(installlog.StickMoveOldRefused, installlog.Choice("reason", "busy"))
	case oldroot.OldCryptsetup:
		installlog.Mark(installlog.StickMoveOldRefused, installlog.Choice("reason", "old-cryptsetup"))
	default:
		return fmt.Errorf("the move helper refused with an unknown reason %q", reason)
	}
	return nil
}

// diskLine names a disk on the screen: model, size, and whether it is
// connected from outside.
func diskLine(d oldroot.Part) string {
	parts := []string{d.Disk}
	if d.Model != "" {
		parts = append(parts, d.Model)
	}
	parts = append(parts, size(d.Size))
	if !d.Internal {
		parts = append(parts, "external")
	}
	return strings.Join(parts, ", ")
}

// size is a disk size in decimal units, as disk makers print them.
func size(bytes int64) string {
	const unit = 1000
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value, suffix := float64(bytes), ""
	for _, s := range []string{"kB", "MB", "GB", "TB"} {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}

func (f Flow) say(text string) {
	if f.UI.Wrap {
		text = calmmenu.Wrap(text)
	}
	fmt.Fprintln(f.UI.Out, text)
}

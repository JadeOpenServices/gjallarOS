package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// Reset, Repair and Undo (Plane 277, wiki 931). Reset and Repair keep the
// app's prefix and record as a snapshot first, so Undo can put them back.
// The files folder (D:) is never touched by any of them.

// doctorApp takes app id's lock and loads it, for the three commands.
func doctorApp(args []string) (string, winesandbox.App, winesandbox.Paths, *os.File, error) {
	if len(args) != 1 {
		return "", winesandbox.App{}, winesandbox.Paths{}, nil, errUsage
	}
	id := args[0]
	_, home, err := currentUser()
	if err != nil {
		return "", winesandbox.App{}, winesandbox.Paths{}, nil, err
	}
	lock, err := lockApp(id)
	if err != nil {
		return "", winesandbox.App{}, winesandbox.Paths{}, nil, err
	}
	a, paths, err := winesandbox.LoadApp(home, id)
	if err != nil {
		lock.Close()
		return "", winesandbox.App{}, winesandbox.Paths{}, nil, err
	}
	return id, a, paths, lock, nil
}

// resetCmd gives app id a new prefix from the base prefix. Programs
// installed on C: and their settings go; the files folder stays. The old
// prefix moves into a snapshot without copying.
func resetCmd(args []string, stdout, stderr io.Writer) error {
	id, a, paths, lock, err := doctorApp(args)
	if err != nil {
		return err
	}
	defer lock.Close()
	userName, home, err := currentUser()
	if err != nil {
		return err
	}
	if err := winesandbox.RealDir(paths.Prefix); err != nil {
		return err
	}
	// The base prefix is made first: if that fails, nothing has changed.
	base, err := ensureBase(home, stderr)
	if err != nil {
		return err
	}
	snap, err := winesandbox.TakeSnapshot(paths, id, a, nil)
	if err != nil {
		return fmt.Errorf("keep the app's state: %w", err)
	}
	if err := copyPrefix(base, paths, id, userName, stderr); err != nil {
		if _, rerr := winesandbox.Restore(paths, id, snap); rerr != nil {
			return fmt.Errorf("%w; putting the old state back failed too: %v", err, rerr)
		}
		return err
	}
	if a.Exe != "" {
		if checkProgram(paths, a.Exe) != nil {
			fmt.Fprintf(stdout, "App %s no longer has %s; pick its program again after installing it.\n", id, a.Exe)
			a.Exe = ""
		}
	}
	if err := saveApp(paths, id, a, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Reset app %s. Its files folder %s stays as it was.\n", id, paths.Files)
	fmt.Fprintf(stdout, "gjallar-wine undo %s puts the old state back.\n", id)
	return nil
}

// repairCmd sets app id's prefix up again in place: links that lead out
// go, the drives and user folders are set anew, and Wine brings the
// prefix up to date. Installed programs stay.
func repairCmd(args []string, stdout, stderr io.Writer) error {
	id, a, paths, lock, err := doctorApp(args)
	if err != nil {
		return err
	}
	defer lock.Close()
	userName, _, err := currentUser()
	if err != nil {
		return err
	}
	if err := winesandbox.RealDir(paths.Prefix); err != nil {
		return err
	}
	if _, err := winesandbox.TakeSnapshot(paths, id, a, func(dst string) error {
		cp := exec.Command(cpPath, "-a", "--reflink=auto", "--", paths.Prefix, dst)
		cp.Stderr = stderr
		return cp.Run()
	}); err != nil {
		return fmt.Errorf("keep the app's state: %w", err)
	}
	undo := fmt.Sprintf("gjallar-wine undo %s puts the state from before the repair back", id)
	if err := dropOutwardLinks(paths.Prefix); err != nil {
		return fmt.Errorf("repair the app's prefix: %w; %s", err, undo)
	}
	if err := winesandbox.FixPrefix(paths.Prefix, paths.Files, userName); err != nil {
		return fmt.Errorf("repair the app's prefix: %w; %s", err, undo)
	}
	fmt.Fprintf(stdout, "Bringing app %s's Wine setup up to date.\n", id)
	code, err := launchApp(id, []string{"wineboot", "-u"}, nil, stderr)
	if err != nil {
		return fmt.Errorf("update the app's Wine setup: %w; %s", err, undo)
	}
	if code != 0 {
		return fmt.Errorf("Wine's update ended with code %d; %s", code, undo)
	}
	if err := saveApp(paths, id, a, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Repaired app %s. %s.\n", id, undo)
	return nil
}

// undoCmd puts back the newest snapshot of app id.
func undoCmd(args []string, stdout, stderr io.Writer) error {
	id, _, paths, lock, err := doctorApp(args)
	if err != nil {
		return err
	}
	defer lock.Close()
	list, err := winesandbox.Snapshots(paths.Home, id)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("app %s has nothing to undo", id)
	}
	a, err := winesandbox.Restore(paths, id, list[0])
	if err != nil {
		return err
	}
	if err := saveApp(paths, id, a, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "App %s is back as it was at %s.", id, list[0].Taken.Local().Format("2006-01-02 15:04:05"))
	if n := len(list) - 1; n > 0 {
		fmt.Fprintf(stdout, " %d older state(s) left to undo.", n)
	}
	fmt.Fprintln(stdout)
	return nil
}

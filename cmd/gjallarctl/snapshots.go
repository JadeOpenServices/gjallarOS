package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/JadeOpenServices/gjallarOS/internal/snapshots"
)

// gjallarctl snapshots is the front end of gjallar-snapshotd (ROADMAP
// section 15, step 3). The service checks the request, asks the password
// and opens the old file; the copy is made here, as the user, under a new
// name, so nothing is overwritten.

const snapshotsUsage = `usage: gjallarctl snapshots list [--names]       snapshots of /home, newest first
       gjallarctl snapshots restore PATH [--from SNAPSHOT] [--to FOLDER]
                                                copy your file PATH back from the
                                                newest snapshot that has it
The copy goes next to PATH, or into FOLDER, with ".restored-" and the
snapshot time in its name. Only your own files in your home can be
restored, and each restore asks your password.`

// snapshotsCallTimeout leaves room for the password dialog.
const snapshotsCallTimeout = 6 * time.Minute

var errSnapshotsMissing = errors.New("there are no snapshots on this system; they come with new installs (ROADMAP section 15)")

type snapshotsBus interface {
	List(ctx context.Context) ([]string, error)
	// Restore returns the old file, read-only, and the snapshot it is from.
	Restore(ctx context.Context, path, from string) (*os.File, string, error)
	Close() error
}

var snapshotsConnect = func() (snapshotsBus, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return systemSnapshotsBus{conn}, nil
}

func runSnapshots(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, snapshotsUsage)
		return 2
	}
	command, args := args[0], args[1:]
	var path, from, to string
	namesOnly := false
	switch command {
	case "list":
		if len(args) == 1 && args[0] == "--names" {
			namesOnly, args = true, nil
		}
		if len(args) != 0 {
			fmt.Fprintln(stderr, snapshotsUsage)
			return 2
		}
	case "restore":
		for len(args) > 0 {
			switch {
			case args[0] == "--from" && len(args) > 1:
				from, args = args[1], args[2:]
			case args[0] == "--to" && len(args) > 1:
				to, args = args[1], args[2:]
			case path == "" && args[0] != "" && args[0][0] != '-':
				path, args = args[0], args[1:]
			default:
				fmt.Fprintln(stderr, snapshotsUsage)
				return 2
			}
		}
		if path == "" {
			fmt.Fprintln(stderr, snapshotsUsage)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "ERROR: unknown snapshots command %q\n%s\n", command, snapshotsUsage)
		return 2
	}
	if settingsEUID() == 0 {
		fmt.Fprintln(stderr, "ERROR: run gjallarctl snapshots as your own user; you can restore only your own files")
		return 1
	}
	bus, err := snapshotsConnect()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: system bus: %v\n", err)
		return 1
	}
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), snapshotsCallTimeout)
	defer cancel()
	if command == "list" {
		err = snapshotsList(ctx, bus, namesOnly, stdout)
	} else {
		err = snapshotsRestore(ctx, bus, path, from, to, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

func snapshotsList(ctx context.Context, bus snapshotsBus, namesOnly bool, stdout io.Writer) error {
	names, err := bus.List(ctx)
	if err != nil {
		return err
	}
	if namesOnly {
		for _, name := range names {
			fmt.Fprintln(stdout, name)
		}
		return nil
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "No snapshots yet; the first one is made within the hour.")
		return nil
	}
	for _, name := range names {
		fmt.Fprintf(stdout, "%s  %s\n", name, snapshotTime(name))
	}
	return nil
}

func snapshotTime(name string) string {
	at, err := snapshots.Time(name)
	if err != nil {
		return ""
	}
	return at.Local().Format("2006-01-02 15:04")
}

// snapshotsRestore asks for the old file and copies it under a new name.
func snapshotsRestore(ctx context.Context, bus snapshotsBus, path, from, to string, stdout io.Writer) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// The snapshot has no links the service follows, so a folder reached
	// through a link here is asked for by its real path.
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(dir, filepath.Base(abs))
	}
	dest := filepath.Dir(abs)
	if to != "" {
		if dest, err = filepath.Abs(to); err != nil {
			return err
		}
	}
	if info, err := os.Stat(dest); err != nil || !info.IsDir() {
		return fmt.Errorf("the folder %s is not there; give one with --to FOLDER", dest)
	}
	// With --from the new name is known now; say so before the password.
	if from != "" {
		target := filepath.Join(dest, snapshots.RestoredName(filepath.Base(abs), from))
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("%s is already there; nothing was changed", target)
		}
	}

	old, snapshot, err := bus.Restore(ctx, abs, from)
	if err != nil {
		return err
	}
	defer old.Close()
	info, err := old.Stat()
	if err != nil {
		return err
	}
	target := filepath.Join(dest, snapshots.RestoredName(filepath.Base(abs), snapshot))
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, info.Mode().Perm())
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s is already there; nothing was changed", target)
	}
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, old); err != nil {
		out.Close()
		os.Remove(target)
		return fmt.Errorf("copy the file: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(target)
		return err
	}
	fmt.Fprintf(stdout, "Restored %s from the snapshot of %s as %s\n", abs, snapshotTime(snapshot), target)
	return nil
}

type systemSnapshotsBus struct{ conn *dbus.Conn }

func (b systemSnapshotsBus) Close() error { return b.conn.Close() }

func (b systemSnapshotsBus) call(ctx context.Context, method string, args ...any) (*dbus.Call, error) {
	call := b.conn.Object(snapshots.BusName, dbus.ObjectPath(snapshots.ObjectPath)).
		CallWithContext(ctx, snapshots.Interface+"."+method, 0, args...)
	if call.Err != nil {
		var busErr dbus.Error
		if errors.As(call.Err, &busErr) {
			if busErr.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
				return nil, errSnapshotsMissing
			}
			if len(busErr.Body) > 0 {
				if text, ok := busErr.Body[0].(string); ok {
					return nil, errors.New(text)
				}
			}
		}
		return nil, call.Err
	}
	return call, nil
}

func (b systemSnapshotsBus) List(ctx context.Context) ([]string, error) {
	call, err := b.call(ctx, "List")
	if err != nil {
		return nil, err
	}
	var answer string
	if err := call.Store(&answer); err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal([]byte(answer), &names); err != nil {
		return nil, err
	}
	return names, nil
}

func (b systemSnapshotsBus) Restore(ctx context.Context, path, from string) (*os.File, string, error) {
	if !b.conn.SupportsUnixFDs() {
		return nil, "", errors.New("the system bus does not pass files")
	}
	call, err := b.call(ctx, "Restore", path, from)
	if err != nil {
		return nil, "", err
	}
	var fd dbus.UnixFD
	var snapshot string
	if err := call.Store(&fd, &snapshot); err != nil {
		return nil, "", err
	}
	return os.NewFile(uintptr(fd), path), snapshot, nil
}

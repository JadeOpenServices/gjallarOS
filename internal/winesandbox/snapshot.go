package winesandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

// Before Reset and Repair change an app's prefix, the prefix and the app's
// record are kept as a snapshot, so Undo can put them back (Plane 277,
// wiki 931). A snapshot is a folder below the app's snapshots folder,
// named by the time it was taken, with the prefix and the record in it.
// Only the app's own snapshots are kept, and only the newest few.

// KeepSnapshots is how many snapshots an app keeps; the oldest goes first.
const KeepSnapshots = 3

// snapshotName matches the folder names Snapshot gives.
var snapshotName = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z$`)

const snapshotTime = "20060102T150405.000000000Z"

// SnapshotsDir is the folder that holds app id's snapshots.
func SnapshotsDir(home, id string) string {
	return filepath.Join(home, ".local", "share", "gjallar-wine", "snapshots", id)
}

// Snapshot is one kept state of an app.
type Snapshot struct {
	Dir   string
	Taken time.Time
}

// prefixIn and recordIn are the parts of snapshot dir.
func prefixIn(dir string) string { return filepath.Join(dir, "prefix") }
func recordIn(dir string) string { return filepath.Join(dir, "app.json") }

// snapshotsDir makes app id's snapshots folder and checks it and the one
// above are folders without links.
func snapshotsDir(home, id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%q is not an app id", id)
	}
	dir := SnapshotsDir(home, id)
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := RealDir(d); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// TakeSnapshot keeps app a's record and its prefix. With fill nil, the
// app's prefix itself moves into the snapshot (Reset, which builds a new
// one), and moves back if a later step fails. Otherwise fill copies the
// prefix into the folder it is given, which does not exist yet (Repair).
// The snapshot is built under a hidden name and only counts once
// complete. Then the oldest snapshots past KeepSnapshots go.
func TakeSnapshot(paths Paths, id string, a App, fill func(prefix string) error) (Snapshot, error) {
	dir, err := snapshotsDir(paths.Home, id)
	if err != nil {
		return Snapshot{}, err
	}
	taken := time.Now().UTC()
	final := filepath.Join(dir, taken.Format(snapshotTime))
	tmp := filepath.Join(dir, "."+filepath.Base(final))
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return Snapshot{}, err
	}
	moved := false
	fail := func(err error) (Snapshot, error) {
		if moved {
			// The app's only prefix is in tmp: it goes back, not away.
			if rerr := os.Rename(prefixIn(tmp), paths.Prefix); rerr != nil {
				return Snapshot{}, fmt.Errorf("%w; moving the app's prefix back failed too, it is in %s: %v", err, prefixIn(tmp), rerr)
			}
		}
		os.RemoveAll(tmp)
		return Snapshot{}, err
	}
	if err := WriteRecord(Paths{Record: recordIn(tmp)}, a); err != nil {
		return fail(err)
	}
	if fill == nil {
		if err := os.Rename(paths.Prefix, prefixIn(tmp)); err != nil {
			return fail(err)
		}
		moved = true
	} else if err := fill(prefixIn(tmp)); err != nil {
		return fail(err)
	}
	if err := RealDir(prefixIn(tmp)); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fail(err)
	}
	s := Snapshot{Dir: final, Taken: taken}
	return s, prune(dir)
}

// Snapshots lists app id's complete snapshots, newest first.
func Snapshots(home, id string) ([]Snapshot, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%q is not an app id", id)
	}
	dir := SnapshotsDir(home, id)
	if err := RealDir(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var list []Snapshot
	for _, entry := range entries {
		if !entry.IsDir() || !snapshotName.MatchString(entry.Name()) {
			continue
		}
		taken, err := time.Parse(snapshotTime, entry.Name())
		if err != nil {
			continue
		}
		list = append(list, Snapshot{Dir: filepath.Join(dir, entry.Name()), Taken: taken})
	}
	slices.SortFunc(list, func(a, b Snapshot) int { return b.Taken.Compare(a.Taken) })
	return list, nil
}

// prune removes the snapshots past KeepSnapshots and any left half done.
func prune(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		switch {
		case snapshotName.MatchString(entry.Name()):
			names = append(names, entry.Name())
		case len(entry.Name()) > 1 && snapshotName.MatchString(entry.Name()[1:]):
			// Left by a run that died; the newest may be one that runs now,
			// but the app's lock keeps a second run out.
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	slices.Sort(names)
	for len(names) > KeepSnapshots {
		if err := os.RemoveAll(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// Restore puts snapshot s back as app id's prefix and record, and drops
// the snapshot. The prefix it replaces is removed. The record must name
// the same files folder, since the prefix's D: leads there.
func Restore(paths Paths, id string, s Snapshot) (App, error) {
	dir, err := snapshotsDir(paths.Home, id)
	if err != nil {
		return App{}, err
	}
	if filepath.Dir(s.Dir) != dir || !snapshotName.MatchString(filepath.Base(s.Dir)) {
		return App{}, fmt.Errorf("%s is not a snapshot of app %s", s.Dir, id)
	}
	a, err := readRecord(recordIn(s.Dir))
	if err != nil {
		return App{}, err
	}
	if PathsFor(paths.Home, id, a.Name).Files != paths.Files {
		return App{}, fmt.Errorf("the snapshot is of app %q, not of this one", a.Name)
	}
	if err := RealDir(prefixIn(s.Dir)); err != nil {
		return App{}, err
	}
	// The current prefix moves aside first, so a failed step leaves one
	// of the two in place.
	old := filepath.Join(dir, ".replaced")
	if err := os.RemoveAll(old); err != nil {
		return App{}, err
	}
	if err := os.Rename(paths.Prefix, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return App{}, err
	}
	if err := os.Rename(prefixIn(s.Dir), paths.Prefix); err != nil {
		if os.Rename(old, paths.Prefix) != nil {
			return App{}, fmt.Errorf("put the snapshot back: %w; the app's prefix is in %s", err, old)
		}
		return App{}, fmt.Errorf("put the snapshot back: %w", err)
	}
	if err := os.RemoveAll(old); err != nil {
		return App{}, err
	}
	if err := os.RemoveAll(s.Dir); err != nil {
		return App{}, err
	}
	return a, nil
}

// DropSnapshots removes all of app id's snapshots, when the app goes or
// when a new app takes its id.
func DropSnapshots(home, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not an app id", id)
	}
	dir := SnapshotsDir(home, id)
	switch err := RealDir(filepath.Dir(dir)); {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	return os.RemoveAll(dir)
}

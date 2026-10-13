// Package managedapps takes back what GjallarOS installed into an app once
// user.config.json no longer asks for it.
//
// Nix puts the wanted items in place, but apps such as Firefox keep what
// they once installed when it drops out of their policy. A ledger per app
// records what GjallarOS asked for, so a later run removes exactly the items
// that left the configuration and never touches what the user installed.
package managedapps

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// Backend knows how to take items out of one app.
type Backend interface {
	// Ledger names the ledger file, such as "firefox-default".
	Ledger() string
	// Busy reports that the app runs, so removals wait for the next run.
	Busy() (bool, error)
	// Remove takes out one item that GjallarOS installed. It returns false
	// and leaves the item alone when it is gone or the user installed it.
	Remove(id string) (bool, error)
}

// Result says what a run did.
type Result struct {
	Removed []string
	// Left were not installed by GjallarOS any more and stay as they are.
	Left []string
	// Failed stay in the ledger for the next run.
	Failed map[string]error
	Busy   bool
}

type ledger struct {
	Items []string `json:"items"`
}

var ledgerName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// StateDir is where the ledgers live: $XDG_STATE_HOME/gjallaros/managed.
func StateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "gjallaros", "managed"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "gjallaros", "managed"), nil
}

// Reconcile removes the items in the ledger that desired no longer lists
// and records desired as the new ledger.
func Reconcile(stateDir string, b Backend, desired []string) (Result, error) {
	name := b.Ledger()
	if !ledgerName.MatchString(name) {
		return Result{}, fmt.Errorf("bad ledger name %q", name)
	}
	path := filepath.Join(stateDir, name+".json")
	recorded, err := readLedger(path)
	if err != nil {
		return Result{}, err
	}

	var stale []string
	for _, id := range recorded {
		if !slices.Contains(desired, id) {
			stale = append(stale, id)
		}
	}
	keep := slices.Clone(desired)
	res := Result{Failed: map[string]error{}}
	if len(stale) > 0 {
		busy, err := b.Busy()
		if err != nil {
			return Result{}, err
		}
		res.Busy = busy
	}
	for _, id := range stale {
		if res.Busy {
			keep = append(keep, id)
			continue
		}
		removed, err := b.Remove(id)
		switch {
		case err != nil:
			res.Failed[id] = err
			keep = append(keep, id)
		case removed:
			res.Removed = append(res.Removed, id)
		default:
			res.Left = append(res.Left, id)
		}
	}
	slices.Sort(keep)
	if err := writeLedger(stateDir, path, slices.Compact(keep)); err != nil {
		return res, err
	}
	return res, nil
}

func readLedger(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	var l ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("read ledger %s: %w", path, err)
	}
	return l.Items, nil
}

func writeLedger(dir, path string, items []string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	if items == nil {
		items = []string{}
	}
	data, err := json.MarshalIndent(ledger{Items: items}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ledger-*")
	if err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	return nil
}

// ReadDesired reads the items the build asks for: {"items": [...]}.
func ReadDesired(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read wanted items: %w", err)
	}
	var l ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("read wanted items %s: %w", path, err)
	}
	return l.Items, nil
}

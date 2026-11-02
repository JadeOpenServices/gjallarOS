// Package devgate keeps developer channels and developer mode behind a
// second proof (Plane 283). A password alone may not move a device to the
// dev or staging channel or turn on developer mode: the person also types
// the startup PIN or the recovery key. The settings service then writes a
// grant that only root can read or change, and the root rebuild refuses
// those settings without one.
package devgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// GrantPath is the grant the settings service writes (its StateDirectory,
// mode 0700) and the root rebuild reads.
const GrantPath = "/var/lib/gjallarOS/settings/developer-grant.json"

// Ways a grant was proven.
const (
	ProofPIN      = "pin"
	ProofRecovery = "recovery"
)

// Grant records what the proof unlocked. Channels covers every channel
// other than stable; DeveloperMode the local folder root builds from.
type Grant struct {
	Channels      bool      `json:"channels"`
	DeveloperMode bool      `json:"developerMode"`
	Proof         string    `json:"proof"`
	Time          time.Time `json:"time"`
}

// Needs reports what a configuration asks of the grant.
func Needs(channel string, developerMode bool) Grant {
	return Grant{Channels: channel != "" && channel != "stable", DeveloperMode: developerMode}
}

// Covers reports whether g allows everything want asks for.
func (g Grant) Covers(want Grant) bool {
	return (g.Channels || !want.Channels) && (g.DeveloperMode || !want.DeveloperMode)
}

// Empty reports a grant that allows nothing.
func (g Grant) Empty() bool {
	return !g.Channels && !g.DeveloperMode
}

// Load reads the grant at path. A missing file is an empty grant.
func Load(path string) (Grant, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Grant{}, nil
	}
	if err != nil {
		return Grant{}, fmt.Errorf("read the developer grant: %w", err)
	}
	var g Grant
	if err := json.Unmarshal(raw, &g); err != nil {
		return Grant{}, fmt.Errorf("read the developer grant: %w", err)
	}
	return g, nil
}

// Save replaces the grant at path, or removes it when g allows nothing.
// The file is written next to the old one and renamed over it, so a
// crash leaves the old grant or the new one.
func Save(path string, g Grant) error {
	if g.Empty() {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the developer grant: %w", err)
		}
		return nil
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".developer-grant-*")
	if err != nil {
		return fmt.Errorf("write the developer grant: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("write the developer grant: %w", err)
	}
	return nil
}

// Check refuses a configuration the grant at path does not cover. The
// root rebuild calls it before it pulls a channel or reads the local
// folder.
func Check(path, channel string, developerMode bool) error {
	want := Needs(channel, developerMode)
	if want.Empty() {
		return nil
	}
	g, err := Load(path)
	if err != nil {
		return err
	}
	if g.Covers(want) {
		return nil
	}
	what := "the " + channel + " channel"
	switch {
	case want.DeveloperMode && !g.DeveloperMode && want.Channels && !g.Channels:
		what += " and developer mode"
	case want.DeveloperMode && !g.DeveloperMode:
		what = "developer mode"
	}
	return fmt.Errorf("%s needs the startup PIN or the recovery key once; run gjallarctl settings confirm-developer, or switch back with gjallarctl settings set releaseChannel=stable developerMode=false", what)
}

// Narrow drops from the grant at path whatever channel and developerMode
// no longer ask for, so turning a setting back on needs the proof again.
// A grant that does not load is removed. It reports whether the file
// changed.
func Narrow(path, channel string, developerMode bool) (bool, error) {
	old, err := Load(path)
	if err != nil {
		return true, Save(path, Grant{})
	}
	need := Needs(channel, developerMode)
	g := old
	g.Channels = g.Channels && need.Channels
	g.DeveloperMode = g.DeveloperMode && need.DeveloperMode
	if g == old {
		return false, nil
	}
	return true, Save(path, g)
}

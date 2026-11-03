package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// Network switches of sandboxed Wine apps (Plane 272). Each user's
// switches are one root-only file in wineDir, so a program that breaks
// out into the user's account still cannot turn its own network on.
// Turning a switch on asks an administrator's password; turning one off
// applies at once. The launcher listens for WineChanged and reads the
// switches again.

const wineDir = "/var/lib/gjallarOS/settings/wine"

const wineAction = "org.gjallaros.wine.network"

// maxWinePrograms bounds one user's file.
const maxWinePrograms = 256

type wineRecord struct {
	UID      uint32                       `json:"uid"`
	User     string                       `json:"user"`
	Programs map[string]winesandbox.Flags `json:"programs"`
}

func (s *service) winePath(uid uint32) string {
	return filepath.Join(s.wineDir, strconv.FormatUint(uint64(uid), 10)+".json")
}

// wineFlags reads uid's switches. A file left by a removed user, or by an
// earlier account on this uid, is deleted and counts for no one.
func (s *service) wineFlags(uid uint32, name string) (map[string]winesandbox.Flags, error) {
	path := s.winePath(uid)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]winesandbox.Flags{}, nil
	}
	if err != nil {
		return nil, err
	}
	var record wineRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if record.UID != uid {
		return nil, fmt.Errorf("%s belongs to uid %d", path, record.UID)
	}
	if record.User != name {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		log.Printf("wine: deleted the network switches of removed user %s (uid %d)", record.User, uid)
		return map[string]winesandbox.Flags{}, nil
	}
	if record.Programs == nil {
		record.Programs = map[string]winesandbox.Flags{}
	}
	return record.Programs, nil
}

func (s *service) saveWine(uid uint32, name string, programs map[string]winesandbox.Flags) error {
	maps.DeleteFunc(programs, func(_ string, f winesandbox.Flags) bool { return f == winesandbox.Flags{} })
	if len(programs) == 0 {
		if err := os.Remove(s.winePath(uid)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(wineRecord{UID: uid, User: name, Programs: programs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.wineDir, 0o700); err != nil {
		return err
	}
	return writeRoot(s.winePath(uid), append(data, '\n'))
}

// Wine returns the caller's switches by program id. Programs not listed
// have both off.
func (s *service) Wine(ctx context.Context, sender string) (map[string]winesandbox.Flags, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nil, err
	}
	name, err := s.userName(who.UID)
	if err != nil {
		return nil, err
	}
	s.wineMu.Lock()
	defer s.wineMu.Unlock()
	return s.wineFlags(who.UID, name)
}

// SetWine sets the caller's switches for program id. A switch that goes
// on asks the password first.
func (s *service) SetWine(ctx context.Context, sender, id string, want winesandbox.Flags) (map[string]winesandbox.Flags, error) {
	if !winesandbox.ValidID(id) {
		return nil, fmt.Errorf("%w: %q is not a program id", errRefused, id)
	}
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nil, err
	}
	name, err := s.userName(who.UID)
	if err != nil {
		return nil, err
	}
	s.wineMu.Lock()
	programs, err := s.wineFlags(who.UID, name)
	s.wineMu.Unlock()
	if err != nil {
		return nil, err
	}
	have := programs[id]
	if want == have {
		return programs, nil
	}
	if err := wineRoom(programs, id, want); err != nil {
		return nil, err
	}
	if on := wineOpens(have, want); on != "" {
		if err := s.sys.Authorize(ctx, sender, wineAction, map[string]string{
			"polkit.message":             fmt.Sprintf("Let the Wine app %q reach %s", id, on),
			"org.gjallaros.wine.program": id,
		}); err != nil {
			return nil, err
		}
	}

	// Read again: the file may have changed while the dialog was open.
	s.wineMu.Lock()
	defer s.wineMu.Unlock()
	if programs, err = s.wineFlags(who.UID, name); err != nil {
		return nil, err
	}
	if err := wineRoom(programs, id, want); err != nil {
		return nil, err
	}
	programs[id] = want
	if err := s.saveWine(who.UID, name, programs); err != nil {
		return nil, err
	}
	log.Printf("uid %d set the network of Wine app %s to internet=%t home=%t", who.UID, id, want.Internet, want.Home)
	s.wineChanged()
	return programs, nil
}

// wineRoom refuses a new program past maxWinePrograms.
func wineRoom(programs map[string]winesandbox.Flags, id string, want winesandbox.Flags) error {
	if _, listed := programs[id]; !listed && want != (winesandbox.Flags{}) && len(programs) >= maxWinePrograms {
		return fmt.Errorf("%w: at most %d apps can have a network switch on", errRefused, maxWinePrograms)
	}
	return nil
}

// wineOpens names what turns on from have to want, or "" when nothing
// does.
func wineOpens(have, want winesandbox.Flags) string {
	internet := want.Internet && !have.Internet
	home := want.Home && !have.Home
	switch {
	case internet && home:
		return "the internet and your home network"
	case internet:
		return "the internet"
	case home:
		return "your home network and tailnet"
	}
	return ""
}

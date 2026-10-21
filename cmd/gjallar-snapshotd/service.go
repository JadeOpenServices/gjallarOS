package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/JadeOpenServices/gjallarOS/internal/snapshots"
)

var errDenied = errors.New("not authorized")

// system is what the service needs from the bus and the user database;
// tests replace it.
type system interface {
	// UID is the user behind a bus connection.
	UID(ctx context.Context, sender string) (int, error)
	// Authorize asks polkit, with the caller's own password, every time.
	Authorize(ctx context.Context, sender, message string) error
}

type service struct {
	sys system
	// root is where @snapshots is mounted; home snapshots are in home/.
	root string
	// home looks up a user's home folder.
	home func(uid int) (string, error)
}

func lookupHome(uid int) (string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", fmt.Errorf("look up your account: %w", err)
	}
	return u.HomeDir, nil
}

// List gives the home snapshot names, newest first.
func (s *service) List() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "home"))
	if err != nil {
		return nil, fmt.Errorf("read the snapshots: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return snapshots.Newest(names), nil
}

// Restore checks the request, asks the caller's password, and opens path
// from the snapshot from, or from the newest snapshot that has it.
func (s *service) Restore(ctx context.Context, sender, path, from string) (*os.File, string, error) {
	if from != "" && !snapshots.ValidName(from) {
		return nil, "", fmt.Errorf("%w: %q is not a snapshot name; see gjallarctl snapshots list", snapshots.ErrRefused, from)
	}
	uid, err := s.sys.UID(ctx, sender)
	if err != nil {
		return nil, "", err
	}
	home, err := s.home(uid)
	if err != nil {
		return nil, "", err
	}
	homeName, rel, err := snapshots.InHome(home, path)
	if err != nil {
		return nil, "", err
	}
	names, err := s.List()
	if err != nil {
		return nil, "", err
	}
	if from != "" {
		if !contains(names, from) {
			return nil, "", fmt.Errorf("%w: there is no snapshot %s; see gjallarctl snapshots list", snapshots.ErrRefused, from)
		}
		names = []string{from}
	}
	if len(names) == 0 {
		return nil, "", fmt.Errorf("%w: there are no snapshots yet", snapshots.ErrNotFound)
	}

	// %q: a file name with a line break cannot add lines to the dialog.
	message := fmt.Sprintf("Authentication is required to restore %q from the newest snapshot that has it", path)
	if from != "" {
		message = fmt.Sprintf("Authentication is required to restore %q from the snapshot %s", path, from)
	}
	if err := s.sys.Authorize(ctx, sender, message); err != nil {
		return nil, "", err
	}

	for _, name := range names {
		file, err := snapshots.Open(filepath.Join(s.root, "home", name), homeName, rel, uid)
		if errors.Is(err, snapshots.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return file, name, nil
	}
	if from != "" {
		return nil, "", fmt.Errorf("%w: %s is not in the snapshot %s", snapshots.ErrNotFound, path, from)
	}
	return nil, "", fmt.Errorf("%w: %s is in none of the %d snapshots", snapshots.ErrNotFound, path, len(names))
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

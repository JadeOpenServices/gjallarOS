package movehelper

import (
	"errors"
	"fmt"
	"slices"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/identity"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// nextcloudCfg is the Nextcloud client's settings file in a home.
const nextcloudCfg = ".config/Nextcloud/nextcloud.cfg"

// newMachineID is the machine-id on the new root.
const newMachineID = "etc/machine-id"

// errNotYet is the answer for a kept item whose writer is not built yet.
var errNotYet = errors.New("the stick cannot carry this over yet")

// IdentityResult is what the identity step did with each kept item.
type IdentityResult struct {
	// Written lists the kept items now on the new disk.
	Written []identity.Item `json:"written,omitempty"`
	// Failed tells for each kept item that stays behind why.
	Failed map[identity.Item]string `json:"failed,omitempty"`
	// SyncFolders counts the Nextcloud folders made again, empty, for the
	// client to fill from the server (L158).
	SyncFolders int `json:"syncFolders,omitempty"`
}

// identityOp writes the kept identity items onto the new disk (design 345,
// 4.4; L64). Root reads every item again from the old disk itself and
// writes only what it checked; the caller names items, never values. An
// item that is not kept is made new: the new disk has none of it, and for
// Nextcloud the settings the home copy brought along are removed. One
// item that fails stays behind and the others still go.
func identityOp(base string, req Request) (res IdentityResult, err error) {
	keep := map[identity.Item]bool{}
	for _, it := range req.Keep {
		if !slices.Contains(identity.Items, it) {
			return res, fmt.Errorf("identity: unknown item %q", it)
		}
		if keep[it] {
			return res, fmt.Errorf("identity: %q named twice", it)
		}
		keep[it] = true
	}
	top, info, err := openedTop(base)
	if err != nil {
		return res, err
	}
	defer top.Close()
	newRoot, err := mountedNew(base)
	if err != nil {
		return res, err
	}
	defer newRoot.Close()
	if err := onSubvolume(newRoot, newRoot, "@"); err != nil {
		return res, err
	}
	for _, it := range identity.Items {
		if !keep[it] {
			if it == identity.Nextcloud {
				if err := renewNextcloud(newRoot, info.Username); err != nil {
					return res, fmt.Errorf("identity: make the Nextcloud account new: %w", err)
				}
			}
			continue
		}
		var err error
		switch it {
		case identity.MachineID:
			err = keepMachineID(top, newRoot)
		case identity.Nextcloud:
			res.SyncFolders, err = keepNextcloud(top, newRoot, info.Username)
		default:
			err = errNotYet
		}
		if err != nil {
			if res.Failed == nil {
				res.Failed = map[identity.Item]string{}
			}
			res.Failed[it] = err.Error()
			continue
		}
		res.Written = append(res.Written, it)
	}
	return res, nil
}

// keepMachineID writes the old disk's machine-id, checked as the scan
// checked it, as the new root's /etc/machine-id: root's, read-only.
func keepMachineID(top, newRoot *safefs.Dir) error {
	id, err := identity.ReadMachineID(top)
	if err != nil {
		return err
	}
	defer clear(id)
	return newRoot.WriteFile(newMachineID, id, int(rootUID), int(rootGID), 0o444)
}

// keepNextcloud keeps the Nextcloud account the home copy brought along.
// Each sync folder of the old settings that is not in the new home (it
// stayed behind) is made again, empty and private: a folder with no sync
// journal makes the client download it from the server, where a missing
// one would leave the account stuck (L158). The made count is returned.
func keepNextcloud(top, newRoot *safefs.Dir, username string) (int, error) {
	old, err := freespace.OpenHome(top, username)
	if err != nil {
		return 0, err
	}
	defer old.Close()
	home, uid, err := installedHome(newRoot, username)
	if err != nil {
		return 0, err
	}
	defer home.Close()
	switch st, err := home.Lstat(nextcloudCfg); {
	case errors.Is(err, unix.ENOENT):
		return 0, errors.New("the Nextcloud settings did not come along with the home folder")
	case err != nil:
		return 0, err
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return 0, fmt.Errorf("the Nextcloud settings in the new home are not a plain file: %w", safefs.ErrNotPlain)
	}
	made := 0
	for _, rel := range old.Sync {
		switch _, err := home.Lstat(rel); {
		case err == nil:
			// Copied with the home.
			continue
		case !errors.Is(err, unix.ENOENT):
			return made, err
		}
		if err := home.MakeDirs(rel, uid, usersGID, 0o700); err != nil {
			return made, err
		}
		made++
	}
	return made, nil
}

// renewNextcloud removes the Nextcloud settings from the new home, so the
// client starts with a new login. A home without them has nothing to do.
func renewNextcloud(newRoot *safefs.Dir, username string) error {
	home, _, err := installedHome(newRoot, username)
	if err != nil {
		return err
	}
	defer home.Close()
	if _, err := home.Lstat(".config/Nextcloud"); errors.Is(err, unix.ENOENT) {
		return nil
	}
	return home.Remove(nextcloudCfg)
}

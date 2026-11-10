package installreport

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// FileName is the report file's name for a report made at now.
func FileName(now time.Time) string {
	return "gjallaros-install-report-" + now.UTC().Format("20060102-150405") + ".txt"
}

// Destination is the folder a report goes to and who owns it there.
type Destination struct {
	Dir string
	// UID and GID own the file when root saves it for the user who ran
	// sudo. Both are -1 when the file keeps the running user as owner.
	UID, GID int
}

// SaveDestination is the running user's home folder. When root runs the
// installer through sudo, it is the home folder of the user who ran sudo
// instead, so they find the report (R35). SUDO_UID only matters to root,
// and whoever sets it is root already.
func SaveDestination() (Destination, error) {
	if os.Geteuid() == 0 {
		if id := os.Getenv("SUDO_UID"); id != "" && id != "0" {
			if _, err := strconv.ParseUint(id, 10, 32); err != nil {
				return Destination{}, fmt.Errorf("SUDO_UID %q is not a user ID", id)
			}
			account, err := user.LookupId(id)
			if err != nil {
				return Destination{}, err
			}
			uid, _ := strconv.Atoi(account.Uid)
			gid, err := strconv.Atoi(account.Gid)
			if err != nil {
				return Destination{}, fmt.Errorf("group of user %s: %w", account.Username, err)
			}
			return Destination{Dir: account.HomeDir, UID: uid, GID: gid}, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Destination{}, err
	}
	return Destination{Dir: home, UID: -1, GID: -1}, nil
}

// Save writes data to a new file in dest.Dir, readable by the owner only.
// It never replaces or follows an existing file or link, so nothing planted
// at that name gets the report. When it saves for another user, the folder
// itself must belong to that user, and the file is theirs before any byte
// is written.
func Save(dest Destination, data []byte, now time.Time) (string, error) {
	if len(data) > MaxSize {
		return "", fmt.Errorf("report is larger than %d bytes", MaxSize)
	}
	path := filepath.Join(dest.Dir, FileName(now))
	dir, err := unix.Open(dest.Dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", dest.Dir, err)
	}
	defer unix.Close(dir)
	if dest.UID >= 0 {
		var st unix.Stat_t
		if err := unix.Fstat(dir, &st); err != nil {
			return "", fmt.Errorf("check %s: %w", dest.Dir, err)
		}
		if int(st.Uid) != dest.UID {
			return "", fmt.Errorf("%s does not belong to user %d; nothing written", dest.Dir, dest.UID)
		}
	}

	fd, err := unix.Openat(dir, FileName(now), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return "", fmt.Errorf("%s already exists; nothing written", path)
		}
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if dest.UID >= 0 {
		err = file.Chown(dest.UID, dest.GID)
	}
	if err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		unix.Unlinkat(dir, FileName(now), 0)
		return "", err
	}
	return path, nil
}

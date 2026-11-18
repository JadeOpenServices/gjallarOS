package freespace

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// Places on the old disk, below the btrfs top level.
const (
	homeDir      = "@home"
	nextcloudCfg = ".config/Nextcloud/nextcloud.cfg"
	checkoutGit  = "@config/.git"
)

// Caps. A real system stays far below each; a crafted one cannot make root
// walk or hold names without end.
const (
	// maxEntries is shared by all walks of one Measure.
	maxEntries = 1 << 26
	// maxFolder caps the names of one folder.
	maxFolder = 1 << 20
	// maxCfg caps nextcloud.cfg.
	maxCfg = 1 << 20
	// maxSyncFolders caps the sync folders taken from nextcloud.cfg.
	maxSyncFolders = 64
	// Account ids as the old system hands them out; root never measures a
	// home of a system account.
	minUserID = 1000
	maxUserID = 59999
)

// Sizes is what Measure found: bytes as a copy counts them against its
// budget (safefs.Size.Used), and counts. No names: they stay in the helper.
type Sizes struct {
	Home      uint64 `json:"home"`
	Nextcloud uint64 `json:"nextcloud"`
	Branches  uint64 `json:"branches"`
	// SyncFolders counts the Nextcloud sync folders measured on their own.
	SyncFolders int `json:"syncFolders"`
	// Skipped counts pipes, sockets and devices; Foreign entries of other
	// owners in the home, which the copy leaves out too; Odd sync folder
	// lines in nextcloud.cfg that were not taken. An odd folder stays in the
	// home size, so nothing goes uncounted.
	Skipped int `json:"skipped"`
	Foreign int `json:"foreign"`
	Odd     int `json:"odd"`
}

// ErrNoHome is returned when the old account has no home folder of its own.
var ErrNoHome = errors.New("the old account has no home folder of its own")

// Measure sizes the categories of the old disk, whose btrfs top level is
// top, mounted read-only. username is the old account (oldroot.Info). No
// file is opened except nextcloud.cfg; links are never followed.
func Measure(top *safefs.Dir, username string) (Sizes, error) {
	var s Sizes
	if !config.ValidUsername(username) {
		return s, fmt.Errorf("measure: %q is not a valid user name", username)
	}
	homeRel := homeDir + "/" + username
	st, err := top.Lstat(homeRel)
	if err != nil {
		return s, fmt.Errorf("measure: %w: %v", ErrNoHome, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid < minUserID || st.Uid > maxUserID {
		return s, fmt.Errorf("measure: %w", ErrNoHome)
	}
	owner := int(st.Uid)
	home, err := top.Sub(homeRel)
	if err != nil {
		return s, fmt.Errorf("measure: %w", err)
	}
	defer home.Close()

	left := maxEntries
	walk := func(d *safefs.Dir, opt safefs.MeasureOptions) (safefs.Size, error) {
		opt.MaxEntries, opt.MaxFolder = left, maxFolder
		size, err := safefs.Measure(d, opt)
		left -= size.Files + size.Folders + size.Links + size.Skipped + size.Foreign + size.Left
		s.Skipped += size.Skipped
		s.Foreign += size.Foreign
		return size, err
	}

	folders, odd := syncFolders(home, username)
	s.Odd = odd
	skip := map[string]bool{}
	for _, rel := range folders {
		st, err := home.Lstat(rel)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		var d *safefs.Dir
		if err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR && int(st.Uid) == owner {
			d, err = home.Sub(rel)
		} else if err == nil {
			err = safefs.ErrNotPlain
		}
		if err != nil {
			// A link, a mount point or another owner in the way: the copy
			// refuses it too. It stays inside the home walk, which counts it
			// as it is.
			s.Odd++
			continue
		}
		size, err := walk(d, safefs.MeasureOptions{SourceUID: owner})
		d.Close()
		if err != nil {
			return s, fmt.Errorf("measure the Nextcloud folders: %w", err)
		}
		// The folder itself costs an entry too; the home walk leaves it out.
		s.Nextcloud += uint64(size.Used + safefs.EntryCost)
		s.SyncFolders++
		skip[rel] = true
	}

	size, err := walk(home, safefs.MeasureOptions{SourceUID: owner, Skip: skip})
	if err != nil {
		return s, fmt.Errorf("measure the home folder: %w", err)
	}
	s.Home = uint64(size.Used)

	git, err := top.Sub(checkoutGit)
	switch {
	case errors.Is(err, unix.ENOENT):
	case err != nil:
		return s, fmt.Errorf("measure the branches: %w", err)
	default:
		// The checkout belongs to root and its owner; it moves as a whole.
		size, err := walk(git, safefs.MeasureOptions{AnyOwner: true})
		git.Close()
		if err != nil {
			return s, fmt.Errorf("measure the branches: %w", err)
		}
		s.Branches = uint64(size.Used)
	}
	return s, nil
}

// syncKey matches the sync folder lines of nextcloud.cfg, for example
// 0\FoldersWithPlaceholders\1\localPath.
var syncKey = regexp.MustCompile(`^[0-9]{1,4}\\(Folders|FoldersWithPlaceholders|Multifolders)\\[0-9]{1,4}\\localPath$`)

// syncFolders reads the Nextcloud sync folders of the old account from
// nextcloud.cfg, as paths relative to its home. Only clean absolute paths
// below /home/<username>/ are taken; anything else, and a folder inside
// another one, counts as odd. A missing or unreadable cfg means no
// folders.
func syncFolders(home *safefs.Dir, username string) (folders []string, odd int) {
	data, err := home.ReadFile(nextcloudCfg, maxCfg)
	if err != nil {
		if !errors.Is(err, unix.ENOENT) {
			odd++
		}
		return nil, odd
	}
	prefix := "/home/" + username + "/"
	section := ""
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), maxCfg)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if section != "[Accounts]" || !ok || !syncKey.MatchString(strings.TrimSpace(key)) {
			continue
		}
		rel, ok := homeRelative(strings.TrimSpace(value), prefix)
		if ok && seen[rel] {
			continue
		}
		if !ok || len(seen) >= maxSyncFolders {
			odd++
			continue
		}
		seen[rel] = true
	}
	if sc.Err() != nil {
		odd++
	}
	for rel := range seen {
		if inside(rel, seen) {
			odd++
			continue
		}
		folders = append(folders, rel)
	}
	sort.Strings(folders)
	return folders, odd
}

// homeRelative turns a localPath value into a path relative to the home,
// or refuses it: quoted or escaped values, relative paths, paths with ..
// and paths outside the home.
func homeRelative(value, prefix string) (string, bool) {
	if strings.ContainsAny(value, "\"\\\x00") {
		return "", false
	}
	p := strings.TrimSuffix(value, "/")
	if !strings.HasPrefix(p, prefix) || filepath.Clean(p) != p {
		return "", false
	}
	rel := strings.TrimPrefix(p, prefix)
	if rel == "" || !filepath.IsLocal(rel) {
		return "", false
	}
	return rel, true
}

// inside tells whether rel lies below another folder in set.
func inside(rel string, set map[string]bool) bool {
	for p := filepath.Dir(rel); p != "."; p = filepath.Dir(p) {
		if set[p] {
			return true
		}
	}
	return false
}

package storecopy

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// What the export may leave in the cache, and nothing else. Root's nix
// reads these files later, so each one is taken over first and checked
// after: a link, a pipe or a device would be read by root (L62).
const (
	cacheInfo = "nix-cache-info"
	narDir    = "nar"
	// realisations and log stay empty: the export copies no content-
	// addressed derivation outputs and no build logs.
	realisationsDir = "realisations"
	logDir          = "log"

	cacheInfoText = "StoreDir: /nix/store\n"

	// MaxPaths caps the store paths in one cache. A NixOS desktop closure
	// has a few thousand.
	MaxPaths = 100_000
	// maxNarinfo caps one narinfo; one with many references is a few KiB.
	maxNarinfo = 64 << 10
)

// ErrCache is a cache that is not exactly what an export leaves.
var ErrCache = errors.New("the store copy is not what an export leaves")

var (
	narinfoName = regexp.MustCompile(`^(` + b32 + `{32})\.narinfo$`)
	narName     = regexp.MustCompile(`^` + b32 + `{52}\.nar$`)
	narHash     = regexp.MustCompile(`^sha256:` + b32 + `{52}$`)
	refName     = regexp.MustCompile(`^` + b32 + `{32}-[A-Za-z0-9+._?=-]{1,211}$`)
	derivName   = regexp.MustCompile(`^` + b32 + `{32}-[A-Za-z0-9+._?=-]{1,207}\.drv$`)
	sigRE       = regexp.MustCompile(`^[A-Za-z0-9.-]{1,64}-[0-9]{1,3}:[A-Za-z0-9+/]{86}==$`)
	caRE        = regexp.MustCompile(`^(text:sha256|fixed:(r:)?(sha1|sha256|sha512|md5)):[0-9a-z]{26,128}$`)
	systemName  = regexp.MustCompile(`^[a-z0-9_]{1,16}-[a-z0-9_]{1,16}$`)
)

// Stats is what a checked cache holds.
type Stats struct {
	Paths int   `json:"paths"`
	Bytes int64 `json:"bytes"`
}

// Settle takes the cache folder over for uid and gid and checks it. Every
// entry is owned and its mode set through its own handle first, so nothing
// changes between the check and root's read later; then each narinfo is
// read and each NAR matched to exactly one narinfo. A cache over budget
// bytes or MaxPaths paths is refused. On any error the caller removes the
// cache and the install downloads instead.
func Settle(cache *safefs.Dir, uid, gid int, budget int64) (Stats, error) {
	var s Stats
	if _, err := cache.Own(".", uid, gid, 0o755); err != nil {
		return s, err
	}
	top, err := cache.Names(".", MaxPaths+4)
	if err != nil {
		return s, err
	}
	narinfos := map[string]bool{}
	for _, n := range top {
		switch {
		case n == cacheInfo:
			if err := ownFile(cache, n, uid, gid, nil); err != nil {
				return s, err
			}
		case n == narDir || n == realisationsDir || n == logDir:
			if err := ownFolder(cache, n, uid, gid); err != nil {
				return s, err
			}
		case narinfoName.MatchString(n):
			if err := ownFile(cache, n, uid, gid, nil); err != nil {
				return s, err
			}
			narinfos[n] = true
		default:
			return s, fmt.Errorf("%w: %q", ErrCache, n)
		}
	}
	info, err := cache.ReadFile(cacheInfo, int64(len(cacheInfoText)))
	if err != nil || string(info) != cacheInfoText {
		return s, fmt.Errorf("%w: %s", ErrCache, cacheInfo)
	}
	for _, d := range []string{realisationsDir, logDir} {
		if names, err := cache.Names(d, 0); err != nil && !errors.Is(err, unix.ENOENT) || len(names) > 0 {
			return s, fmt.Errorf("%w: %s is not empty", ErrCache, d)
		}
	}
	nars, err := cache.Names(narDir, MaxPaths)
	if err != nil {
		if errors.Is(err, unix.ENOENT) && len(narinfos) == 0 {
			return s, nil
		}
		return s, err
	}
	sizes := make(map[string]int64, len(nars))
	for _, n := range nars {
		if !narName.MatchString(n) {
			return s, fmt.Errorf("%w: %s/%q", ErrCache, narDir, n)
		}
		var st unix.Stat_t
		if err := ownFile(cache, narDir+"/"+n, uid, gid, &st); err != nil {
			return s, err
		}
		sizes[n] = st.Size
	}
	for n := range narinfos {
		data, err := cache.ReadFile(n, maxNarinfo)
		if err != nil {
			return s, err
		}
		ni, err := parseNarinfo(string(data))
		if err != nil {
			return s, fmt.Errorf("%s: %w", n, err)
		}
		if ni.hash+".narinfo" != n {
			return s, fmt.Errorf("%w: %s names another path", ErrCache, n)
		}
		size, ok := sizes[ni.nar]
		if !ok || size != ni.size {
			return s, fmt.Errorf("%w: %s: NAR missing, used twice or of another size", ErrCache, n)
		}
		delete(sizes, ni.nar)
		s.Paths++
		if s.Bytes += size; s.Bytes > budget {
			return s, fmt.Errorf("%w: over %d bytes", ErrCache, budget)
		}
	}
	if len(sizes) > 0 {
		return s, fmt.Errorf("%w: %d NARs no narinfo names", ErrCache, len(sizes))
	}
	return s, nil
}

// ownFile takes a plain file over, refusing one with other names (hard
// links), and reports its stat into st when st is not nil.
func ownFile(cache *safefs.Dir, rel string, uid, gid int, st *unix.Stat_t) error {
	got, err := cache.Own(rel, uid, gid, 0o644)
	if err != nil {
		return err
	}
	if got.Mode&unix.S_IFMT != unix.S_IFREG || got.Nlink != 1 {
		return fmt.Errorf("%w: %s is not a plain file with one name", ErrCache, rel)
	}
	if st != nil {
		*st = got
	}
	return nil
}

func ownFolder(cache *safefs.Dir, rel string, uid, gid int) error {
	got, err := cache.Own(rel, uid, gid, 0o755)
	if err != nil {
		return err
	}
	if got.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: %s is not a folder", ErrCache, rel)
	}
	return nil
}

type narinfo struct {
	hash string // the store path's hash part
	nar  string // the NAR's name in nar/
	size int64
}

// parseNarinfo reads a narinfo as nix writes it for an uncompressed cache.
// Every key is known, every value has its shape, and the NAR is named by
// its own hash.
func parseNarinfo(text string) (narinfo, error) {
	var ni narinfo
	body, ok := strings.CutSuffix(text, "\n")
	if !ok {
		return ni, fmt.Errorf("%w: no last line end", ErrCache)
	}
	seen := map[string]string{}
	for line := range strings.SplitSeq(body, "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			return ni, fmt.Errorf("%w: line %q", ErrCache, line)
		}
		if _, dup := seen[k]; dup && k != "Sig" {
			return ni, fmt.Errorf("%w: %s twice", ErrCache, k)
		}
		seen[k] = v
		if !narinfoValue(k, v) {
			return ni, fmt.Errorf("%w: %s %q", ErrCache, k, v)
		}
	}
	for _, k := range []string{"StorePath", "URL", "Compression", "FileHash", "FileSize", "NarHash", "NarSize"} {
		if _, ok := seen[k]; !ok {
			return ni, fmt.Errorf("%w: no %s", ErrCache, k)
		}
	}
	m := storePathRE.FindStringSubmatch(seen["StorePath"])
	if m == nil {
		return ni, fmt.Errorf("%w: StorePath", ErrCache)
	}
	ni.hash = m[1]
	if seen["Compression"] != "none" || seen["FileHash"] != seen["NarHash"] || seen["FileSize"] != seen["NarSize"] {
		return ni, fmt.Errorf("%w: a compressed or changed NAR", ErrCache)
	}
	ni.nar = strings.TrimPrefix(seen["URL"], narDir+"/")
	if ni.nar != strings.TrimPrefix(seen["FileHash"], "sha256:")+".nar" {
		return ni, fmt.Errorf("%w: URL is not named by its hash", ErrCache)
	}
	ni.size, _ = strconv.ParseInt(seen["NarSize"], 10, 64)
	return ni, nil
}

func narinfoValue(k, v string) bool {
	switch k {
	case "StorePath":
		return StorePath(v)
	case "URL":
		n, ok := strings.CutPrefix(v, narDir+"/")
		return ok && narName.MatchString(n)
	case "Compression":
		return v == "none"
	case "FileHash", "NarHash":
		return narHash.MatchString(v)
	case "FileSize", "NarSize":
		n, err := strconv.ParseInt(v, 10, 64)
		return err == nil && n > 0 && strconv.FormatInt(n, 10) == v
	case "References":
		if v == "" {
			return true
		}
		for r := range strings.SplitSeq(v, " ") {
			if !refName.MatchString(r) {
				return false
			}
		}
		return true
	case "Deriver":
		return derivName.MatchString(v)
	case "Sig":
		return sigRE.MatchString(v)
	case "CA":
		return caRE.MatchString(v)
	case "System":
		return systemName.MatchString(v)
	}
	return false
}

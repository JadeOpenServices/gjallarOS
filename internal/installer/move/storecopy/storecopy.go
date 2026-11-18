// Package storecopy carries the old disk's nix store over to the new one
// (345 step 11-12, L62), so the install downloads and builds less.
//
// Root never reads the old store as a store. An unprivileged sandbox unit
// (DynamicUser, no network, nothing of the stick but the old store read-only
// and one empty cache folder) exports the old system closure into a file
// cache on the new disk. Root then takes the cache over, checks every entry,
// and nixos-install uses it as a substituter that needs the usual
// signatures: a path from the old disk is only used when cache.nixos.org or
// the gjallarOS cache signed it, or when its content matches its name. The
// rest is downloaded or built again. A changed old disk can therefore not
// put anything into the new system; at most it fills the cache (L131).
package storecopy

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// CacheName is the cache folder on the new disk, below the new root.
	CacheName = "gjallar-move-cache"

	// oldNix and cacheDir are where the unit sees the old @nix subvolume
	// and the cache. Both are bind mounts, so the unit's user needs no way
	// through the helper's closed folder.
	oldNix   = "/oldnix"
	cacheDir = "/cache"

	// FromURL reads the old store read-only, without its daemon or locks.
	FromURL = "local?real=" + oldNix + "/store&state=" + oldNix + "/var/nix&read-only=true"
	// ToURL writes plain NARs: root checks each file's size against its
	// narinfo, and nothing has to be unpacked to do so.
	ToURL = "file://" + cacheDir + "?compression=none"

	// NixOSKey signs cache.nixos.org.
	NixOSKey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="

	// ExportUnit and ClosureUnit name the units, so a second run while
	// one is still going fails to start instead of running twice.
	ExportUnit  = "gjallar-move-export"
	ClosureUnit = "gjallar-move-closure"
	// PackUnit packs the old system checkout's commits (repocopy, L63).
	PackUnit = "gjallar-move-pack"
	// GitUnit runs root's git steps in the new checkout (repocopy, L141).
	GitUnit = "gjallar-move-git"

	// NotLoaded is systemctl's exit code for a unit that is not loaded:
	// nothing of it runs.
	NotLoaded = 5

	// ExportLimit and ClosureLimit stop a unit that runs too long.
	ExportLimit  = 2 * time.Hour
	ClosureLimit = 5 * time.Minute
	PackLimit    = 30 * time.Minute
	GitLimit     = 30 * time.Minute

	features = "nix-command read-only-local-store"
)

var (
	// Nix is the nix the unit runs, pinned at build time.
	Nix = "/run/current-system/sw/bin/nix"
	// SystemdRun starts the units. It stays the system's own, so it
	// matches the systemd that runs as PID 1.
	SystemdRun = "/run/current-system/sw/bin/systemd-run"
	// Systemctl stops them, for the same reason the system's own.
	Systemctl = "/run/current-system/sw/bin/systemctl"
)

// Units are every confined unit of the move. A helper killed while
// systemd-run waits leaves its unit running, writing into the cache and
// holding the old disk; each one is stopped before the cache is removed or
// the old disk closed (L135).
var Units = []string{ExportUnit + ".service", ClosureUnit + ".service", PackUnit + ".service", GitUnit + ".service"}

// unitProps confine the unit. The unit's user exists only while it runs.
// It sees the stick read-only, no home, no devices, no network, no other
// process, and has no capabilities.
var unitProps = append([]string{"DynamicUser=yes", "Environment=HOME=/tmp"}, confined...)

// confined is what every unit of the move gets, the root one too: no
// network, no devices, no other process, no capabilities, the stick
// read-only, and caps on memory, tasks and the stop.
var confined = []string{
	"PrivateNetwork=yes",
	"PrivateTmp=yes",
	"PrivateDevices=yes",
	"PrivateIPC=yes",
	"ProtectSystem=strict",
	"ProtectHome=yes",
	"ProtectKernelTunables=yes",
	"ProtectKernelModules=yes",
	"ProtectKernelLogs=yes",
	"ProtectControlGroups=yes",
	"ProtectClock=yes",
	"ProtectHostname=yes",
	"ProtectProc=invisible",
	"ProcSubset=pid",
	"NoNewPrivileges=yes",
	"RestrictSUIDSGID=yes",
	"LockPersonality=yes",
	"RestrictRealtime=yes",
	"RestrictNamespaces=yes",
	"RestrictAddressFamilies=AF_UNIX",
	"SystemCallArchitectures=native",
	"CapabilityBoundingSet=",
	"UMask=0022",
	"MemoryMax=4G",
	"TasksMax=64",
	// A stop before cleanup (L135) kills a unit that does not end within
	// this, so close and the cache removal never wait systemd's 90 s.
	"TimeoutStopSec=10s",
	// nix would ask the stick's daemon for anything it cannot do alone.
	"InaccessiblePaths=-/nix/var/nix/daemon-socket",
}

// Bind is one bind mount into a confined unit: Src on the stick, Dst inside
// the unit. The zero Bind is none.
type Bind struct{ Src, Dst string }

func (b Bind) check() error {
	for _, p := range []string{b.Src, b.Dst} {
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, ": \t\n\r\v\f") {
			return fmt.Errorf("unit folder %q is not a plain absolute path", p)
		}
	}
	return nil
}

// UnitArgs returns systemd-run's arguments for one confined run of cmd, with
// ro bound read-only and rw, when not zero, bound writable. Nothing else of
// the stick is writable for it.
//
// systemd sets up the bind mounts by path, as root, and follows links on
// the way: nosymfollow on the old disk does not cover systemd's own lookup.
// Callers pass only paths fixed below the helper's closed folder, on the old
// disk mounted read-only, so no one else can change them; a ":" or blank
// would split the property, so those are refused.
func UnitArgs(unit string, limit time.Duration, ro, rw Bind, cmd ...string) ([]string, error) {
	if ro == (Bind{}) {
		return nil, errors.New("unit: no read-only folder")
	}
	if err := ro.check(); err != nil {
		return nil, err
	}
	if rw != (Bind{}) {
		if err := rw.check(); err != nil {
			return nil, err
		}
	}
	if len(cmd) == 0 || !strings.HasPrefix(cmd[0], "/") {
		return nil, errors.New("unit: the program is not an absolute path")
	}
	args := []string{
		"--quiet", "--wait", "--collect", "--pipe",
		"--unit=" + unit, "--service-type=exec",
		"--property=RuntimeMaxSec=" + strconv.Itoa(int(limit.Seconds())),
		"--property=BindReadOnlyPaths=" + ro.Src + ":" + ro.Dst,
	}
	if rw != (Bind{}) {
		args = append(args, "--property=BindPaths="+rw.Src+":"+rw.Dst)
	}
	for _, p := range unitProps {
		args = append(args, "--property="+p)
	}
	args = append(args, "--")
	return append(args, cmd...), nil
}

// RootUnitArgs returns systemd-run's arguments for one run of cmd as root in
// a confined unit: no capabilities, so root reaches only what root owns, and
// nothing writable but rw. Root's git takes the old disk's commits through
// index-pack and fsck; a fault in git's parsers then reaches only the new
// checkout's git folder, with no network and capped memory (L141). env is
// the program's whole environment, KEY=value each.
//
// rw is set up by path like the binds of UnitArgs: the caller checks it is
// root's and writable by no one else all the way down first.
func RootUnitArgs(unit string, limit time.Duration, rw string, env []string, cmd ...string) ([]string, error) {
	if err := (Bind{rw, rw}).check(); err != nil {
		return nil, err
	}
	if len(cmd) == 0 || !strings.HasPrefix(cmd[0], "/") {
		return nil, errors.New("unit: the program is not an absolute path")
	}
	args := []string{
		"--quiet", "--wait", "--collect", "--pipe",
		"--unit=" + unit, "--service-type=exec",
		"--property=RuntimeMaxSec=" + strconv.Itoa(int(limit.Seconds())),
		"--property=ReadWritePaths=" + rw,
	}
	for _, p := range confined {
		args = append(args, "--property="+p)
	}
	for _, e := range env {
		k, _, ok := strings.Cut(e, "=")
		if !ok || k == "" || strings.ContainsAny(e, "\x00\n\r") {
			return nil, fmt.Errorf("unit: environment %q is not KEY=value", e)
		}
		args = append(args, "--setenv="+e)
	}
	args = append(args, "--")
	return append(args, cmd...), nil
}

// unitArgs runs nix confined, with the old @nix subvolume at oldNix and the
// cache folder, when not "", at cacheDir.
func unitArgs(unit string, limit time.Duration, old, cache string, nixArgs ...string) ([]string, error) {
	var rw Bind
	if cache != "" {
		rw = Bind{cache, cacheDir}
	}
	if old == "" {
		return nil, errors.New("unit: no old store")
	}
	return UnitArgs(unit, limit, Bind{old, oldNix}, rw,
		append([]string{Nix, "--extra-experimental-features", features}, nixArgs...)...)
}

// ExportArgs returns systemd-run's arguments to export the closure of the
// store path into the cache folder.
func ExportArgs(old, cache, path string) ([]string, error) {
	if !StorePath(path) {
		return nil, fmt.Errorf("%q is not a store path", path)
	}
	if cache == "" {
		return nil, errors.New("export: no cache folder")
	}
	return unitArgs(ExportUnit, ExportLimit, old, cache,
		"copy", "--no-check-sigs", "--from", FromURL, "--to", ToURL, path)
}

// ClosureArgs returns systemd-run's arguments to measure the closure of the
// store path in the old store.
func ClosureArgs(old, path string) ([]string, error) {
	if !StorePath(path) {
		return nil, fmt.Errorf("%q is not a store path", path)
	}
	return unitArgs(ClosureUnit, ClosureLimit, old, "",
		"path-info", "--closure-size", "--store", FromURL, path)
}

// MaxClosureOutput caps what ClosureArgs' run may print.
const MaxClosureOutput = 4096

// ParseClosure reads the closure size that ClosureArgs' run printed for
// path: exactly one line of the path, a tab and the size.
func ParseClosure(out []byte, path string) (int64, error) {
	line, ok := strings.CutSuffix(string(out), "\n")
	if !ok || strings.Contains(line, "\n") {
		return 0, errors.New("closure size: not one line")
	}
	got, size, ok := strings.Cut(line, "\t")
	if !ok || got != path {
		return 0, errors.New("closure size: not the asked path")
	}
	n, err := strconv.ParseInt(strings.TrimLeft(size, " "), 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("closure size: not a size")
	}
	return n, nil
}

// SubstituteArgs returns nixos-install's options to use the checked cache
// at cache (an absolute path on the stick) before the usual caches. The
// signature rule is spelled out, never relaxed: a path from the cache needs
// a signature by one of keys, or content that matches its name. keys are
// the only keys trusted for the whole install; NixOSKey must be one.
func SubstituteArgs(cache string, keys ...string) ([]string, error) {
	if !strings.HasPrefix(cache, "/") || strings.ContainsAny(cache, "?#& \t\n") {
		return nil, fmt.Errorf("cache folder %q is not a plain absolute path", cache)
	}
	if !keyList(keys) {
		return nil, errors.New("substitute keys: not a list of named keys with the NixOS key")
	}
	return []string{
		"--option", "extra-substituters", "file://" + cache + "?priority=10",
		"--option", "trusted-public-keys", strings.Join(keys, " "),
		"--option", "require-sigs", "true",
	}, nil
}

var keyRE = regexp.MustCompile(`^[A-Za-z0-9.-]{1,64}-[0-9]{1,3}:[A-Za-z0-9+/]{43}=$`)

func keyList(keys []string) bool {
	nixos := false
	for _, k := range keys {
		if !keyRE.MatchString(k) {
			return false
		}
		nixos = nixos || k == NixOSKey
	}
	return nixos
}

// VerifyArgs returns the nix arguments that check the new system's closure
// in the new store at root against keys, after the install.
func VerifyArgs(root, toplevel string, keys ...string) ([]string, error) {
	if !strings.HasPrefix(root, "/") || strings.ContainsAny(root, "?#& \t\n") {
		return nil, fmt.Errorf("new root %q is not a plain absolute path", root)
	}
	if !StorePath(toplevel) {
		return nil, fmt.Errorf("%q is not a store path", toplevel)
	}
	if !keyList(keys) {
		return nil, errors.New("verify keys: not a list of named keys with the NixOS key")
	}
	return []string{
		"--extra-experimental-features", "nix-command",
		"--option", "trusted-public-keys", strings.Join(keys, " "),
		"store", "verify", "--store", "local?root=" + root, "--recursive", toplevel,
	}, nil
}

// nix's base32 leaves out e, o, u and t.
const b32 = `[0123456789abcdfghijklmnpqrsvwxyz]`

var (
	storePathRE = regexp.MustCompile(`^/nix/store/(` + b32 + `{32})-[A-Za-z0-9+._?=-]{1,211}$`)
	systemRE    = regexp.MustCompile(`^/nix/store/` + b32 + `{32}-nixos-system-[A-Za-z0-9+._?=-]{1,190}$`)
	generation  = regexp.MustCompile(`^system-([1-9][0-9]{0,5})-link$`)
)

// StorePath reports whether p names a store path.
func StorePath(p string) bool { return storePathRE.MatchString(p) && !strings.HasPrefix(p[44:], ".") }

// Links reads one link without following it, as safefs.Dir.Readlink does.
type Links interface {
	Readlink(rel string, max int) (string, error)
}

// Profiles is the system profile folder inside the old @nix subvolume.
const Profiles = "var/nix/profiles"

// OldSystem finds the store path of the old disk's current system: the
// generation link the system profile names, and the system it names. nix
// is the old @nix subvolume. Neither link is followed, only read.
func OldSystem(nix Links) (string, error) {
	gen, err := nix.Readlink(Profiles+"/system", 64)
	if err != nil {
		return "", fmt.Errorf("old system profile: %w", err)
	}
	if !generation.MatchString(gen) {
		return "", fmt.Errorf("old system profile names %q, not a generation", gen)
	}
	sys, err := nix.Readlink(Profiles+"/"+gen, 256)
	if err != nil {
		return "", fmt.Errorf("old system generation: %w", err)
	}
	if !systemRE.MatchString(sys) {
		return "", fmt.Errorf("old system generation names %q, not a NixOS system", sys)
	}
	return sys, nil
}

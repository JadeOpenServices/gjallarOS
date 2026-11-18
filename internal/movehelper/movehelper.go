// Package movehelper is the root side of the disk move (345). The move runs
// on the USB stick, where the live login on the other consoles has the same
// user id as the user on the new disk (L58). Root work therefore never
// happens below /mnt or any folder that user can change: the helper keeps
// its folders under /run/gjallar-move, owned by root and closed to everyone
// else, and takes a fixed list of operations as JSON on stdin. It never
// takes a path outside its own folders.
package movehelper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/identity"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/prepared"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/btaddr"
	"golang.org/x/sys/unix"
)

const (
	// Base is the helper's folder on the stick.
	Base = "/run/gjallar-move"
	// Old holds the old disk, mounted read-only (CP2).
	Old = "old"
	// New holds the new root.
	New = "new"

	// MaxRequest caps the JSON read from stdin.
	MaxRequest = 64 << 10

	minUserID = 1000
	maxUserID = 59999
	// usersGID is the "users" group, the primary group of NixOS users.
	usersGID = 100

	// maxKey caps the key for the old disk: a passphrase or recovery key.
	maxKey = 1024
)

// KeepXattr lists the extended attributes a copy keeps: the Nextcloud
// client's file state. Anything else stays on the old disk (L61).
func KeepXattr(name string) bool { return strings.HasPrefix(name, "user.openvfs.") }

// Request is one operation.
type Request struct {
	Op string `json:"op"`
	// Src is a path below Old, Dst a path below New (copy).
	Src string `json:"src,omitempty"`
	Dst string `json:"dst,omitempty"`
	// SrcUID owns the files on the old disk; files of anyone else stay
	// behind. UID and GID own the copies (copy).
	SrcUID int `json:"srcUid,omitempty"`
	UID    int `json:"uid,omitempty"`
	GID    int `json:"gid,omitempty"`
	// Budget is the most bytes the copy or the store export may use on the
	// new disk (copy, export).
	Budget int64 `json:"budget,omitempty"`
	// Disk is the old disk as find listed it (check, open).
	Disk string `json:"disk,omitempty"`
	// Key unlocks an encrypted old disk, exactly these bytes (open).
	Key []byte `json:"key,omitempty"`
}

// RefusalResult is what a refused step returns. It is an answer for the
// screen, not a failure of the helper.
type RefusalResult struct {
	Refusal *oldroot.Refusal `json:"refusal"`
}

// Run reads one request from in and carries it out below base. The result
// goes to out as JSON.
func Run(base string, in io.Reader, out io.Writer) error {
	req, err := readRequest(in)
	if err != nil {
		return err
	}
	defer clear(req.Key)
	ctx := context.Background()
	var result any
	switch req.Op {
	case "dirs":
		err = Dirs(base)
		result = struct{}{}
	case "find":
		result, err = findOp(ctx)
	case "copy", "check", "open", "inventory", "sizes", "close", "closure", "export", "dropcache":
		var unlock func()
		if unlock, err = lock(base); err != nil {
			break
		}
		defer unlock()
		switch req.Op {
		case "copy":
			result, err = copyOp(base, req)
		case "check":
			result, err = checkOp(ctx, req)
		case "open":
			result, err = openOp(ctx, base, req)
		case "inventory":
			result, err = inventoryOp(ctx, base)
		case "sizes":
			result, err = sizesOp(base)
		case "close":
			if err = stopUnits(ctx); err == nil {
				err = oldroot.Close(ctx, filepath.Join(base, Old))
			}
			result = struct{}{}
		case "closure":
			result, err = closureOp(ctx, base)
		case "export":
			result, err = exportOp(ctx, base, req)
		case "dropcache":
			err = dropCache(ctx, base)
			result = struct{}{}
		}
	default:
		return fmt.Errorf("unknown operation %q", req.Op)
	}
	if r, ok := oldroot.AsRefusal(err); ok {
		result, err = RefusalResult{r}, nil
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

func readRequest(in io.Reader) (Request, error) {
	var req Request
	data, err := io.ReadAll(io.LimitReader(in, MaxRequest+1))
	if err != nil {
		return req, fmt.Errorf("read request: %w", err)
	}
	// The request can carry the old disk's key.
	defer clear(data)
	if len(data) > MaxRequest {
		return req, fmt.Errorf("request is over %d bytes", MaxRequest)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("read request: %w", err)
	}
	if dec.More() {
		return req, errors.New("read request: more than one request")
	}
	return req, nil
}

// Dirs makes base with Old and New in it, owned by the helper's user and
// closed to everyone else. A folder that is already there must look exactly
// like that, or the move stops: someone else made it.
func Dirs(base string) error {
	if !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return fmt.Errorf("helper folder %q is not a clean absolute path", base)
	}
	for _, p := range []string{base, filepath.Join(base, Old), filepath.Join(base, New)} {
		if err := ownDir(p); err != nil {
			return err
		}
	}
	return nil
}

// lock makes the helper folders and holds base locked, so a second helper
// run (the live login on another console has the same user, L58) waits its
// turn instead of closing or reusing what this one opened. A held lock
// refuses at once rather than queueing.
func lock(base string) (func(), error) {
	if err := Dirs(base); err != nil {
		return nil, err
	}
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", base, err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("another move step is running")
		}
		return nil, fmt.Errorf("lock %s: %w", base, err)
	}
	return func() { unix.Close(fd) }, nil
}

func ownDir(p string) error {
	if err := unix.Mkdir(p, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("make %s: %w", p, err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return fmt.Errorf("check %s: %w", p, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s is not a folder", p)
	}
	if int(st.Uid) != os.Geteuid() || st.Mode&0o7777 != 0o700 {
		return fmt.Errorf("%s belongs to user %d with mode %o; the move needs it to be its own (mode 700)", p, st.Uid, st.Mode&0o7777)
	}
	return nil
}

// local refuses a path that is empty, absolute, climbs out or is not clean.
// openat2 refuses those as well; this gives a clear message first.
func local(what, p string) error {
	if p == "" || !filepath.IsLocal(p) || filepath.Clean(p) != p {
		return fmt.Errorf("%s %q is not a clean path inside the move folder", what, p)
	}
	return nil
}

func normalID(id int) bool { return id >= minUserID && id <= maxUserID }

func copyOp(base string, req Request) (safefs.Stats, error) {
	var stats safefs.Stats
	if err := local("source", req.Src); err != nil {
		return stats, err
	}
	if err := local("target", req.Dst); err != nil {
		return stats, err
	}
	if !normalID(req.SrcUID) {
		return stats, fmt.Errorf("old owner %d is not a normal user", req.SrcUID)
	}
	if !normalID(req.UID) || (req.GID != usersGID && !normalID(req.GID)) {
		return stats, fmt.Errorf("owner %d:%d is not a normal user", req.UID, req.GID)
	}
	if req.Budget <= 0 {
		return stats, errors.New("copy without a space budget")
	}
	src, err := openBelow(filepath.Join(base, Old), req.Src)
	if err != nil {
		return stats, err
	}
	defer src.Close()
	dst, err := openBelow(filepath.Join(base, New), req.Dst)
	if err != nil {
		return stats, err
	}
	defer dst.Close()
	return safefs.Copy(src, dst, safefs.CopyOptions{
		SourceUID: req.SrcUID, UID: req.UID, GID: req.GID, Budget: req.Budget, KeepXattr: KeepXattr,
	})
}

func openBelow(top, rel string) (*safefs.Dir, error) {
	d, err := safefs.OpenDir(top)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.Sub(rel)
}

// lsblk runs lsblk with the arguments oldroot reads. Find refuses output
// over its cap, so one byte more is enough to tell.
func lsblk(ctx context.Context) ([]byte, error) {
	var out bytes.Buffer
	cmd := oldroot.Tool(ctx, oldroot.Lsblk, oldroot.LsblkArgs...)
	cmd.Stdout = &capped{w: &out, left: 1<<20 + 1}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run lsblk: %w", err)
	}
	return out.Bytes(), nil
}

type capped struct {
	w    io.Writer
	left int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.left > 0 {
		n := min(len(p), c.left)
		c.w.Write(p[:n])
		c.left -= n
	}
	return len(p), nil
}

// FindResult lists the old gjallarOS disks (find).
type FindResult struct {
	Disks []oldroot.Part `json:"disks"`
}

func findOp(ctx context.Context) (FindResult, error) {
	raw, err := lsblk(ctx)
	if err != nil {
		return FindResult{}, err
	}
	parts, err := oldroot.Find(raw)
	return FindResult{Disks: parts}, err
}

// oldDisk looks up disk in a fresh lsblk listing, so the helper only ever
// touches a disk it found itself, and returns its root partition and every
// partition on it.
func oldDisk(ctx context.Context, disk string) (oldroot.Part, []string, error) {
	if disk == "" {
		return oldroot.Part{}, nil, errors.New("no disk given")
	}
	raw, err := lsblk(ctx)
	if err != nil {
		return oldroot.Part{}, nil, err
	}
	parts, err := oldroot.Find(raw)
	if err != nil {
		return oldroot.Part{}, nil, err
	}
	for _, p := range parts {
		if p.Disk == disk {
			all, err := oldroot.Partitions(raw, disk)
			return p, all, err
		}
	}
	return oldroot.Part{}, nil, fmt.Errorf("%q is not an old gjallarOS disk on this computer", disk)
}

// CheckResult is what step 3 found without a key (check).
type CheckResult struct {
	Encrypted bool `json:"encrypted"`
	// PINToken: the old disk also unlocks with TPM and PIN (F1 notice).
	PINToken bool `json:"pinToken"`
}

// checkOp sets the old disk read-only before anything reads it and checks
// its LUKS metadata. Step 3.
func checkOp(ctx context.Context, req Request) (CheckResult, error) {
	part, all, err := oldDisk(ctx, req.Disk)
	if err != nil {
		return CheckResult{}, err
	}
	if err := oldroot.SetReadOnly(append([]string{part.Disk}, all...)...); err != nil {
		return CheckResult{}, err
	}
	res := CheckResult{Encrypted: part.Encrypted}
	if !part.Encrypted {
		return res, nil
	}
	cap, err := oldroot.MemoryCap()
	if err != nil {
		return res, err
	}
	h, err := oldroot.CheckEncrypted(ctx, part.Path, cap)
	res.PINToken = h.PINToken
	return res, err
}

// OpenResult is what steps 4 and 5 found on the opened old disk (open).
type OpenResult struct {
	oldroot.Info
	PINToken bool `json:"pinToken"`
	// Prepared tells what became of the record of gjallarctl move
	// prepare on an encrypted old disk: PrepareFound, PrepareNone,
	// PrepareStale or PrepareUnreadable. Empty on an unencrypted disk.
	Prepared string `json:"prepared,omitempty"`
	// PINChecked is true when a fitting record says the startup PIN was
	// checked on the old computer.
	PINChecked bool `json:"pinChecked"`
}

// The states of OpenResult.Prepared.
const (
	PrepareFound      = "found"
	PrepareNone       = "none"
	PrepareStale      = "stale"
	PrepareUnreadable = "unreadable"
)

// preparedRecord is where move prepare leaves its record, seen from the
// old disk's top level.
const preparedRecord = "@" + prepared.Dir + "/" + prepared.Name

// readPrepared reads the move prepare record on the opened old disk and
// checks it against the header the disk was opened with. The record only
// changes what the stick says, so any trouble with it counts as no record.
func readPrepared(top *safefs.Dir, digest string) (state string, pin bool) {
	raw, err := top.ReadFile(preparedRecord, prepared.MaxSize)
	if errors.Is(err, os.ErrNotExist) {
		return PrepareNone, false
	}
	if err != nil {
		return PrepareUnreadable, false
	}
	r, err := prepared.Parse(raw)
	if err != nil {
		return PrepareUnreadable, false
	}
	if !r.Fits(digest) {
		return PrepareStale, false
	}
	return PrepareFound, r.PIN
}

// openOp unlocks the old disk if it is encrypted, mounts its top level
// read-only at Old and checks that it holds a gjallarOS. Steps 4 and 5.
// Anything that fails after the unlock closes the disk again.
func openOp(ctx context.Context, base string, req Request) (res OpenResult, err error) {
	if len(req.Key) > maxKey {
		return res, fmt.Errorf("key is over %d bytes", maxKey)
	}
	// First: an open old disk counts as in use, so Find would not list it.
	old := filepath.Join(base, Old)
	if err := oldroot.Free(old); err != nil {
		return res, err
	}
	part, all, err := oldDisk(ctx, req.Disk)
	if err != nil {
		return res, err
	}
	if part.Encrypted == (len(req.Key) == 0) {
		return res, errors.New("a key goes with an encrypted disk and only with one")
	}
	// Set again: the disk may have been plugged out and in since check.
	if err := oldroot.SetReadOnly(append([]string{part.Disk}, all...)...); err != nil {
		return res, err
	}
	dev := part.Path
	var digest string
	if part.Encrypted {
		cap, err := oldroot.MemoryCap()
		if err != nil {
			return res, err
		}
		prep, err := oldroot.Prepare(ctx, base, part.Path, cap)
		if err != nil {
			return res, err
		}
		err = prep.Open(ctx, bytes.NewReader(req.Key))
		prep.Remove()
		if err != nil {
			return res, err
		}
		res.PINToken, digest = prep.Header.PINToken, prep.Digest
		if dev, err = oldroot.MappedDevice(part.Path); err == nil {
			// The mapping sits on the read-only partition; mark it as well.
			err = oldroot.SetReadOnly(dev)
		}
		if err != nil {
			return res, errors.Join(err, oldroot.Close(ctx, old))
		}
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, oldroot.Close(ctx, old))
		}
	}()
	if err := oldroot.Mount(dev, old); err != nil {
		return res, err
	}
	top, err := safefs.OpenDir(old)
	if err != nil {
		return res, err
	}
	defer top.Close()
	if res.Info, err = oldroot.Inspect(top); err != nil {
		return res, err
	}
	if part.Encrypted {
		res.Prepared, res.PINChecked = readPrepared(top, digest)
	}
	return res, nil
}

// InventoryResult counts what of this computer's identity the open old disk
// holds (step 6, move.identity.choice). Counts only: no name, address, key
// or id leaves the helper.
type InventoryResult struct {
	identity.Inventory
}

// inventoryOp looks through the old disk that open left mounted at Old.
// Bluetooth pairings count as this computer's when the old disk's adapter
// address is one of the USB Bluetooth adapters here.
func inventoryOp(ctx context.Context, base string) (res InventoryResult, err error) {
	top, info, err := openedTop(base)
	if err != nil {
		return res, err
	}
	defer top.Close()
	adapters := map[string]bool{}
	for _, a := range (btaddr.Sysfs{Root: "/sys"}).Addresses(ctx) {
		adapters[a] = true
	}
	res.Inventory = identity.Scan(top, info.Username, adapters)
	return res, nil
}

// SizesResult is the size of each category on the open old disk (step 6,
// for the free-space check of step 8). Bytes and counts only.
type SizesResult struct {
	freespace.Sizes
}

// sizesOp measures the old disk that open left mounted at Old. No file is
// opened but the Nextcloud client config; links are not followed.
func sizesOp(base string) (res SizesResult, err error) {
	top, info, err := openedTop(base)
	if err != nil {
		return res, err
	}
	defer top.Close()
	res.Sizes, err = freespace.Measure(top, info.Username)
	return res, err
}

// openedTop opens the top level of the old disk that open mounted at Old
// and inspects it again, for the user name: the files below are read for
// that user.
func openedTop(base string) (*safefs.Dir, oldroot.Info, error) {
	old := filepath.Join(base, Old)
	if err := oldroot.Opened(old); err != nil {
		return nil, oldroot.Info{}, err
	}
	top, err := safefs.OpenDir(old)
	if err != nil {
		return nil, oldroot.Info{}, err
	}
	info, err := oldroot.Inspect(top)
	if err != nil {
		top.Close()
		return nil, oldroot.Info{}, err
	}
	return top, info, nil
}

// oldNix is the old disk's @nix subvolume below the opened top level. It
// must be a folder, not a link: the unit's bind mount would follow a link
// to anywhere on the stick. The old disk is mounted read-only, so the
// folder checked is the folder bound.
const oldNix = "@nix"

// oldSystem finds the old disk's current system, and the folder of the old
// @nix subvolume for the unit's bind mount.
func oldSystem(base string) (sys, nixDir string, err error) {
	top, _, err := openedTop(base)
	if err != nil {
		return "", "", err
	}
	defer top.Close()
	nix, err := top.Sub(oldNix)
	if err != nil {
		return "", "", fmt.Errorf("old @nix: %w", err)
	}
	defer nix.Close()
	sys, err = storecopy.OldSystem(nix)
	if err != nil {
		return "", "", err
	}
	return sys, filepath.Join(base, Old, oldNix), nil
}

// maxUnitErr caps what of a unit's error output reaches the screen.
const maxUnitErr = 2048

// runUnit starts one confined nix run (storecopy) and waits for it. Its
// output is kept up to maxOut bytes, one more to tell an overflow.
func runUnit(ctx context.Context, args []string, maxOut int) ([]byte, error) {
	var out, stderr bytes.Buffer
	cmd := oldroot.Tool(ctx, storecopy.SystemdRun, args...)
	cmd.Stdout = &capped{w: &out, left: maxOut + 1}
	cmd.Stderr = &capped{w: &stderr, left: maxUnitErr}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %q", err, stderr.String())
	}
	if out.Len() > maxOut {
		return nil, fmt.Errorf("the unit printed over %d bytes", maxOut)
	}
	return out.Bytes(), nil
}

// ClosureResult is the old system and the size of everything it needs, for
// the free-space check of step 8 (closure).
type ClosureResult struct {
	System string `json:"system"`
	Bytes  int64  `json:"bytes"`
}

// closureOp measures the old system's closure in the old store, read in a
// confined unit: root does not read the old store as a store.
func closureOp(ctx context.Context, base string) (res ClosureResult, err error) {
	sys, nixDir, err := oldSystem(base)
	if err != nil {
		return res, err
	}
	args, err := storecopy.ClosureArgs(nixDir, sys)
	if err != nil {
		return res, err
	}
	out, err := runUnit(ctx, args, storecopy.MaxClosureOutput)
	if err != nil {
		return res, fmt.Errorf("closure size: %w", err)
	}
	res.Bytes, err = storecopy.ParseClosure(out, sys)
	res.System = sys
	return res, err
}

// ExportResult is what the store export left on the new disk (export). An
// export that failed or was refused leaves nothing and says why in
// Skipped: the install then downloads and builds everything, as without
// an old disk.
type ExportResult struct {
	storecopy.Stats
	Skipped string `json:"skipped,omitempty"`
}

// exportOp exports the old system's closure into a cache folder on the new
// disk (L62). A confined unit writes it, root takes it over and checks it;
// what does not pass is removed.
func exportOp(ctx context.Context, base string, req Request) (res ExportResult, err error) {
	if req.Budget <= 0 {
		return res, errors.New("export: no budget")
	}
	sys, nixDir, err := oldSystem(base)
	if err != nil {
		return res, err
	}
	newRoot, err := mountedNew(base)
	if err != nil {
		return res, err
	}
	defer newRoot.Close()
	cache, err := newRoot.Mkdir(storecopy.CacheName, 0o777)
	if err != nil {
		return res, fmt.Errorf("export: %w", err)
	}
	defer cache.Close()
	args, err := storecopy.ExportArgs(nixDir, filepath.Join(base, New, storecopy.CacheName), sys)
	if err == nil {
		_, err = runUnit(ctx, args, maxUnitErr)
	}
	if err == nil {
		res.Stats, err = storecopy.Settle(cache, 0, 0, req.Budget)
	}
	if err != nil {
		res = ExportResult{Skipped: err.Error()}
		if rmErr := removeCache(ctx, newRoot); rmErr != nil {
			return res, fmt.Errorf("export: %v; removing the copy: %w", err, rmErr)
		}
	}
	return res, nil
}

// dropCache removes the cache folder once the install no longer needs it
// (dropcache). Gone already is fine.
func dropCache(ctx context.Context, base string) error {
	newRoot, err := mountedNew(base)
	if err != nil {
		return err
	}
	defer newRoot.Close()
	return removeCache(ctx, newRoot)
}

// removeCache stops the units first: a unit left from a killed helper
// would go on writing while the folder is removed (L135). A unit that does
// not stop keeps the cache where it is.
func removeCache(ctx context.Context, newRoot *safefs.Dir) error {
	if err := stopUnits(ctx); err != nil {
		return err
	}
	return newRoot.RemoveTree(storecopy.CacheName, storecopy.MaxPaths+4)
}

// stopUnits stops every confined unit of the move and waits until each has
// ended (L135). A unit that is not loaded is fine. It is a plain function,
// never a variable: a package-level value that leads to a program is what
// the exec guard refuses (L137).
func stopUnits(ctx context.Context) error {
	for _, u := range storecopy.Units {
		var stderr bytes.Buffer
		cmd := oldroot.Tool(ctx, storecopy.Systemctl, "stop", "--", u)
		cmd.Stderr = &capped{w: &stderr, left: maxUnitErr}
		err := cmd.Run()
		if err != nil && oldroot.ExitCode(err) == storecopy.NotLoaded {
			continue
		}
		if err != nil {
			return fmt.Errorf("stopping %s: %w: %q", u, err, stderr.String())
		}
	}
	return nil
}

// mountedNew opens the new root, which must be a mount: without one the
// cache would fill the stick's memory.
func mountedNew(base string) (*safefs.Dir, error) {
	p := filepath.Join(base, New)
	mounted, err := oldroot.MountPoint(p)
	if err != nil {
		return nil, err
	}
	if !mounted {
		return nil, errors.New("the new disk is not mounted")
	}
	return safefs.OpenDir(p)
}

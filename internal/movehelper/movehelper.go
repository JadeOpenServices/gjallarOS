// Package movehelper is the root side of the disk move (345). The move runs
// on the USB stick, whose live login on the other consoles can change /mnt
// and its own files (L58). Its user id lies outside the ids of installed
// users (Q254, CheckLiveUsers), and root work still never happens below a
// folder the live login can change: the helper keeps
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
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/freespace"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/identity"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/prepared"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/repocopy"
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
	// nixbldGID and nixbldUID+1 to nixbldUID+maxNixbld are the stick's Nix
	// build accounts (NixOS ids.uids.nixbld, ids.gids.nixbld). They lie in
	// the normal range, so the helper never copies to those ids (L152).
	nixbldGID = 30000
	nixbldUID = 30000
	maxNixbld = 32
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
	// Sync copies the Nextcloud sync folders with the home (home); without
	// it they stay on the old disk.
	Sync bool `json:"sync,omitempty"`
	// Budget is the most bytes a copy or the store export may use on the
	// new disk (home, repo, export).
	Budget int64 `json:"budget,omitempty"`
	// Disk is the old disk as find listed it (check, open).
	Disk string `json:"disk,omitempty"`
	// Key unlocks an encrypted old disk, exactly these bytes (open).
	Key []byte `json:"key,omitempty"`
	// Keep lists the identity items the user keeps (identity); every other
	// item is made new.
	Keep []identity.Item `json:"keep,omitempty"`
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
	case "home", "check", "open", "inventory", "sizes", "config", "close", "closure", "export", "dropcache", "repo", "attach", "detach", "identity":
		var unlock func()
		if unlock, err = lock(base); err != nil {
			break
		}
		defer unlock()
		switch req.Op {
		case "home":
			result, err = homeOp(base, req)
		case "identity":
			result, err = identityOp(base, req)
		case "check":
			result, err = checkOp(ctx, req)
		case "open":
			result, err = openOp(ctx, base, req)
		case "inventory":
			result, err = inventoryOp(ctx, base)
		case "sizes":
			result, err = sizesOp(base)
		case "config":
			result, err = configOp(base)
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
		case "repo":
			result, err = repoOp(ctx, base, req)
		case "attach":
			err = attachOp(base)
			result = struct{}{}
		case "detach":
			err = detachOp(base)
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

// prepareBase is isolateBase. Unit tests that run as a plain user, who
// cannot mount, swap it; the namespace test runs the real one.
var prepareBase = isolateBase

// lock makes the helper folders and holds base locked, so a second helper
// run (from another console) waits its turn instead of closing or reusing
// what this one opened. A held lock refuses at once rather than queueing.
// Base is then a mount with a peer group of its own (L153, L154).
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
	// Locked first: two helpers must not both bind base on itself.
	if err := prepareBase(base); err != nil {
		unix.Close(fd)
		return nil, err
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

// normalID accepts the id of an installed system's user or group. The ids
// the live stick gives its Nix build accounts are refused: a build runs as
// one of them and must never own what the helper writes (L152).
func normalID(id int) bool {
	return id >= minUserID && id <= maxUserID && (id < nixbldUID || id > nixbldUID+maxNixbld)
}

// HomeResult is what the home copy did. A copy that went wrong is taken
// back and Failed says why: the move goes on without the home.
type HomeResult struct {
	safefs.Stats
	Failed string `json:"failed,omitempty"`
}

// maxHomeNames caps the entries per folder when a failed copy is taken
// back; the copy itself made every one of them.
const maxHomeNames = 1 << 20

// homeOp copies the old account's home into the new account's (R49). The
// helper picks both ends itself: @home/<user> on the old disk, for the user
// its settings name, and home/<user> on the new root, the home
// nixos-install made for the same user. The caller chooses only whether the
// Nextcloud sync folders come along and the budget.
func homeOp(base string, req Request) (res HomeResult, err error) {
	if req.Budget <= 0 {
		return res, errors.New("home: no budget")
	}
	top, info, err := openedTop(base)
	if err != nil {
		return res, err
	}
	defer top.Close()
	old, err := freespace.OpenHome(top, info.Username)
	if err != nil {
		return res, err
	}
	defer old.Close()
	newRoot, err := mountedNew(base)
	if err != nil {
		return res, err
	}
	defer newRoot.Close()
	dst, uid, err := newHome(newRoot, info.Username)
	if err != nil {
		return res, err
	}
	defer dst.Close()
	return copyHome(old, dst, uid, usersGID, req.Sync, req.Budget)
}

// newHome opens home/<username> on the new root, as installedHome does,
// and refuses it unless it is empty: what nixos-install made, nothing put
// there since.
func newHome(newRoot *safefs.Dir, username string) (*safefs.Dir, int, error) {
	d, uid, err := installedHome(newRoot, username)
	if err != nil {
		return nil, 0, err
	}
	if _, err = d.Names(".", 0); errors.Is(err, safefs.ErrTooMany) {
		err = fmt.Errorf("the new home of %s is not empty", username)
	}
	if err != nil {
		d.Close()
		return nil, 0, err
	}
	return d, uid, nil
}

// installedHome opens home/<username> on the new root. home is the @home
// mount, entered on its own (L156) and checked to be the new disk's @home.
// The folder must be a plain folder of a normal user in the users group,
// never a Nix build account (L152). The checks are made on the opened
// folder, so it cannot be swapped after them.
func installedHome(newRoot *safefs.Dir, username string) (*safefs.Dir, int, error) {
	if !config.ValidUsername(username) {
		return nil, 0, fmt.Errorf("%q is not a valid user name", username)
	}
	homes, err := newRoot.SubMount("home")
	if err != nil {
		return nil, 0, err
	}
	defer homes.Close()
	if err := onSubvolume(newRoot, homes, "@home"); err != nil {
		return nil, 0, err
	}
	if st, err := homes.Stat(); err != nil {
		return nil, 0, err
	} else if st.Uid != rootUID || st.Mode&0o022 != 0 {
		return nil, 0, errors.New("home on the new disk is not root's alone")
	}
	d, err := homes.Sub(username)
	if err != nil {
		return nil, 0, err
	}
	st, err := d.Stat()
	switch {
	case err != nil:
	case !normalID(int(st.Uid)):
		err = fmt.Errorf("the new home of %s belongs to user %d, not a normal user", username, st.Uid)
	case st.Gid != usersGID:
		err = fmt.Errorf("the new home of %s is in group %d, not users", username, st.Gid)
	}
	if err != nil {
		d.Close()
		return nil, 0, err
	}
	return d, int(st.Uid), nil
}

// copyHome copies old into dst for uid:gid; dst is empty. Without sync the
// sync folders stay behind, the same ones the size check measured on their
// own. A failed copy is taken back whole.
func copyHome(old *freespace.OldHome, dst *safefs.Dir, uid, gid int, sync bool, budget int64) (HomeResult, error) {
	opt := safefs.CopyOptions{SourceUID: old.Owner, UID: uid, GID: gid, Budget: budget, KeepXattr: KeepXattr}
	if !sync {
		opt.Skip = old.Skip()
	}
	stats, err := safefs.Copy(old.Dir, dst, opt)
	res := HomeResult{Stats: stats}
	if err == nil {
		return res, nil
	}
	res.Failed = err.Error()
	names, lerr := dst.Names(".", -1)
	if lerr != nil {
		return res, fmt.Errorf("home copy failed (%v); taking it back: %w", err, lerr)
	}
	for _, n := range names {
		if rerr := dst.RemoveTree(n, maxHomeNames); rerr != nil {
			return res, fmt.Errorf("home copy failed (%v); taking it back: %w", err, rerr)
		}
	}
	return res, nil
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
	if err := checkBase(base); err != nil {
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

// ConfigResult is the old system's settings for the new install (step 10,
// L65): user.config.json after the installer's schema check, with the
// installer keys of a fresh install. It is the typed settings written out
// again, never the old file's bytes.
type ConfigResult struct {
	User config.User `json:"user"`
}

// maxConfigResult stays below the installer's cap on the helper's answer.
const maxConfigResult = 60 << 10

// configOp hands the checked settings of the old disk that open left
// mounted at Old to the installer.
func configOp(base string) (res ConfigResult, err error) {
	top, info, err := openedTop(base)
	if err != nil {
		return res, err
	}
	top.Close()
	res.User = info.User
	b, err := json.Marshal(res)
	if err != nil {
		return ConfigResult{}, err
	}
	if len(b) > maxConfigResult {
		return ConfigResult{}, errors.New("settings of the old disk are too large")
	}
	return res, nil
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
// gitUnitArgs runs one of root's git steps in its own unit (L141): root
// without capabilities, nothing writable but the new checkout's git folder,
// which newGitDir found root's and plain all the way down. The unit is
// stopped with the others by stopUnits.
func gitUnitArgs(base string, args []string) ([]string, error) {
	return storecopy.RootUnitArgs(storecopy.GitUnit, storecopy.GitLimit,
		filepath.Join(base, New, newCheckout, ".git"), repocopy.GitEnv,
		append([]string{repocopy.Git}, args...)...)
}

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

// newDiskFS is the file system the installer makes on the new disk
// (rootprovision). Tests, which cannot make btrfs, set tmpfs.
var newDiskFS = "btrfs"

// onSubvolume refuses unless d, opened with SubMount, is the subvolume
// subvol ("@home", "@config") of the file system the new root is on, and
// the new root is that file system's @ (L156 H2). Being a mount root alone
// lets any mount pass: a bind of another folder, of the new root's own etc,
// or of another disk at home or at the checkout. Both ends are found in
// the mount table by the mount id of the open handle, so a mount moved
// after the check is not the one checked.
func onSubvolume(newRoot, d *safefs.Dir, subvol string) error {
	rootID, err := newRoot.MountID()
	if err != nil {
		return err
	}
	id, err := d.MountID()
	if err != nil {
		return err
	}
	mounts, err := mountTable()
	if err != nil {
		return err
	}
	var r, m *mountEntry
	for i := range mounts {
		switch mounts[i].id {
		case rootID:
			r = &mounts[i]
		case id:
			m = &mounts[i]
		}
	}
	switch {
	case r == nil || m == nil:
		return fmt.Errorf("%s: the mount is not in the mount table", d.Name())
	case r.fstype != newDiskFS || r.root != "/@":
		return errors.New("the new disk is not mounted as the installer mounts it")
	case m.fstype != r.fstype || m.dev != r.dev || m.source != r.source:
		return fmt.Errorf("%s is not on the new disk", d.Name())
	case m.root != "/"+subvol:
		return fmt.Errorf("%s is not the %s subvolume of the new disk", d.Name(), subvol)
	}
	return nil
}

// oldRepo is the old system checkout's git folder below the opened top
// level: the @config subvolume is the checkout.
const oldRepo = "@config/.git"

// newCheckout is systemcheckout.Path below the new root. The helper does
// not link systemcheckout, which starts programs of its own; a test keeps
// the two the same.
const newCheckout = "etc/gjallar/system"

// repoOp carries the local branches, stashes and detached HEAD of the old
// system checkout into the new one (L63). Root runs git only in the new
// checkout; the old objects are packed by a confined unit (repocopy). A
// copy that fails takes back what it wrote and says why in Failed.
func repoOp(ctx context.Context, base string, req Request) (res repocopy.Result, err error) {
	if req.Budget <= 0 {
		return res, errors.New("repo: no budget")
	}
	top, _, err := openedTop(base)
	if err != nil {
		return res, err
	}
	defer top.Close()
	gitDir, refused := oldGitDir(top)
	if refused != "" {
		return repocopy.Result{Refused: refused}, nil
	}
	defer gitDir.Close()
	newRoot, err := mountedNew(base)
	if err != nil {
		return res, err
	}
	defer newRoot.Close()
	if err := newGitDir(newRoot); err != nil {
		return res, err
	}
	host := repocopy.Host{
		Pack: func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
			return runPiped(ctx, storecopy.SystemdRun, nil, args, stdin, stdout)
		},
		Git: func(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
			unit, err := gitUnitArgs(base, args)
			if err != nil {
				return err
			}
			return runPiped(ctx, storecopy.SystemdRun, nil, unit, stdin, stdout)
		},
	}
	return repocopy.Copy(ctx, host, gitDir, filepath.Join(base, Old, oldRepo, "objects"),
		filepath.Join(base, New, newCheckout), req.Budget)
}

// oldGitDir opens the old checkout's git folder. Every folder on the way is
// opened without following links, on the old disk mounted read-only: the
// folder checked is the folder the pack unit binds. No checkout is not an
// error, the move just has nothing to carry.
func oldGitDir(top *safefs.Dir) (*safefs.Dir, string) {
	gitDir, err := top.Sub(oldRepo)
	if errors.Is(err, unix.ENOENT) {
		return nil, "no-repo"
	}
	if err != nil {
		return nil, "path"
	}
	return gitDir, ""
}

// newGitDir checks the new checkout before root's git runs in it (L139).
// Every folder on the way from the new root and everything in the git
// folder is root's and writable by nobody else, with no link, pipe or
// device in it, and nothing that sends git to objects or history kept
// somewhere else. Git reads all of these as root; a copy cannot write
// there (homeTarget), and this holds even if something else did.
func newGitDir(newRoot *safefs.Dir) error {
	at := ""
	for _, part := range strings.Split(newCheckout, "/") {
		at = path.Join(at, part)
		st, err := newRoot.Lstat(at)
		if err != nil {
			return fmt.Errorf("repo: the new checkout: %w", err)
		}
		if err := rootsOnly(at, st, true); err != nil {
			return err
		}
	}
	// The checkout is the @config subvolume, mounted there by the installer:
	// the only step that may cross into another file system.
	co, err := newRoot.SubMount(newCheckout)
	if err != nil {
		return fmt.Errorf("repo: the new checkout: %w", err)
	}
	defer co.Close()
	if err := onSubvolume(newRoot, co, "@config"); err != nil {
		return fmt.Errorf("repo: the new checkout: %w", err)
	}
	return checkoutGitDir(co)
}

// checkoutGitDir checks .git in the opened new checkout.
func checkoutGitDir(co *safefs.Dir) error {
	st, err := co.Lstat(".")
	if err != nil {
		return fmt.Errorf("repo: the new checkout: %w", err)
	}
	if err := rootsOnly(newCheckout, st, true); err != nil {
		return err
	}
	st, err = co.Lstat(".git")
	if err != nil {
		return fmt.Errorf("repo: the new checkout: %w", err)
	}
	if err := rootsOnly(newCheckout+"/.git", st, true); err != nil {
		return err
	}
	d, err := co.Sub(".git")
	if err != nil {
		return fmt.Errorf("repo: the new checkout: %w", err)
	}
	defer d.Close()
	for _, r := range gitRedirects {
		if _, err := d.Lstat(r); !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("repo: the new checkout has .git/%s", r)
		}
	}
	left := maxGitEntries
	return walkRoots(d, ".", 0, &left)
}

// gitRedirects make git read objects, refs or history from another place.
var gitRedirects = []string{
	"commondir", "objects/info/alternates", "objects/info/http-alternates", "info/grafts",
}

const (
	// maxGitEntries caps the walk of the new .git: a fresh checkout has a
	// few hundred entries.
	maxGitEntries = 1 << 16
	maxGitDepth   = 32
)

// rootUID owns the new checkout; tests run as their own user. rootGID is
// the group of the files root writes on the new root.
var rootUID, rootGID uint32

func rootsOnly(rel string, st unix.Stat_t, dir bool) error {
	switch {
	case st.Uid != rootUID:
		return fmt.Errorf("repo: %s in the new checkout belongs to user %d", rel, st.Uid)
	case st.Mode&0o022 != 0:
		return fmt.Errorf("repo: %s in the new checkout is writable by others", rel)
	case dir && st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("repo: %s in the new checkout is not a folder", rel)
	case !dir && st.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("repo: %s in the new checkout is not a plain file", rel)
	}
	return nil
}

func walkRoots(d *safefs.Dir, rel string, depth int, left *int) error {
	if depth >= maxGitDepth {
		return fmt.Errorf("repo: .git/%s is too deep", rel)
	}
	names, err := d.Names(rel, *left)
	if err != nil {
		return fmt.Errorf("repo: the new checkout: %w", err)
	}
	*left -= len(names)
	for _, n := range names {
		p := path.Join(rel, n)
		st, err := d.Lstat(p)
		if err != nil {
			return fmt.Errorf("repo: the new checkout: %w", err)
		}
		dir := st.Mode&unix.S_IFMT == unix.S_IFDIR
		if err := rootsOnly(".git/"+p, st, dir); err != nil {
			return err
		}
		if dir {
			if err := walkRoots(d, p, depth+1, left); err != nil {
				return err
			}
		}
	}
	return nil
}

// runPiped runs one fixed program with stdin and stdout passed through and
// its error output capped. It is a plain function, never a variable (L137).
func runPiped(ctx context.Context, prog string, env, args []string, stdin io.Reader, stdout io.Writer) error {
	var stderr bytes.Buffer
	cmd := oldroot.Tool(ctx, prog, args...)
	cmd.Env = slices.Concat(cmd.Env, env)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	cmd.Stderr = &capped{w: &stderr, left: maxUnitErr}
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %q", err, stderr.String())
	}
	return nil
}

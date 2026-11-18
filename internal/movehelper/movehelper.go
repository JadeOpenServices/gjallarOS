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
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
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
	// Budget is the most bytes the copy may use on the new disk (copy).
	Budget int64 `json:"budget,omitempty"`
	// Disk is the old disk as find listed it (check, open).
	Disk string `json:"disk,omitempty"`
	// Key unlocks an encrypted old disk, exactly these bytes (open).
	Key []byte `json:"key,omitempty"`
}

// refusalResult is what a refused step returns. It is an answer for the
// screen, not a failure of the helper.
type refusalResult struct {
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
	case "copy", "check", "open", "close":
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
		case "close":
			err = oldroot.Close(ctx, filepath.Join(base, Old))
			result = struct{}{}
		}
	default:
		return fmt.Errorf("unknown operation %q", req.Op)
	}
	if r, ok := oldroot.AsRefusal(err); ok {
		result, err = refusalResult{r}, nil
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
	cmd := exec.CommandContext(ctx, "lsblk", oldroot.LsblkArgs...)
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

type findResult struct {
	Disks []oldroot.Part `json:"disks"`
}

func findOp(ctx context.Context) (findResult, error) {
	raw, err := lsblk(ctx)
	if err != nil {
		return findResult{}, err
	}
	parts, err := oldroot.Find(raw)
	return findResult{Disks: parts}, err
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

type checkResult struct {
	Encrypted bool `json:"encrypted"`
	// PINToken: the old disk also unlocks with TPM and PIN (F1 notice).
	PINToken bool `json:"pinToken"`
}

// checkOp sets the old disk read-only before anything reads it and checks
// its LUKS metadata. Step 3.
func checkOp(ctx context.Context, req Request) (checkResult, error) {
	part, all, err := oldDisk(ctx, req.Disk)
	if err != nil {
		return checkResult{}, err
	}
	if err := oldroot.SetReadOnly(append([]string{part.Disk}, all...)...); err != nil {
		return checkResult{}, err
	}
	res := checkResult{Encrypted: part.Encrypted}
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

type openResult struct {
	oldroot.Info
	PINToken bool `json:"pinToken"`
}

// openOp unlocks the old disk if it is encrypted, mounts its top level
// read-only at Old and checks that it holds a gjallarOS. Steps 4 and 5.
// Anything that fails after the unlock closes the disk again.
func openOp(ctx context.Context, base string, req Request) (res openResult, err error) {
	if len(req.Key) > maxKey {
		return res, fmt.Errorf("key is over %d bytes", maxKey)
	}
	part, all, err := oldDisk(ctx, req.Disk)
	if err != nil {
		return res, err
	}
	if part.Encrypted == (len(req.Key) == 0) {
		return res, errors.New("a key goes with an encrypted disk and only with one")
	}
	old := filepath.Join(base, Old)
	if err := oldroot.Free(old); err != nil {
		return res, err
	}
	// Set again: the disk may have been plugged out and in since check.
	if err := oldroot.SetReadOnly(append([]string{part.Disk}, all...)...); err != nil {
		return res, err
	}
	dev := part.Path
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
		res.PINToken = prep.Header.PINToken
		dev = "/dev/mapper/" + oldroot.Mapping
		// The mapping sits on the read-only partition; mark it as well.
		if err := oldroot.SetReadOnly(dev); err != nil {
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
	res.Info, err = oldroot.Inspect(top)
	return res, err
}

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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
}

// Run reads one request from in and carries it out below base. The result
// goes to out as JSON.
func Run(base string, in io.Reader, out io.Writer) error {
	req, err := readRequest(in)
	if err != nil {
		return err
	}
	var result any
	switch req.Op {
	case "dirs":
		err = Dirs(base)
		result = struct{}{}
	case "copy":
		result, err = copyOp(base, req)
	default:
		return fmt.Errorf("unknown operation %q", req.Op)
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
	if err := Dirs(base); err != nil {
		return stats, err
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

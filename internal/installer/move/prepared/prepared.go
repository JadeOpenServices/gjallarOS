// Package prepared is the record "gjallarctl move prepare" leaves on the old
// system: the startup PIN was checked there, on the computer whose chip can
// check it (345, Finding F1). The stick reads it after the unlock and skips
// the PIN notice when it fits the disk.
//
// The record is a convenience, not proof. Anyone who can unlock the disk can
// also write it, and the same key already reads everything on it. It holds
// no key, no PIN and no name: a version, a time, one yes/no and the digest
// of the encryption header it was made for, so a PIN or TPM change after the
// prepare makes it stale.
package prepared

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// Dir is the folder of the record on the system it was made on.
	Dir = "/var/lib/gjallarOS"
	// Name is the file name of the record inside Dir.
	Name = "move-prepared"
	// Version is the only record version this package reads and writes.
	Version = 1
	// MaxSize caps the record. A real one is under 200 bytes.
	MaxSize = 4096
)

// Record is what move prepare checked.
type Record struct {
	Version int       `json:"version"`
	Time    time.Time `json:"time"`
	// PIN is true when the startup PIN was checked by the chip.
	PIN bool `json:"pin"`
	// Header is the hex SHA-256 of the LUKS metadata area the checks ran
	// against, cut at the data start (oldroot.AreaDigest).
	Header string `json:"header"`
}

// Parse reads a record strictly: no unknown field, no second value, the
// known version and a well-formed digest.
func Parse(raw []byte) (Record, error) {
	var r Record
	if len(raw) > MaxSize {
		return r, errors.New("move-prepared is over the size cap")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Record{}, fmt.Errorf("move-prepared is not valid: %w", err)
	}
	if dec.More() {
		return Record{}, errors.New("move-prepared has more than one value")
	}
	if r.Version != Version {
		return Record{}, fmt.Errorf("move-prepared has version %d", r.Version)
	}
	if !validDigest(r.Header) {
		return Record{}, errors.New("move-prepared has no valid header digest")
	}
	if r.Time.IsZero() {
		return Record{}, errors.New("move-prepared has no time")
	}
	return r, nil
}

// Fits tells whether the record was made for the header with this digest.
func (r Record) Fits(digest string) bool {
	return validDigest(digest) && r.Header == digest
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == string(bytes.ToLower([]byte(s)))
}

// ownerUID is the owner the folder must have; tests change it.
var ownerUID = uint32(0)

// Write stores r as Name inside dir, replacing an older record. dir must be
// a real folder (not a link) owned by root that only root can write to. The
// new record is written to a fresh file that must not exist yet and then
// renamed over the old one, so a link, a pipe or anything else planted at
// either name is never opened or followed.
func Write(dir string, r Record) error {
	if r.Version != Version || !validDigest(r.Header) || r.Time.IsZero() {
		return errors.New("move-prepared: incomplete record")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	dirFD, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer unix.Close(dirFD)
	var st unix.Stat_t
	if err := unix.Fstat(dirFD, &st); err != nil {
		return fmt.Errorf("check %s: %w", dir, err)
	}
	if st.Uid != ownerUID || st.Mode&0o022 != 0 {
		return fmt.Errorf("%s is not a folder only root can write to", dir)
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := "." + Name + "-" + hex.EncodeToString(rnd[:])
	fd, err := unix.Openat(dirFD, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s/%s: %w", dir, tmp, err)
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, werr := f.Write(raw)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = unix.Renameat(dirFD, tmp, dirFD, Name)
	}
	if werr != nil {
		unix.Unlinkat(dirFD, tmp, 0)
		return fmt.Errorf("write %s/%s: %w", dir, Name, werr)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("write %s/%s: %w", dir, Name, err)
	}
	return nil
}

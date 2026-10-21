package settings

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A pairing file (ROADMAP "Pairing and own keys") has the layout of a
// settings file under its own magic line, so neither ever opens as the
// other. It is signed by the key it carries: that only shows the sender
// holds the key. Trust comes from the user comparing PairCode on both
// devices.

const (
	// PairSchema is the payload version SealPairing writes.
	PairSchema = 1
	// MaxPairBytes bounds a pairing file.
	MaxPairBytes = 4 << 10
	// PairIDDigits is how many digits of a device's half name its pairing.
	PairIDDigits = 10

	pairMagic = "gjallaros-pairing-v1\n"
	// pairDomain and pairRounds follow Signal's safety numbers: SHA-512
	// over a version tag, the identity and the key, then more rounds over
	// the hash and the key.
	pairDomain = "gjallaros-pair-v1"
	pairRounds = 5200
	halfBytes  = 30
)

// ErrNotPairingFile says the bytes are not a pairing file.
var ErrNotPairingFile = errors.New("this is not a GjallarOS pairing file")

var deviceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// PairFile is the signed part of a pairing file.
type PairFile struct {
	Schema  int       `json:"schema"`
	Device  string    `json:"device"`
	User    string    `json:"user"`
	UID     uint32    `json:"uid"`
	Created time.Time `json:"created"`
	// Hostname is what the device says it is called. It is shown, never
	// trusted.
	Hostname string `json:"hostname"`
}

// PairSide is one device's account as the pairing code covers it.
type PairSide struct {
	Key    ed25519.PublicKey
	Device string
	UID    uint32
	User   string
}

// PairOffer is what a pairing file or a stored pairing shows the user, as
// the settings service sends it.
type PairOffer struct {
	ID       string    `json:"id"`
	Code     string    `json:"code,omitempty"`
	Hostname string    `json:"hostname"`
	User     string    `json:"user"`
	Added    time.Time `json:"added,omitzero"`
}

// Side returns the account a pairing file names, with the key that signed it.
func (f PairFile) Side(key ed25519.PublicKey) PairSide {
	return PairSide{Key: key, Device: f.Device, UID: f.UID, User: f.User}
}

// SealPairing signs file with key.
func SealPairing(key ed25519.PrivateKey, file PairFile) ([]byte, error) {
	if file.Schema != PairSchema {
		return nil, fmt.Errorf("pairing file schema %d; this version writes %d", file.Schema, PairSchema)
	}
	if err := file.check(); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, err
	}
	out := seal(pairMagic, key, append(payload, '\n'))
	if len(out) > MaxPairBytes {
		return nil, fmt.Errorf("the pairing file would hold %d bytes; at most %d are allowed", len(out), MaxPairBytes)
	}
	return out, nil
}

// OpenPairing checks a pairing file's signature against the key it
// carries, then parses it strictly.
func OpenPairing(data []byte) (PairFile, ed25519.PublicKey, error) {
	if len(data) > MaxPairBytes {
		return PairFile{}, nil, fmt.Errorf("%w: it is larger than %d bytes", ErrNotPairingFile, MaxPairBytes)
	}
	payload, public, err := open(pairMagic, ErrNotPairingFile, data, func(ed25519.PublicKey) bool { return true })
	if err != nil {
		return PairFile{}, nil, err
	}
	if !utf8.Valid(payload) {
		return PairFile{}, nil, errors.New("the pairing file is not UTF-8 text")
	}
	if err := uniqueNames(payload); err != nil {
		return PairFile{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var file PairFile
	if err := decoder.Decode(&file); err != nil {
		return PairFile{}, nil, fmt.Errorf("read the pairing file: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return PairFile{}, nil, errors.New("the pairing file has more after its contents")
	}
	if file.Schema != PairSchema {
		return PairFile{}, nil, fmt.Errorf("the pairing file has version %d; this system reads version %d", file.Schema, PairSchema)
	}
	if err := file.check(); err != nil {
		return PairFile{}, nil, err
	}
	return file, public, nil
}

func (f PairFile) check() error {
	switch {
	case !deviceIDPattern.MatchString(f.Device):
		return errors.New("the pairing file does not name a device")
	case f.Created.IsZero():
		return errors.New("the pairing file does not say when it was made")
	}
	if err := plainName("user name", f.User, 64); err != nil {
		return err
	}
	return plainName("hostname", f.Hostname, 64)
}

// plainName refuses empty, long or control-character text: these names
// end up on a screen next to the code.
func plainName(what, name string, max int) error {
	if name == "" || len(name) > max {
		return fmt.Errorf("the pairing file's %s is empty or longer than %d bytes", what, max)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("the pairing file's %s holds characters that cannot be shown", what)
		}
	}
	return nil
}

// PairHalf is one side's 30 digits, as Signal computes each half of a
// safety number.
func PairHalf(side PairSide) string {
	h := sha512.New()
	h.Write([]byte(pairDomain))
	for _, part := range []string{side.Device, strconv.FormatUint(uint64(side.UID), 10), side.User} {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	h.Write([]byte{0})
	h.Write(side.Key)
	sum := h.Sum(nil)
	for range pairRounds {
		h.Reset()
		h.Write(sum)
		h.Write(side.Key)
		sum = h.Sum(sum[:0])
	}
	var digits strings.Builder
	for i := 0; i < halfBytes; i += 5 {
		var chunk [8]byte
		copy(chunk[3:], sum[i:i+5])
		fmt.Fprintf(&digits, "%05d", binary.BigEndian.Uint64(chunk[:])%100000)
	}
	return digits.String()
}

// PairCode is the 60 digits both devices show, in groups of five. The
// halves are sorted, so it does not matter which side computes it.
func PairCode(a, b PairSide) string {
	first, second := PairHalf(a), PairHalf(b)
	if second < first {
		first, second = second, first
	}
	digits := first + second
	groups := make([]string, 0, len(digits)/5)
	for i := 0; i < len(digits); i += 5 {
		groups = append(groups, digits[i:i+5])
	}
	return strings.Join(groups, " ")
}

// PairID names a pairing by the start of the other device's half.
func PairID(side PairSide) string {
	half := PairHalf(side)
	return half[:5] + "-" + half[5:PairIDDigits]
}

package settings

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"
)

// A settings file (ROADMAP "Signed settings files") is three header lines
// and the settings as JSON:
//
//	gjallaros-settings-v1
//	key <base64 Ed25519 public key>
//	sig <base64 Ed25519 signature>
//	{ ... }
//
// The signature covers every byte except the sig line. Open checks it over
// the raw bytes before anything is parsed, and the key line only names
// which trusted key to check with: a key the caller does not trust is
// refused, whatever the signature says.

const (
	// FileSchema is the payload version Seal writes and Open accepts.
	FileSchema = 1
	// MaxFileBytes bounds a settings file.
	MaxFileBytes = MaxRequestBytes + 4<<10

	fileMagic = "gjallaros-settings-v1\n"
	keyPrefix = "key "
	sigPrefix = "sig "
)

var (
	keyLineLen = len(keyPrefix) + base64.StdEncoding.EncodedLen(ed25519.PublicKeySize) + 1
	sigLineLen = len(sigPrefix) + base64.StdEncoding.EncodedLen(ed25519.SignatureSize) + 1
)

// Errors Open returns before it parses the settings.
var (
	ErrNotSettingsFile = errors.New("this is not a GjallarOS settings file")
	ErrUnknownSigner   = errors.New("this file was not made on this device or a device you paired")
	ErrBadSignature    = errors.New("this file was changed after it was made")
)

// File is the signed part of a settings file.
type File struct {
	Schema int `json:"schema"`
	// Device is the app-specific id of the device that made the file.
	Device  string    `json:"device"`
	User    string    `json:"user"`
	UID     uint32    `json:"uid"`
	Created time.Time `json:"created"`
	// Settings are user.config.json keys with their values.
	Settings map[string]json.RawMessage `json:"settings"`
}

// Seal signs file with key and returns the file's bytes.
func Seal(key ed25519.PrivateKey, file File) ([]byte, error) {
	if file.Schema != FileSchema {
		return nil, fmt.Errorf("settings file schema %d; this version writes %d", file.Schema, FileSchema)
	}
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, err
	}
	out := seal(fileMagic, key, append(payload, '\n'))
	if len(out) > MaxFileBytes {
		return nil, fmt.Errorf("the settings file would hold %d bytes; at most %d are allowed", len(out), MaxFileBytes)
	}
	return out, nil
}

// seal writes magic, the key and sig lines and payload. Pairing files use
// the same layout under another magic line.
func seal(magic string, key ed25519.PrivateKey, payload []byte) []byte {
	public := key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(key, signedBytes(magic, public, payload))
	var out bytes.Buffer
	out.WriteString(magic)
	out.WriteString(keyPrefix + base64.StdEncoding.EncodeToString(public) + "\n")
	out.WriteString(sigPrefix + base64.StdEncoding.EncodeToString(sig) + "\n")
	out.Write(payload)
	return out.Bytes()
}

func signedBytes(magic string, key ed25519.PublicKey, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString(magic)
	b.WriteString(keyPrefix + base64.StdEncoding.EncodeToString(key) + "\n")
	b.Write(payload)
	return b.Bytes()
}

// Open checks a settings file and returns its contents and the key that
// signed it. trusted says whether a key may sign files for this device.
// Only after the signature holds is the payload parsed, strictly: one JSON
// object, no unknown fields, no repeated names, nothing after it.
func Open(data []byte, trusted func(ed25519.PublicKey) bool) (File, ed25519.PublicKey, error) {
	if len(data) > MaxFileBytes {
		return File{}, nil, fmt.Errorf("%w: it is larger than %d bytes", ErrNotSettingsFile, MaxFileBytes)
	}
	payload, public, err := open(fileMagic, ErrNotSettingsFile, data, trusted)
	if err != nil {
		return File{}, nil, err
	}
	file, err := parseFile(payload)
	if err != nil {
		return File{}, nil, err
	}
	return file, public, nil
}

// open checks the header and signature of data under magic and returns the
// payload, still unparsed, and the key that signed it.
func open(magic string, notFile error, data []byte, trusted func(ed25519.PublicKey) bool) ([]byte, ed25519.PublicKey, error) {
	rest, ok := bytes.CutPrefix(data, []byte(magic))
	if !ok || len(rest) < keyLineLen+sigLineLen {
		return nil, nil, notFile
	}
	keyLine, sigLine, payload := rest[:keyLineLen], rest[keyLineLen:keyLineLen+sigLineLen], rest[keyLineLen+sigLineLen:]
	key, err := headerValue(keyLine, keyPrefix, ed25519.PublicKeySize, notFile)
	if err != nil {
		return nil, nil, err
	}
	sig, err := headerValue(sigLine, sigPrefix, ed25519.SignatureSize, notFile)
	if err != nil {
		return nil, nil, err
	}
	public := ed25519.PublicKey(key)
	if !trusted(public) {
		return nil, nil, ErrUnknownSigner
	}
	signed := append(data[:len(magic)+keyLineLen:len(magic)+keyLineLen], payload...)
	if !ed25519.Verify(public, signed, sig) {
		return nil, nil, ErrBadSignature
	}
	return payload, public, nil
}

func headerValue(line []byte, prefix string, size int, notFile error) ([]byte, error) {
	text, ok := bytes.CutPrefix(line, []byte(prefix))
	if !ok || text[len(text)-1] != '\n' {
		return nil, notFile
	}
	value, err := base64.StdEncoding.Strict().DecodeString(string(text[:len(text)-1]))
	if err != nil || len(value) != size {
		return nil, notFile
	}
	return value, nil
}

func parseFile(payload []byte) (File, error) {
	if !utf8.Valid(payload) {
		return File{}, errors.New("the settings in the file are not UTF-8 text")
	}
	if err := uniqueNames(payload); err != nil {
		return File{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return File{}, fmt.Errorf("read the settings in the file: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return File{}, errors.New("the settings file has more after its settings")
	}
	switch {
	case file.Schema != FileSchema:
		return File{}, fmt.Errorf("the settings file has version %d; this system reads version %d", file.Schema, FileSchema)
	case file.Device == "" || file.User == "" || file.Created.IsZero():
		return File{}, errors.New("the settings file does not say where and when it was made")
	case len(file.Settings) == 0:
		return File{}, errors.New("the settings file holds no settings")
	}
	return file, nil
}

// uniqueNames refuses JSON that repeats a name within one object: parsers
// disagree on which of the two counts.
func uniqueNames(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				name, err := decoder.Token()
				if err != nil {
					return err
				}
				key := name.(string)
				if seen[key] {
					return fmt.Errorf("the file names %q twice", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case json.Delim('['):
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return fmt.Errorf("read the file: %w", err)
	}
	return nil
}

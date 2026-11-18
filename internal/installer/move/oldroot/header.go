package oldroot

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// LUKS2 on-disk header (cryptsetup docs/on-disk-format-luks2.pdf): a 4096
// byte binary part followed by the JSON area, written twice. The second
// copy normally starts right after the first; when the first is damaged,
// cryptsetup looks for the second at each possible header size.
const (
	binaryHeaderSize = 4096
	minHeaderSize    = 16 << 10
	maxHeaderSize    = 4 << 20
)

var (
	primaryMagic   = []byte("LUKS\xba\xbe")
	secondaryMagic = []byte("SKUL\xba\xbe")
)

// tokenTypes are the tokens a gjallarOS install writes. With external
// tokens off cryptsetup loads no plugin for any other type, but a type the
// installer never writes means someone else changed the header.
var tokenTypes = map[string]bool{"systemd-tpm2": true, "systemd-recovery": true}

var kdfTypes = map[string]bool{"pbkdf2": true, "argon2i": true, "argon2id": true}

// Header is what the move needs from the old disk's LUKS header.
type Header struct {
	// PINToken is set when a TPM token asks for a PIN (finding F1).
	PINToken bool
	// DataOffset is where the encrypted data starts, in bytes. Everything
	// before it is LUKS metadata.
	DataOffset int64
}

// CheckHeader reads every LUKS2 header copy on dev and checks it before
// cryptsetup sees the disk (L60). Each copy cryptsetup could pick must pass:
// no re-encryption, only the token types gjallarOS writes, and no Argon2
// memory cost over kdfCapKiB.
//
// The JSON is read by this code, not by cryptsetup, and more strictly than
// cryptsetup's json-c reads it: keys are matched exactly, a key may appear
// once, and no string may hold a NUL or a control character. json-c cuts a
// key at a NUL and keeps the last of two equal keys, so a looser reader
// could pass a header that cryptsetup reads differently.
func CheckHeader(dev io.ReaderAt, size, kdfCapKiB int64) (Header, error) {
	var h Header
	seen := 0
	for _, off := range headerOffsets(size) {
		magic := make([]byte, len(primaryMagic))
		if _, err := dev.ReadAt(magic, off); err != nil {
			if off == 0 {
				return h, fmt.Errorf("read the disk header: %w", err)
			}
			continue
		}
		want := secondaryMagic
		if off == 0 {
			want = primaryMagic
		}
		if !bytes.Equal(magic, want) {
			continue
		}
		seen++
		copyHeader, err := checkCopy(dev, off, size, kdfCapKiB)
		if err != nil {
			return h, err
		}
		h.PINToken = h.PINToken || copyHeader.PINToken
		if seen > 1 && copyHeader.DataOffset != h.DataOffset {
			return h, refuse(NotLUKS2, "the header copies disagree on where the data starts")
		}
		h.DataOffset = copyHeader.DataOffset
	}
	if seen == 0 {
		return h, refuse(NotLUKS2, "no LUKS2 header")
	}
	return h, nil
}

// headerOffsets lists where a header copy can start: the start of the
// disk and every place cryptsetup searches for the second copy.
func headerOffsets(size int64) []int64 {
	offs := []int64{0}
	for o := int64(minHeaderSize); o <= maxHeaderSize; o *= 2 {
		if o+binaryHeaderSize <= size {
			offs = append(offs, o)
		}
	}
	return offs
}

func checkCopy(dev io.ReaderAt, off, size, kdfCapKiB int64) (Header, error) {
	var h Header
	bin := make([]byte, binaryHeaderSize)
	if _, err := dev.ReadAt(bin, off); err != nil {
		return h, refuse(NotLUKS2, fmt.Sprintf("header copy at %d cannot be read: %v", off, err))
	}
	if v := binary.BigEndian.Uint16(bin[6:8]); v != 2 {
		return h, refuse(NotLUKS2, fmt.Sprintf("header version %d", v))
	}
	hdrSize := binary.BigEndian.Uint64(bin[8:16])
	if hdrSize < minHeaderSize || hdrSize > maxHeaderSize || hdrSize&(hdrSize-1) != 0 {
		return h, refuse(NotLUKS2, fmt.Sprintf("header size %d", hdrSize))
	}
	if off+int64(hdrSize) > size {
		return h, refuse(NotLUKS2, "header runs past the end of the partition")
	}
	area := make([]byte, hdrSize-binaryHeaderSize)
	if _, err := dev.ReadAt(area, off+binaryHeaderSize); err != nil {
		return h, refuse(NotLUKS2, fmt.Sprintf("header JSON cannot be read: %v", err))
	}
	if i := bytes.IndexByte(area, 0); i >= 0 {
		area = area[:i]
	}
	doc, err := strictJSON(area)
	if err != nil {
		return h, refuse(NotLUKS2, "header JSON: "+err.Error())
	}
	return checkMetadata(doc, kdfCapKiB)
}

func checkMetadata(doc map[string]any, kdfCapKiB int64) (Header, error) {
	var h Header
	if cfg, ok := doc["config"].(map[string]any); ok {
		if req, ok := cfg["requirements"].(map[string]any); ok {
			if m, ok := req["mandatory"].([]any); ok && len(m) > 0 {
				return h, refuse(Reencrypt, fmt.Sprintf("mandatory requirements %v", m))
			}
		}
	}
	segments, ok := doc["segments"].(map[string]any)
	if !ok || len(segments) == 0 {
		return h, refuse(NotLUKS2, "no data segment")
	}
	if len(segments) != 1 {
		return h, refuse(Reencrypt, fmt.Sprintf("%d data segments", len(segments)))
	}
	for _, s := range segments {
		seg, ok := s.(map[string]any)
		if !ok || seg["type"] != "crypt" {
			return h, refuse(Unsupported, fmt.Sprintf("data segment type %v", typeOf(s)))
		}
		if flags, ok := seg["flags"].([]any); ok && len(flags) > 0 {
			return h, refuse(Reencrypt, fmt.Sprintf("data segment flags %v", flags))
		}
		off, err := byteCount(seg["offset"])
		if err != nil || off == 0 || off%binaryHeaderSize != 0 || off > maxHeaderArea {
			return h, refuse(Unsupported, fmt.Sprintf("data segment offset %v", printable(fmt.Sprint(seg["offset"]))))
		}
		h.DataOffset = off
	}
	slots, ok := doc["keyslots"].(map[string]any)
	if !ok || len(slots) == 0 {
		return h, refuse(NotLUKS2, "no key slots")
	}
	for name, s := range slots {
		slot, ok := s.(map[string]any)
		if !ok {
			return h, refuse(NotLUKS2, "key slot "+name+" is not an object")
		}
		switch slot["type"] {
		case "luks2":
		case "reencrypt":
			return h, refuse(Reencrypt, "key slot "+name+" holds re-encryption state")
		default:
			return h, refuse(Unsupported, fmt.Sprintf("key slot %s type %v", name, typeOf(slot)))
		}
		area, ok := slot["area"].(map[string]any)
		if !ok {
			return h, refuse(NotLUKS2, "key slot "+name+" has no area")
		}
		aOff, err1 := byteCount(area["offset"])
		aSize, err2 := byteCount(area["size"])
		if err1 != nil || err2 != nil || aSize > h.DataOffset || aOff > h.DataOffset-aSize {
			return h, refuse(NotLUKS2, "key slot "+name+" area runs into the data")
		}
		kdf, ok := slot["kdf"].(map[string]any)
		if !ok {
			return h, refuse(NotLUKS2, "key slot "+name+" has no KDF")
		}
		kind, _ := kdf["type"].(string)
		if !kdfTypes[kind] {
			return h, refuse(Unsupported, fmt.Sprintf("key slot %s KDF %q", name, kind))
		}
		if kind == "pbkdf2" {
			continue
		}
		mem, ok := kdf["memory"].(int64)
		if !ok || mem <= 0 {
			return h, refuse(NotLUKS2, "key slot "+name+" has no Argon2 memory cost")
		}
		if mem > kdfCapKiB {
			return h, refuse(KDFMemory, fmt.Sprintf("key slot %s needs %d MiB, the cap here is %d MiB", name, mem>>10, kdfCapKiB>>10))
		}
	}
	if tokens, ok := doc["tokens"].(map[string]any); ok {
		for name, t := range tokens {
			tok, ok := t.(map[string]any)
			if !ok {
				return h, refuse(UnknownToken, "token "+name+" is not an object")
			}
			kind, _ := tok["type"].(string)
			if !tokenTypes[kind] {
				return h, refuse(UnknownToken, fmt.Sprintf("token %s type %q", name, printable(kind)))
			}
			if kind == "systemd-tpm2" && tok["tpm2-pin"] == true {
				h.PINToken = true
			}
		}
	} else if _, there := doc["tokens"]; there {
		return h, refuse(NotLUKS2, "tokens is not an object")
	}
	return h, nil
}

// byteCount reads a LUKS2 size or offset: a decimal number in a JSON string,
// as the format writes 64-bit values.
func byteCount(v any) (int64, error) {
	s, ok := v.(string)
	if !ok || s == "" || len(s) > 19 {
		return 0, errors.New("not a byte count")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a byte count")
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

func typeOf(v any) any {
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["type"].(string); ok {
			return strconv.Quote(printable(s))
		}
	}
	return "missing"
}

// printable keeps a header word safe to show: at most 40 runes of plain
// ASCII.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 40 {
			b.WriteString("…")
			break
		}
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}

// strictJSON decodes one JSON object. Integers become int64; anything a
// header never holds (fractions, exponents, huge numbers, duplicate keys,
// control characters, broken UTF-8, nesting deeper than 16) is refused.
func strictJSON(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := value(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("more than one value")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not an object")
	}
	return m, nil
}

const maxDepth = 16

func value(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("nested too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key := kt.(string)
				if err := plainString(key); err != nil {
					return nil, fmt.Errorf("key %q: %w", printable(key), err)
				}
				if _, dup := obj[key]; dup {
					return nil, fmt.Errorf("key %q appears twice", printable(key))
				}
				v, err := value(dec, depth+1)
				if err != nil {
					return nil, err
				}
				obj[key] = v
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			var arr []any
			for dec.More() {
				v, err := value(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token()
			return arr, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case json.Number:
		n, err := strconv.ParseInt(t.String(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("number %q is not a whole number", printable(t.String()))
		}
		return n, nil
	case string:
		if err := plainString(t); err != nil {
			return nil, err
		}
		return t, nil
	default:
		return t, nil
	}
}

func plainString(s string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return errors.New("holds a control character")
		}
	}
	return nil
}

package oldroot

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The headers are real fw13 install headers (diskcrypto testdata), changed
// only where a test says so.
const fixtureDir = "../../diskcrypto/testdata/"

var fixtures = []string{
	"luks-tpm2-passphrase.json",
	"luks-tpm2pin-recovery.json",
	"luks-tpm2-recovery.json",
	"luks-tpm2-recovery-passphrase.json",
}

const (
	testHdrSize    = 16 << 10
	testDataOffset = 16 << 20
	testCap        = 1 << 20
)

func loadFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(fixtureDir + name)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func marshal(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// putCopy writes one header copy at off: the binary part and the JSON.
func putCopy(area []byte, off int, magic []byte, version uint16, js []byte) {
	copy(area[off:], magic)
	binary.BigEndian.PutUint16(area[off+6:], version)
	binary.BigEndian.PutUint64(area[off+8:], testHdrSize)
	copy(area[off+binaryHeaderSize:off+testHdrSize], js)
}

// buildArea lays out a LUKS2 metadata area as cryptsetup writes it: the
// primary copy at 0, the secondary right after it, then the key slots.
func buildArea(primary, secondary []byte) []byte {
	area := make([]byte, testDataOffset)
	if primary != nil {
		putCopy(area, 0, primaryMagic, 2, primary)
	}
	if secondary != nil {
		putCopy(area, testHdrSize, secondaryMagic, 2, secondary)
	}
	return area
}

func check(area []byte) (Header, error) {
	return CheckHeader(bytes.NewReader(area), int64(len(area)), testCap)
}

func wantRefusal(t *testing.T, err error, reason Reason) {
	t.Helper()
	r, ok := AsRefusal(err)
	if !ok {
		t.Fatalf("got %v, want refusal %s", err, reason)
	}
	if r.Reason != reason {
		t.Fatalf("got refusal %s (%s), want %s", r.Reason, r.Detail, reason)
	}
	if r.Text == "" {
		t.Fatalf("refusal %s has no text", reason)
	}
}

func TestRealHeadersPass(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			js := marshal(t, loadFixture(t, name))
			h, err := check(buildArea(js, js))
			if err != nil {
				t.Fatal(err)
			}
			if h.DataOffset != testDataOffset {
				t.Fatalf("data offset %d", h.DataOffset)
			}
			if want := name == "luks-tpm2pin-recovery.json"; h.PINToken != want {
				t.Fatalf("PIN token %v, want %v", h.PINToken, want)
			}
		})
	}
}

// The fixtures are cryptsetup's pretty-printed output; the on-disk JSON is
// the same data, so the raw text must pass as well.
func TestRawFixtureText(t *testing.T) {
	raw, err := os.ReadFile(fixtureDir + fixtures[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := check(buildArea(raw, raw)); err != nil {
		t.Fatal(err)
	}
}

func TestChangedHeadersRefused(t *testing.T) {
	cases := []struct {
		name   string
		change func(doc map[string]any)
		reason Reason
	}{
		{"unknown token", func(doc map[string]any) {
			doc["tokens"].(map[string]any)["1"].(map[string]any)["type"] = "ssh"
		}, UnknownToken},
		{"token not an object", func(doc map[string]any) {
			doc["tokens"].(map[string]any)["2"] = "x"
		}, UnknownToken},
		{"argon2 over the cap", func(doc map[string]any) {
			kdf := slot(doc, "0")["kdf"].(map[string]any)
			kdf["type"], kdf["memory"], kdf["time"], kdf["cpus"] = "argon2id", json.Number("4194304"), json.Number("4"), json.Number("4")
		}, KDFMemory},
		{"argon2 without memory", func(doc map[string]any) {
			kdf := slot(doc, "0")["kdf"].(map[string]any)
			kdf["type"] = "argon2id"
		}, NotLUKS2},
		{"unknown KDF", func(doc map[string]any) {
			slot(doc, "0")["kdf"].(map[string]any)["type"] = "scrypt"
		}, Unsupported},
		{"re-encryption key slot", func(doc map[string]any) {
			slot(doc, "0")["type"] = "reencrypt"
		}, Reencrypt},
		{"other key slot type", func(doc map[string]any) {
			slot(doc, "0")["type"] = "luks3"
		}, Unsupported},
		{"mandatory requirement", func(doc map[string]any) {
			doc["config"].(map[string]any)["requirements"] = map[string]any{"mandatory": []any{"online-reencrypt-v2"}}
		}, Reencrypt},
		{"two segments", func(doc map[string]any) {
			segs := doc["segments"].(map[string]any)
			segs["1"] = segs["0"]
		}, Reencrypt},
		{"no segments", func(doc map[string]any) {
			doc["segments"] = map[string]any{}
		}, NotLUKS2},
		{"segment flags", func(doc map[string]any) {
			doc["segments"].(map[string]any)["0"].(map[string]any)["flags"] = []any{"backup-final"}
		}, Reencrypt},
		{"segment type", func(doc map[string]any) {
			doc["segments"].(map[string]any)["0"].(map[string]any)["type"] = "linear"
		}, Unsupported},
		{"data offset past the cap", func(doc map[string]any) {
			doc["segments"].(map[string]any)["0"].(map[string]any)["offset"] = "134217728"
		}, Unsupported},
		{"data offset not aligned", func(doc map[string]any) {
			doc["segments"].(map[string]any)["0"].(map[string]any)["offset"] = "16777217"
		}, Unsupported},
		{"data offset signed", func(doc map[string]any) {
			doc["segments"].(map[string]any)["0"].(map[string]any)["offset"] = "+16777216"
		}, Unsupported},
		{"key slot area in the data", func(doc map[string]any) {
			slot(doc, "0")["area"].(map[string]any)["offset"] = "16773120"
		}, NotLUKS2},
		{"key slot area huge", func(doc map[string]any) {
			slot(doc, "0")["area"].(map[string]any)["size"] = "9223372036854775807"
		}, NotLUKS2},
		{"no key slots", func(doc map[string]any) {
			doc["keyslots"] = map[string]any{}
		}, NotLUKS2},
		{"tokens not an object", func(doc map[string]any) {
			doc["tokens"] = []any{}
		}, NotLUKS2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := loadFixture(t, "luks-tpm2pin-recovery.json")
			c.change(doc)
			js := marshal(t, doc)
			_, err := check(buildArea(js, js))
			wantRefusal(t, err, c.reason)
		})
	}
}

func slot(doc map[string]any, n string) map[string]any {
	return doc["keyslots"].(map[string]any)[n].(map[string]any)
}

// cryptsetup falls back to the secondary copy, so a bad secondary copy is
// refused even when the primary is fine, and a lone secondary is checked.
func TestEveryCopyChecked(t *testing.T) {
	good := marshal(t, loadFixture(t, fixtures[1]))
	doc := loadFixture(t, fixtures[1])
	doc["tokens"].(map[string]any)["1"].(map[string]any)["type"] = "ssh"
	bad := marshal(t, doc)

	_, err := check(buildArea(good, bad))
	wantRefusal(t, err, UnknownToken)

	area := buildArea(nil, bad)
	_, err = check(area)
	wantRefusal(t, err, UnknownToken)

	// A copy placed where cryptsetup searches when the first is damaged.
	area = buildArea(good, good)
	putCopy(area, 1<<20, secondaryMagic, 2, bad)
	_, err = check(area)
	wantRefusal(t, err, UnknownToken)

	area = buildArea(nil, good)
	if _, err := check(area); err != nil {
		t.Fatalf("lone good secondary: %v", err)
	}

	doc = loadFixture(t, fixtures[1])
	doc["segments"].(map[string]any)["0"].(map[string]any)["offset"] = "8388608"
	_, err = check(buildArea(good, marshal(t, doc)))
	wantRefusal(t, err, NotLUKS2)
}

func TestBrokenJSONRefused(t *testing.T) {
	good := string(marshal(t, loadFixture(t, fixtures[1])))
	cases := map[string]string{
		// json-c keeps the last of two keys; a first-key reader would see
		// the allowed token.
		"duplicate key": strings.Replace(good, `"type":"systemd-recovery"`, `"type":"systemd-recovery","type":"ssh"`, 1),
		// json-c cuts a key at a NUL.
		"escaped NUL in a key":  strings.Replace(good, `"tokens"`, `"tokens\u0000x"`, 1),
		"control in a string":   strings.Replace(good, `"systemd-recovery"`, `"systemd-recovery\u001b"`, 1),
		"replacement character": strings.Replace(good, `"systemd-recovery"`, `"systemd-recovery�"`, 1),
		"fraction":              strings.Replace(good, `"iterations":1000`, `"iterations":1000.5`, 1),
		"exponent":              strings.Replace(good, `"iterations":1000`, `"iterations":1e3`, 1),
		"two values":            good + "{}",
		"not an object":         "[" + good + "]",
		"too deep":              strings.Replace(good, `"keyslots"`, `"x":`+strings.Repeat("[", 20)+strings.Repeat("]", 20)+`,"keyslots"`, 1),
		"invalid UTF-8":         strings.Replace(good, `"systemd-recovery"`, "\"systemd-recovery\xff\"", 1),
		"cut short":             good[:len(good)/2],
		"empty":                 "",
	}
	for name, js := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := check(buildArea([]byte(js), []byte(js)))
			wantRefusal(t, err, NotLUKS2)
		})
	}
}

func TestBinaryHeaderRefused(t *testing.T) {
	good := marshal(t, loadFixture(t, fixtures[1]))

	_, err := check(make([]byte, testDataOffset))
	wantRefusal(t, err, NotLUKS2)

	area := buildArea(good, good)
	binary.BigEndian.PutUint16(area[6:], 1)
	_, err = check(area)
	wantRefusal(t, err, NotLUKS2)

	for _, size := range []uint64{8 << 10, 24 << 10, 8 << 20} {
		area = buildArea(good, good)
		binary.BigEndian.PutUint64(area[8:], size)
		_, err = check(area)
		wantRefusal(t, err, NotLUKS2)
	}

	// The header runs past a small partition.
	area = buildArea(good, nil)[:testHdrSize-1]
	_, err = check(area)
	wantRefusal(t, err, NotLUKS2)
}

func TestCheckAreaCutsAtData(t *testing.T) {
	good := marshal(t, loadFixture(t, fixtures[1]))
	area := append(buildArea(good, good), make([]byte, 4<<20)...)
	out, h, err := CheckArea(area, testCap)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != testDataOffset || !h.PINToken {
		t.Fatalf("got %d bytes, PIN %v", len(out), h.PINToken)
	}

	// A partition smaller than the data offset.
	_, _, err = CheckArea(buildArea(good, good)[:1<<20], testCap)
	wantRefusal(t, err, Unsupported)
}

func TestKDFCap(t *testing.T) {
	for _, c := range []struct {
		meminfo string
		want    int64
		ok      bool
	}{
		{"MemTotal:       16000000 kB\nMemAvailable:    4000000 kB\n", 3000000, true},
		{"MemAvailable:   64000000 kB\n", 4 << 20, true},
		{"MemTotal:       16000000 kB\n", 0, false},
		{"MemAvailable:   0 kB\n", 0, false},
	} {
		got, err := KDFCap([]byte(c.meminfo))
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%q: got %d, %v", c.meminfo, got, err)
		}
	}
}

func TestCheckCryptsetup(t *testing.T) {
	for out, ok := range map[string]bool{
		"cryptsetup 2.8.6 flags: UDEV BLKID KEYRING KERNEL_CAPI \n": true,
		"cryptsetup 2.4.3\n":  true,
		"cryptsetup 3.0.0\n":  true,
		"cryptsetup 2.4.2\n":  false,
		"cryptsetup 2.3.10\n": false,
		"cryptsetup 1.7.5\n":  false,
		"something else\n":    false,
		"":                    false,
	} {
		err := CheckCryptsetup([]byte(out))
		if (err == nil) != ok {
			t.Errorf("%q: %v", out, err)
		}
		if err != nil {
			wantRefusal(t, err, OldCryptsetup)
		}
	}
}

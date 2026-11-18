package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/prepared"
)

// EXAMPLE key in the systemd alphabet.
var moveTestKey = strings.TrimSuffix(strings.Repeat("cbdefghi-", 8), "-")

// moveArea lays out a LUKS2 metadata area as cryptsetup writes it around
// one of the real fw13 install headers in the diskcrypto testdata.
func moveArea(t *testing.T, fixture string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../internal/installer/diskcrypto/testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	const hdr, data = 16 << 10, 16 << 20
	area := make([]byte, data)
	for off, magic := range map[int]string{0: "LUKS\xba\xbe", hdr: "SKUL\xba\xbe"} {
		copy(area[off:], magic)
		binary.BigEndian.PutUint16(area[off+6:], 2)
		binary.BigEndian.PutUint64(area[off+8:], hdr)
		copy(area[off+4096:off+hdr], js)
	}
	return area
}

type moveFake struct {
	areas   [][]byte // one per read; the last repeats
	reads   int
	typed   []string
	prompts []string
	replies []moveReply
	asked   []string
	written []prepared.Record
}

type moveReply struct {
	reply devgate.Reply
	err   error
}

func (f *moveFake) deps(dev string) movePrepareDeps {
	return movePrepareDeps{
		rootDevice: func(context.Context) (string, error) { return dev, nil },
		readArea: func(path string) ([]byte, error) {
			if path != dev {
				return nil, errors.New("wrong device " + path)
			}
			a := f.areas[min(f.reads, len(f.areas)-1)]
			f.reads++
			return a, nil
		},
		ask: func(_ context.Context, kind string, secret []byte) (devgate.Reply, error) {
			f.asked = append(f.asked, kind+":"+string(secret))
			if len(f.replies) == 0 {
				return devgate.Reply{}, errors.New("no reply left")
			}
			r := f.replies[0]
			f.replies = f.replies[1:]
			return r.reply, r.err
		},
		secret: func(prompt string) (string, error) {
			f.prompts = append(f.prompts, prompt)
			if len(f.typed) == 0 {
				return "", errors.New("no answer left")
			}
			s := f.typed[0]
			f.typed = f.typed[1:]
			return s, nil
		},
		write: func(r prepared.Record) error {
			f.written = append(f.written, r)
			return nil
		},
		now: func() time.Time { return time.Date(2026, 11, 18, 6, 0, 0, 0, time.FixedZone("CET", 3600)) },
	}
}

func runMoveFake(f *moveFake, dev string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := movePrepare(context.Background(), f.deps(dev), &out, &errOut)
	return code, out.String(), errOut.String()
}

var moveOK = moveReply{reply: devgate.Reply{OK: true, PINLeft: 3}}

func TestMovePrepareUnencrypted(t *testing.T) {
	f := &moveFake{}
	code, out, _ := runMoveFake(f, "")
	if code != 0 || !strings.Contains(out, "not encrypted") {
		t.Fatalf("code %d, out %q", code, out)
	}
	if len(f.prompts) != 0 || len(f.written) != 0 || f.reads != 0 {
		t.Fatalf("asked %v, wrote %v, read %d", f.prompts, f.written, f.reads)
	}
}

func TestMovePrepareRefusedHeader(t *testing.T) {
	f := &moveFake{areas: [][]byte{make([]byte, 16<<20)}}
	code, _, errOut := runMoveFake(f, "/dev/nvme0n1p2")
	if code != 1 || !strings.Contains(errOut, "the stick would refuse this disk") {
		t.Fatalf("code %d, err %q", code, errOut)
	}
	if len(f.prompts) != 0 || len(f.written) != 0 {
		t.Fatalf("asked %v, wrote %v", f.prompts, f.written)
	}
}

func TestMovePreparePIN(t *testing.T) {
	area := moveArea(t, "luks-tpm2pin-recovery.json")
	f := &moveFake{
		areas: [][]byte{area},
		typed: []string{strings.ToUpper(moveTestKey), "1111", "2468"},
		replies: []moveReply{
			moveOK,
			{reply: devgate.Reply{PINLeft: 2}, err: devgate.ErrWrong},
			moveOK,
		},
	}
	code, out, errOut := runMoveFake(f, "/dev/nvme0n1p2")
	if code != 0 {
		t.Fatalf("code %d, out %q, err %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "Wrong startup PIN; 2 tries left.") || !strings.Contains(out, "Ready.") {
		t.Fatalf("out %q, err %q", out, errOut)
	}
	want := []string{"recovery:" + moveTestKey, "pin:1111", "pin:2468"}
	if strings.Join(f.asked, " ") != strings.Join(want, " ") {
		t.Fatalf("asked %v", f.asked)
	}
	cut, _, err := oldroot.CheckArea(area, oldroot.MaxKDFMemoryKiB)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.written) != 1 {
		t.Fatalf("written %v", f.written)
	}
	r := f.written[0]
	if !r.PIN || r.Version != prepared.Version || !r.Fits(oldroot.AreaDigest(cut)) || r.Time.Location() != time.UTC || !r.Time.Equal(time.Date(2026, 11, 18, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("record %+v", r)
	}
	if f.reads != 2 {
		t.Fatalf("the header was read %d times, want 2 (before and after the checks)", f.reads)
	}
}

func TestMovePrepareNoPIN(t *testing.T) {
	f := &moveFake{
		areas:   [][]byte{moveArea(t, "luks-tpm2-recovery.json")},
		typed:   []string{moveTestKey},
		replies: []moveReply{moveOK},
	}
	code, out, errOut := runMoveFake(f, "/dev/nvme0n1p2")
	if code != 0 || len(f.written) != 1 || f.written[0].PIN {
		t.Fatalf("code %d, out %q, err %q, written %v", code, out, errOut, f.written)
	}
	if len(f.prompts) != 1 || strings.Contains(out, "startup PIN") {
		t.Fatalf("prompts %v, out %q", f.prompts, out)
	}
}

func TestMovePrepareWrongKeys(t *testing.T) {
	wrong := moveReply{reply: devgate.Reply{PINLeft: 3}, err: devgate.ErrWrong}
	f := &moveFake{
		areas:   [][]byte{moveArea(t, "luks-tpm2pin-recovery.json")},
		typed:   []string{moveTestKey, "not a key", moveTestKey},
		replies: []moveReply{wrong, wrong},
	}
	code, _, errOut := runMoveFake(f, "/dev/nvme0n1p2")
	if code != 1 || len(f.written) != 0 {
		t.Fatalf("code %d, written %v", code, f.written)
	}
	// A key in the wrong form costs no check.
	if len(f.asked) != 2 || strings.Count(errOut, "does not open this disk") != 2 || !strings.Contains(errOut, "No recovery key opened this disk") {
		t.Fatalf("asked %v, err %q", f.asked, errOut)
	}
	if len(f.prompts) != moveRecoveryTries {
		t.Fatalf("prompts %v", f.prompts)
	}
}

func TestMovePrepareStops(t *testing.T) {
	for name, typed := range map[string][]string{
		"at the key": {""},
		"at the PIN": {moveTestKey, ""},
	} {
		f := &moveFake{
			areas:   [][]byte{moveArea(t, "luks-tpm2pin-recovery.json")},
			typed:   typed,
			replies: []moveReply{moveOK},
		}
		code, out, _ := runMoveFake(f, "/dev/nvme0n1p2")
		if code != 1 || len(f.written) != 0 || !strings.Contains(out, "Nothing was prepared") {
			t.Errorf("%s: code %d, out %q, written %v", name, code, out, f.written)
		}
	}
}

func TestMovePreparePINLockedOut(t *testing.T) {
	retry := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	for name, last := range map[string]moveReply{
		// The last allowed try was wrong.
		"wrong": {reply: devgate.Reply{Retry: retry}, err: devgate.ErrWrong},
		// No try was left to begin with.
		"refused": {reply: devgate.Reply{Retry: retry, Error: "too many wrong PINs; use the recovery key or wait"}, err: errors.New("too many wrong PINs; use the recovery key or wait")},
	} {
		f := &moveFake{
			areas:   [][]byte{moveArea(t, "luks-tpm2pin-recovery.json")},
			typed:   []string{moveTestKey, "1111", "2468"},
			replies: []moveReply{moveOK, last},
		}
		code, _, errOut := runMoveFake(f, "/dev/nvme0n1p2")
		if code != 1 || len(f.written) != 0 || !strings.Contains(errOut, "No PIN tries are left until "+retry.Local().Format(time.DateTime)) {
			t.Errorf("%s: code %d, err %q, written %v", name, code, errOut, f.written)
		}
		if len(f.asked) != 2 {
			t.Errorf("%s: asked again after the lockout: %v", name, f.asked)
		}
	}
}

func TestMovePrepareCheckFails(t *testing.T) {
	f := &moveFake{
		areas:   [][]byte{moveArea(t, "luks-tpm2pin-recovery.json")},
		typed:   []string{moveTestKey, "2468"},
		replies: []moveReply{moveOK, {reply: devgate.Reply{Error: "the security chip is not available; use the recovery key"}, err: errors.New("the security chip is not available; use the recovery key")}},
	}
	code, _, errOut := runMoveFake(f, "/dev/nvme0n1p2")
	if code != 1 || len(f.written) != 0 || !strings.Contains(errOut, "security chip is not available") {
		t.Fatalf("code %d, err %q, written %v", code, errOut, f.written)
	}
}

// A key or PIN changed while move prepare waited for the answers: the
// record would describe a header that is gone.
func TestMovePrepareHeaderChanged(t *testing.T) {
	f := &moveFake{
		areas:   [][]byte{moveArea(t, "luks-tpm2pin-recovery.json"), moveArea(t, "luks-tpm2-recovery.json")},
		typed:   []string{moveTestKey, "2468"},
		replies: []moveReply{moveOK, moveOK},
	}
	code, _, errOut := runMoveFake(f, "/dev/nvme0n1p2")
	if code != 1 || len(f.written) != 0 || !strings.Contains(errOut, "changed while this ran") {
		t.Fatalf("code %d, err %q, written %v", code, errOut, f.written)
	}
}

func TestRunMoveUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"prepare", "now"}, {"go"}} {
		var errOut bytes.Buffer
		if code := runMove(args, &bytes.Buffer{}, &errOut); code != 2 || !strings.Contains(errOut.String(), moveUsage) {
			t.Errorf("%v: code %d, err %q", args, code, errOut.String())
		}
	}
}

package secureboot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"
)

// synthPE builds a SYNTHETIC PE32+ image with the given sections, in the
// order given, laid out one after another behind 0x200 bytes of headers.
// A non-nil cert is appended as the certificate table.
func synthPE(t *testing.T, sections map[string][]byte, order []string, cert []byte) []byte {
	t.Helper()
	const opt = 0x40 + 24
	const optSize = 240
	const headers = 0x200
	img := make([]byte, headers)
	copy(img, "MZ")
	binary.LittleEndian.PutUint32(img[0x3c:], 0x40)
	copy(img[0x40:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(img[0x44:], 0x8664)
	binary.LittleEndian.PutUint16(img[0x46:], uint16(len(order)))
	binary.LittleEndian.PutUint16(img[0x54:], optSize)
	binary.LittleEndian.PutUint16(img[0x56:], 0x22)
	binary.LittleEndian.PutUint16(img[opt:], 0x20b)
	binary.LittleEndian.PutUint32(img[opt+32:], 0x1000) // section alignment
	binary.LittleEndian.PutUint32(img[opt+36:], 0x200)  // file alignment
	binary.LittleEndian.PutUint32(img[opt+60:], headers)
	binary.LittleEndian.PutUint32(img[opt+64:], 0x5a5a5a5a) // checksum
	binary.LittleEndian.PutUint32(img[opt+108:], 16)
	table := opt + optSize
	for i, name := range order {
		data := sections[name]
		raw := (len(data) + 0x1ff) &^ 0x1ff
		at := table + 40*i
		copy(img[at:at+8], name)
		binary.LittleEndian.PutUint32(img[at+8:], uint32(len(data)))
		binary.LittleEndian.PutUint32(img[at+12:], uint32(0x1000*(i+1)))
		binary.LittleEndian.PutUint32(img[at+16:], uint32(raw))
		binary.LittleEndian.PutUint32(img[at+20:], uint32(len(img)))
		img = append(img, data...)
		img = append(img, make([]byte, raw-len(data))...)
	}
	if cert != nil {
		secdir := opt + 112 + 4*8
		binary.LittleEndian.PutUint32(img[secdir:], uint32(len(img)))
		binary.LittleEndian.PutUint32(img[secdir+4:], uint32(len(cert)))
		img = append(img, cert...)
	}
	return img
}

func synthUKI(t *testing.T, cmdline string) []byte {
	return synthPE(t, map[string][]byte{
		".text":    []byte("SYNTHETIC stub code"),
		".cmdline": []byte(cmdline),
	}, []string{".text", ".cmdline"}, nil)
}

func TestAuthenticodeSHA256(t *testing.T) {
	plain := synthUKI(t, "gjallar.verity=SYNTHETIC")
	got, err := AuthenticodeSHA256(plain)
	if err != nil {
		t.Fatal(err)
	}

	// The definition, spelled out for this layout: headers without the
	// checksum and the certificate table entry, then the sections.
	const opt = 0x40 + 24
	checksum, secdir := opt+64, opt+112+4*8
	h := sha256.New()
	h.Write(plain[:checksum])
	h.Write(plain[checksum+4 : secdir])
	h.Write(plain[secdir+8:])
	if want := h.Sum(nil); !bytes.Equal(got[:], want) {
		t.Fatalf("hash %x, want %x", got, want)
	}

	// A signature changes the checksum, the certificate table entry and
	// appends the table; none of that is hashed.
	signed := synthPE(t, map[string][]byte{
		".text":    []byte("SYNTHETIC stub code"),
		".cmdline": []byte("gjallar.verity=SYNTHETIC"),
	}, []string{".text", ".cmdline"}, []byte("SYNTHETIC certificate table"))
	binary.LittleEndian.PutUint32(signed[opt+64:], 0x12345678)
	if again, err := AuthenticodeSHA256(signed); err != nil || again != got {
		t.Fatalf("signed copy: %x %v, want %x", again, err, got)
	}

	// Any change to what shim checks changes the hash.
	other := synthUKI(t, "gjallar.verity=SYNTHETIX")
	if changed, err := AuthenticodeSHA256(other); err != nil || changed == got {
		t.Fatalf("changed section kept the hash: %v", err)
	}

	bad := map[string][]byte{
		"empty": nil,
		"no MZ": append([]byte("ZM"), plain[2:]...),
		"short": plain[:0x80],
		"no PE": func() []byte { b := bytes.Clone(plain); copy(b[0x40:], "XX"); return b }(),
		"magic": func() []byte { b := bytes.Clone(plain); b[opt] = 0; return b }(),
		"dirs":  func() []byte { b := bytes.Clone(plain); binary.LittleEndian.PutUint32(b[opt+108:], 4); return b }(),
		"headers": func() []byte {
			b := bytes.Clone(plain)
			binary.LittleEndian.PutUint32(b[opt+60:], uint32(len(b)+1))
			return b
		}(),
		"section past the end": func() []byte {
			b := bytes.Clone(plain)
			binary.LittleEndian.PutUint32(b[opt+240+16:], uint32(len(b)))
			return b
		}(),
		"overlapping sections": func() []byte {
			b := bytes.Clone(plain)
			binary.LittleEndian.PutUint32(b[opt+240+40+20:], 0x200)
			return b
		}(),
		"certificate table not last": func() []byte {
			b := bytes.Clone(signed)
			b = append(b, 0)
			return b
		}(),
	}
	for name, data := range bad {
		if _, err := AuthenticodeSHA256(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func utf16Var(s string) []byte {
	var raw []byte
	for _, u := range utf16.Encode([]rune(s)) {
		raw = binary.LittleEndian.AppendUint16(raw, u)
	}
	return append(raw, 0, 0)
}

const synthVerity = "gjallar.verity=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// stickFixture is a SYNTHETIC stick start: systemd-stub's variable, the
// stage-1 copy of the UKI and the running command line.
type stickFixture struct {
	fw   fakeFirmware
	copy stickCopy
	uki  []byte
	sum  string
}

func newStickFixture(t *testing.T) stickFixture {
	t.Helper()
	fw := newFakeFirmware(t)
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	if err := os.Mkdir(run, 0o755); err != nil {
		t.Fatal(err)
	}
	c := stickCopy{dir: run, name: "stick-uki.efi", owner: os.Getuid(), cmdline: filepath.Join(dir, "cmdline")}
	uki := synthUKI(t, "init=/init "+synthVerity+"\x00")
	if err := os.WriteFile(filepath.Join(run, c.name), uki, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.cmdline, []byte("BOOT_IMAGE=x "+synthVerity+" quiet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fw.set("StubImageIdentifier", stubLoaderGUID, utf16Var(`\EFI\BOOT\grubx64.efi`))
	h, err := AuthenticodeSHA256(uki)
	if err != nil {
		t.Fatal(err)
	}
	return stickFixture{fw, c, uki, hex.EncodeToString(h[:])}
}

func TestStickHash(t *testing.T) {
	f := newStickFixture(t)
	got, ok, err := f.fw.root.stickHash(f.copy)
	if err != nil || !ok || got != f.sum {
		t.Fatalf("stick start: %q %v %v, want %q", got, ok, err, f.sum)
	}

	// FAT ignores case; systemd-stub reports the path as shim passed it.
	f.fw.set("StubImageIdentifier", stubLoaderGUID, utf16Var(`\efi\boot\GRUBX64.EFI`))
	if _, ok, err := f.fw.root.stickHash(f.copy); !ok || err != nil {
		t.Fatalf("other case: %v %v", ok, err)
	}

	// An installed system starts another file, or no UKI at all.
	f.fw.set("StubImageIdentifier", stubLoaderGUID, utf16Var(`\EFI\Linux\nixos-generation-1.efi`))
	if _, ok, err := f.fw.root.stickHash(f.copy); ok || err != nil {
		t.Fatalf("installed start: %v %v", ok, err)
	}
	os.Remove(filepath.Join(f.fw.root.efivars, "StubImageIdentifier-"+stubLoaderGUID))
	if _, ok, err := f.fw.root.stickHash(f.copy); ok || err != nil {
		t.Fatalf("no stub: %v %v", ok, err)
	}
}

func TestStickHashMissingCopy(t *testing.T) {
	f := newStickFixture(t)
	os.Remove(filepath.Join(f.copy.dir, f.copy.name))
	if _, ok, err := f.fw.root.stickHash(f.copy); ok || !errors.Is(err, errNoStickCopy) {
		t.Fatalf("missing copy: %v %v", ok, err)
	}
}

// TestStickCopyRefusesPlanted plants what a user session could put at the
// copy's path; each one is refused before a byte of it is hashed.
func TestStickCopyRefusesPlanted(t *testing.T) {
	cases := map[string]func(t *testing.T, f stickFixture){
		"link to the file": func(t *testing.T, f stickFixture) {
			path := filepath.Join(f.copy.dir, f.copy.name)
			real := filepath.Join(t.TempDir(), "real.efi")
			os.WriteFile(real, f.uki, 0o444)
			os.Remove(path)
			if err := os.Symlink(real, path); err != nil {
				t.Fatal(err)
			}
		},
		"link to the folder": func(t *testing.T, f stickFixture) {
			real := t.TempDir()
			os.WriteFile(filepath.Join(real, f.copy.name), f.uki, 0o444)
			os.RemoveAll(f.copy.dir)
			if err := os.Symlink(real, f.copy.dir); err != nil {
				t.Fatal(err)
			}
		},
		"FIFO": func(t *testing.T, f stickFixture) {
			path := filepath.Join(f.copy.dir, f.copy.name)
			os.Remove(path)
			if err := syscall.Mkfifo(path, 0o444); err != nil {
				t.Fatal(err)
			}
		},
		"folder": func(t *testing.T, f stickFixture) {
			path := filepath.Join(f.copy.dir, f.copy.name)
			os.Remove(path)
			os.Mkdir(path, 0o755)
		},
		"world-writable file": func(t *testing.T, f stickFixture) {
			os.Chmod(filepath.Join(f.copy.dir, f.copy.name), 0o666)
		},
		"group-writable folder": func(t *testing.T, f stickFixture) {
			os.Chmod(f.copy.dir, 0o775)
		},
		"oversized": func(t *testing.T, f stickFixture) {
			path := filepath.Join(f.copy.dir, f.copy.name)
			os.Chmod(path, 0o644)
			if err := os.Truncate(path, stickUKIMax+1); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o444); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStickFixture(t)
			plant(t, f)
			if _, ok, err := f.fw.root.stickHash(f.copy); ok || err == nil {
				t.Fatalf("accepted: ok=%v err=%v", ok, err)
			}
		})
	}
	t.Run("other owner", func(t *testing.T) {
		f := newStickFixture(t)
		f.copy.owner = os.Getuid() + 1
		if _, ok, err := f.fw.root.stickHash(f.copy); ok || err == nil || !strings.Contains(err.Error(), "belongs to uid") {
			t.Fatalf("accepted: ok=%v err=%v", ok, err)
		}
	})
}

func TestStickCopySameVerity(t *testing.T) {
	cases := map[string]struct{ uki, running string }{
		"other image":         {synthVerity + "0", synthVerity},
		"no verity in UKI":    {"init=/init", synthVerity},
		"two in UKI":          {synthVerity + " " + synthVerity, synthVerity},
		"empty value":         {"gjallar.verity=", "gjallar.verity="},
		"none on the running": {synthVerity, "quiet"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStickFixture(t)
			path := filepath.Join(f.copy.dir, f.copy.name)
			os.Chmod(path, 0o644)
			if err := os.WriteFile(path, synthUKI(t, c.uki), 0o444); err != nil {
				t.Fatal(err)
			}
			os.Chmod(path, 0o444)
			if err := os.WriteFile(f.copy.cmdline, []byte(c.running), 0o644); err != nil {
				t.Fatal(err)
			}
			_, ok, err := f.fw.root.stickHash(f.copy)
			if ok || err == nil || !strings.Contains(err.Error(), "verity") && !strings.Contains(err.Error(), "another stick image") {
				t.Fatalf("accepted or wrong reason: ok=%v err=%v", ok, err)
			}
		})
	}
	t.Run("no command line section", func(t *testing.T) {
		f := newStickFixture(t)
		path := filepath.Join(f.copy.dir, f.copy.name)
		os.Chmod(path, 0o644)
		if err := os.WriteFile(path, synthPE(t, map[string][]byte{".text": []byte("x")}, []string{".text"}, nil), 0o444); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.fw.root.stickHash(f.copy); err == nil || !strings.Contains(err.Error(), "no command line") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestUTF16String(t *testing.T) {
	if got := utf16String(utf16Var(stickBootPath)); got != stickBootPath {
		t.Fatalf("%q", got)
	}
	// Odd trailing byte and no terminator.
	raw := utf16Var("ab")
	if got := utf16String(append(raw[:4], 'x')); got != "ab" {
		t.Fatalf("%q", got)
	}
}

// fakeMokutil stands in for "sudo mokutil --delete-hash": it records the
// calls and, when queue is set, adds each hash to MokDel as one list per
// call, as mokutil appends to a request that waits.
func fakeMokutil(t *testing.T, f stickFixture, queue bool) *[]string {
	t.Helper()
	var calls []string
	var raw []byte
	saved := run
	t.Cleanup(func() { run = saved })
	run = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if queue {
			if raw == nil {
				waiting, _, err := f.fw.root.shimRequest("MokDel")
				if err != nil {
					t.Fatal(err)
				}
				raw = append([]byte{}, waiting...)
			}
			raw = append(raw, digestList(t, sha256SignatureType, args[len(args)-1])...)
			f.fw.set("MokDel", shimLockGUID, raw)
		}
		return nil
	}
	return &calls
}

func TestQueueHashDeletes(t *testing.T) {
	// SYNTHETIC hashes of older sticks.
	old1 := strings.Repeat("ab", 32)
	old2 := strings.Repeat("cd", 32)
	listed := func(t *testing.T, f stickFixture, sums ...string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.fw.root.mokvars, "MokListRT"), digestList(t, sha256SignatureType, sums...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	t.Run("the stick and older sticks, one call each", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, old1, f.sum, old2)
		calls := fakeMokutil(t, f, true)
		ui := &scriptedUI{answers: []bool{true}}
		var out bytes.Buffer
		del, err := queueHashDeletes(ctx, ui, f.fw.root, f.copy, &out, "PASSWORD TEXT")
		if err != nil || del == nil || del.stick != f.sum || del.reused || !slices.Equal(del.sums, []string{f.sum, old1, old2}) {
			t.Fatalf("%+v %v", del, err)
		}
		if len(*calls) != 3 {
			t.Fatalf("calls %q", *calls)
		}
		for i, sum := range del.sums {
			if (*calls)[i] != "sudo "+privilegedTool("mokutil")+" --delete-hash "+sum {
				t.Fatalf("call %d %q", i, (*calls)[i])
			}
		}
		text := out.String()
		if strings.Count(text, "PASSWORD TEXT") != 1 || strings.Count(text, againPassword) != 2 || strings.Index(text, "PASSWORD TEXT") > strings.Index(text, againPassword) {
			t.Fatalf("out %q", text)
		}
		if !strings.Contains(text, spacedHex(old1[:16])) || !strings.Contains(text, spacedHex(old2[:16])) || strings.Contains(text, spacedHex(f.sum[:16])) {
			t.Fatalf("listed %q", text)
		}
		if len(ui.asked) != 1 {
			t.Fatalf("asked %q", ui.asked)
		}
		notice := hashDeleteNotice(del)
		for _, sum := range del.sums {
			if !strings.Contains(notice, spacedHex(sum[:16])) {
				t.Fatalf("notice %q", notice)
			}
		}
	})

	t.Run("older sticks kept", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, old1, f.sum)
		calls := fakeMokutil(t, f, true)
		del, err := queueHashDeletes(ctx, &scriptedUI{answers: []bool{false}}, f.fw.root, f.copy, &bytes.Buffer{}, "")
		if err != nil || del == nil || !slices.Equal(del.sums, []string{f.sum}) || len(*calls) != 1 {
			t.Fatalf("%+v %v %q", del, err, *calls)
		}
	})

	t.Run("kept, but already queued", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, old1, f.sum)
		f.fw.set("MokDel", shimLockGUID, digestList(t, sha256SignatureType, old1))
		calls := fakeMokutil(t, f, true)
		if del, err := queueHashDeletes(ctx, &scriptedUI{answers: []bool{false}}, f.fw.root, f.copy, &bytes.Buffer{}, ""); err == nil || len(*calls) != 0 {
			t.Fatalf("%+v %v %q", del, err, *calls)
		}
	})

	t.Run("only older sticks", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, old1)
		calls := fakeMokutil(t, f, true)
		del, err := queueHashDeletes(ctx, &scriptedUI{answers: []bool{true}}, f.fw.root, f.copy, &bytes.Buffer{}, "")
		if err != nil || del == nil || del.stick != "" || !slices.Equal(del.sums, []string{old1}) || len(*calls) != 1 {
			t.Fatalf("%+v %v %q", del, err, *calls)
		}
	})

	t.Run("mokutil skipped a hash", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, f.sum)
		fakeMokutil(t, f, false)
		if del, err := queueHashDeletes(ctx, &scriptedUI{}, f.fw.root, f.copy, &bytes.Buffer{}, ""); err == nil {
			t.Fatalf("no request written, but %+v", del)
		}
	})

	t.Run("part already queued", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, f.sum, old1)
		f.fw.set("MokDel", shimLockGUID, digestList(t, sha256SignatureType, f.sum))
		calls := fakeMokutil(t, f, true)
		del, err := queueHashDeletes(ctx, &scriptedUI{answers: []bool{true}}, f.fw.root, f.copy, &bytes.Buffer{}, "")
		if err != nil || del == nil || del.reused || len(*calls) != 1 || !strings.HasSuffix((*calls)[0], old1) {
			t.Fatalf("%+v %v %q", del, err, *calls)
		}
	})

	t.Run("all already queued", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, f.sum)
		f.fw.set("MokDel", shimLockGUID, digestList(t, sha256SignatureType, f.sum))
		calls := fakeMokutil(t, f, true)
		del, err := queueHashDeletes(ctx, &scriptedUI{}, f.fw.root, f.copy, &bytes.Buffer{}, "")
		if err != nil || del == nil || !del.reused || len(*calls) != 0 {
			t.Fatalf("%+v %v %q", del, err, *calls)
		}
	})

	t.Run("shim lists no file hash", func(t *testing.T) {
		f := newStickFixture(t)
		calls := fakeMokutil(t, f, true)
		if del, err := queueHashDeletes(ctx, &scriptedUI{}, f.fw.root, f.copy, &bytes.Buffer{}, ""); del != nil || err != nil || len(*calls) != 0 {
			t.Fatalf("%+v %v %q", del, err, *calls)
		}
	})

	t.Run("not started from the stick", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, old1)
		os.Remove(filepath.Join(f.fw.root.efivars, "StubImageIdentifier-"+stubLoaderGUID))
		calls := fakeMokutil(t, f, true)
		ui := &scriptedUI{}
		if del, err := queueHashDeletes(ctx, ui, f.fw.root, f.copy, &bytes.Buffer{}, ""); del != nil || err != nil || len(*calls) != 0 || len(ui.asked) != 0 {
			t.Fatalf("%+v %v %q", del, err, *calls)
		}
	})

	t.Run("planted copy stops before root", func(t *testing.T) {
		f := newStickFixture(t)
		listed(t, f, f.sum, old1)
		os.Chmod(filepath.Join(f.copy.dir, f.copy.name), 0o666)
		calls := fakeMokutil(t, f, true)
		if _, err := queueHashDeletes(ctx, &scriptedUI{answers: []bool{true}}, f.fw.root, f.copy, &bytes.Buffer{}, ""); err == nil || len(*calls) != 0 {
			t.Fatalf("%v %q", err, *calls)
		}
	})
}

func TestShimRequestRootOnlyDelete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0000 file")
	}
	f := newStickFixture(t)
	path := filepath.Join(f.fw.root.efivars, "MokDel-"+shimLockGUID)
	raw := append([]byte{0x07, 0, 0, 0}, digestList(t, sha256SignatureType, f.sum)...)
	if err := os.WriteFile(path, raw, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mokHash(f.fw.root, "MokDel", f.sum); !errors.Is(err, fs.ErrPermission) && err == nil {
		t.Fatalf("no root reader: err = %v", err)
	}
	f.fw.root.rootRead = func(p string) ([]byte, error) {
		if p != path {
			t.Fatalf("root read %q", p)
		}
		return raw, nil
	}
	if got, err := mokHash(f.fw.root, "MokDel", f.sum); !got || err != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestMokVisitSteps(t *testing.T) {
	ours, _ := synthCert(t, "SYNTHETIC GjallarOS db", nil, nil)
	sum := strings.Repeat("0f", 32)
	if got := (mokVisit{}).steps(); got != "" {
		t.Fatalf("nothing queued: %q", got)
	}
	old, _ := synthCert(t, "SYNTHETIC Database Key", nil, nil)
	del := func(t *testing.T, raw ...[]byte) []SignatureList {
		t.Helper()
		lists, err := signatureLists(slices.Concat(raw...))
		if err != nil {
			t.Fatal(err)
		}
		return lists
	}
	both := mokVisit{key: ours.Raw, del: del(t, x509SignatureList(old.Raw), digestList(t, sha256SignatureType, sum))}.steps()
	for _, want := range []string{"1. Enroll MOK", sha1Fingerprint(ours.Raw, stepIndent), "6. Delete MOK", "7. View key 0", sha1Fingerprint(old.Raw, stepIndent),
		"SYNTHETIC Database Key", "8. Press down to View key 1", "0f 0f 0f 0f 0f 0f 0f 0f", "installer stick", "11. Type the password you just chose", "12. Reboot"} {
		if !strings.Contains(both, want) {
			t.Errorf("both: missing %q in\n%s", want, both)
		}
	}
	if strings.Index(both, "Enroll MOK") > strings.Index(both, "Delete MOK") ||
		strings.Index(both, sha1Fingerprint(old.Raw, stepIndent)) > strings.Index(both, "0f 0f 0f") {
		t.Error("steps out of MokManager's order")
	}
	onlyStick := mokVisit{del: del(t, digestList(t, sha256SignatureType, sum)), delReused: true}.steps()
	if strings.Contains(onlyStick, "Enroll") || !strings.Contains(onlyStick, "1. Delete MOK") || !strings.Contains(onlyStick, "6. Reboot") ||
		!strings.Contains(onlyStick, "chosen when the request was made") {
		t.Errorf("stick only:\n%s", onlyStick)
	}
	many := mokVisit{del: del(t, digestList(t, sha256SignatureType, sum, strings.Repeat("ab", 32)))}.steps()
	// One list of several hashes is one View key that opens [Hash List]
	// (seen in the L99 VM run: View hash 0..2, then Back).
	for _, want := range []string{"2. View key 0; it opens [Hash List] with 2 hashes", "3. View hash 0;", "0f 0f 0f 0f 0f 0f 0f 0f",
		"4. Press down to View hash 1;", "ab ab ab ab ab ab ab ab", "5. Press down to Back, then Enter", "6. Continue"} {
		if !strings.Contains(many, want) {
			t.Errorf("many: missing %q in\n%s", want, many)
		}
	}
	if strings.Contains(many, "View key 1") {
		t.Errorf("many: one hash list must be one View key:\n%s", many)
	}
	mixed := mokVisit{del: del(t, x509SignatureList(old.Raw), digestList(t, sha256SignatureType, sum, strings.Repeat("ab", 32), strings.Repeat("cd", 32)),
		digestList(t, sha256SignatureType, strings.Repeat("ef", 32)))}.steps()
	for _, want := range []string{"2. View key 0; under [Fingerprint]", "3. Press down to View key 1; it opens [Hash List] with 3 hashes",
		"4. View hash 0;", "6. Press down to View hash 2;", "cd cd cd cd", "7. Press down to Back, then Enter",
		"8. Press down to View key 2; the SHA256 hash must start with", "ef ef ef ef", "9. Continue"} {
		if !strings.Contains(mixed, want) {
			t.Errorf("mixed: missing %q in\n%s", want, mixed)
		}
	}
	onlyKey := mokVisit{key: ours.Raw}.steps()
	if strings.Contains(onlyKey, "Delete") || !strings.Contains(onlyKey, "6. Reboot") {
		t.Errorf("key only:\n%s", onlyKey)
	}
}

package winicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// All icons and programs here are SYNTHETIC, built in the test.

func le16(b []byte, v int) []byte { return binary.LittleEndian.AppendUint16(b, uint16(v)) }
func le32(b []byte, v int) []byte { return binary.LittleEndian.AppendUint32(b, uint32(v)) }

// pngIcon is a square PNG of side s in one colour.
func pngIcon(t *testing.T, s int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, s, s))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dib32 is a 32-bit icon bitmap of side s: the top half red, the bottom
// half blue, all opaque, with an empty mask.
func dib32(s int) []byte {
	d := le32(nil, 40)
	d = le32(d, s)
	d = le32(d, 2*s)
	d = le16(d, 1)
	d = le16(d, 32)
	d = append(d, make([]byte, 24)...)
	for row := range s { // bottom up
		for range s {
			if row < s/2 {
				d = append(d, 255, 0, 0, 255) // blue, as BGRA
			} else {
				d = append(d, 0, 0, 255, 255) // red
			}
		}
	}
	return append(d, make([]byte, (s+31)/32*4*s)...)
}

// dib1 is a 1-bit icon bitmap of side 32 with black and white, whose mask
// makes the left half clear.
func dib1() []byte {
	d := le32(nil, 40)
	d = le32(d, 32)
	d = le32(d, 64)
	d = le16(d, 1)
	d = le16(d, 1)
	d = append(d, make([]byte, 24)...)
	d = append(d, 0, 0, 0, 0, 255, 255, 255, 0)
	for range 32 {
		d = append(d, 0x0f, 0x0f, 0x0f, 0x0f)
	}
	for range 32 {
		d = append(d, 0xff, 0xff, 0, 0)
	}
	return d
}

// ico builds an .ico file from images, each with its listed side.
func ico(sides []int, bits []int, images [][]byte) []byte {
	d := le16(le16(le16(nil, 0), 1), len(images))
	off := 6 + 16*len(images)
	for i, img := range images {
		d = append(d, byte(sides[i]%256), byte(sides[i]%256), 0, 0)
		d = le16(d, 1)
		d = le16(d, bits[i])
		d = le32(d, len(img))
		d = le32(d, off)
		off += len(img)
	}
	for _, img := range images {
		d = append(d, img...)
	}
	return d
}

func TestICO(t *testing.T) {
	data := ico([]int{16, 32, 256}, []int{32, 1, 32}, [][]byte{
		dib32(16), dib1(), pngIcon(t, 256, color.NRGBA{0, 200, 0, 255}),
	})
	entries, err := ParseICO(data)
	if err != nil || len(entries) != 3 {
		t.Fatalf("ParseICO: %d entries, %v", len(entries), err)
	}
	imgs, err := Pick(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Sizes {
		img, ok := imgs[s]
		if !ok || img.Rect.Dx() != s || img.Rect.Dy() != s {
			t.Fatalf("size %d: %v", s, ok)
		}
	}
	if got := imgs[16].NRGBAAt(0, 0); got != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("16 top left %v, want red", got)
	}
	if got := imgs[16].NRGBAAt(0, 15); got != (color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("16 bottom left %v, want blue", got)
	}
	if got := imgs[32].NRGBAAt(0, 0); got.A != 0 {
		t.Errorf("32 left %v, want clear", got)
	}
	if got := imgs[32].NRGBAAt(31, 0); got != (color.NRGBA{255, 255, 255, 255}) {
		t.Errorf("32 right %v, want white", got)
	}
	// 48 is scaled from 256.
	if got := imgs[48].NRGBAAt(20, 20); got != (color.NRGBA{0, 200, 0, 255}) {
		t.Errorf("48 %v, want green", got)
	}
}

func TestICOOnlySmall(t *testing.T) {
	entries, err := ParseICO(ico([]int{16}, []int{32}, [][]byte{dib32(16)}))
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := Pick(entries)
	if err != nil || len(imgs) != 1 || imgs[16] == nil {
		t.Fatalf("Pick: %d images, %v", len(imgs), err)
	}
}

func TestICOBroken(t *testing.T) {
	good := ico([]int{16}, []int{32}, [][]byte{dib32(16)})
	tooMany := append([]byte(nil), good...)
	binary.LittleEndian.PutUint16(tooMany[4:], MaxEntries+1)
	outside := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(outside[6+12:], uint32(len(good)))
	wide := pngIcon(t, 64, color.NRGBA{A: 255})
	wide = append([]byte(nil), wide...)
	binary.BigEndian.PutUint32(wide[16:], 32) // IHDR width 32, height 64
	for name, data := range map[string][]byte{
		"empty":      nil,
		"not an ico": []byte("SYNTHETIC not an icon"),
		"too many":   tooMany,
		"outside":    outside,
		"cut":        good[:len(good)/2],
	} {
		entries, err := ParseICO(data)
		if err == nil {
			_, err = Pick(entries)
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Decode(Entry{Data: wide}); err == nil {
		t.Error("PNG that is not square decoded")
	}
	// Every cut of a good file fails or decodes, and never reads past it.
	for n := range good {
		if entries, err := ParseICO(good[:n]); err == nil {
			Pick(entries)
		}
	}
}

// program builds a 32-bit program file whose resources hold one icon
// group with one icon, the PNG icon.
func program(icon []byte) []byte {
	const (
		sectionOff = 0x200
		va         = 0x1000
	)
	// The resource tree: type, then id, then language, each a directory
	// with one numbered entry, laid out one after the other.
	dir := func(b []byte, id, to int) []byte {
		b = append(b, make([]byte, 14)...)
		b = le16(b, 1)
		b = le32(b, id)
		return le32(b, to)
	}
	group := le16(le16(le16(nil, 0), 1), 1)
	group = append(group, 64, 64, 0, 0)
	group = le16(group, 1)
	group = le16(group, 32)
	group = le32(group, len(icon))
	group = le16(group, 7)

	var r []byte
	// Root at 0 with two types: 3 (icon) and 14 (group).
	r = append(r, make([]byte, 14)...)
	r = le16(r, 2)
	r = le32(r, rtIcon)
	r = le32(r, 0x80000000|32)
	r = le32(r, rtGroupIcon)
	r = le32(r, 0x80000000|80)
	// 32: icon ids → 56: languages → leaf at 128.
	r = dir(r, 7, 0x80000000|56)
	r = dir(r, 0x409, 128)
	// 80: group ids → 104: languages → leaf at 144.
	r = dir(r, 1, 0x80000000|104)
	r = dir(r, 0x409, 144)
	r = append(r, make([]byte, 128-len(r))...)
	iconAt := 160
	groupAt := iconAt + len(icon)
	r = le32(le32(le32(le32(r, va+iconAt), len(icon)), 0), 0)
	r = le32(le32(le32(le32(r, va+groupAt), len(group)), 0), 0)
	r = append(r, icon...)
	r = append(r, group...)

	f := make([]byte, 64)
	f[0], f[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(f[0x3c:], 64)
	f = append(f, "PE\x00\x00"...)
	f = le16(f, 0x14c)
	f = le16(f, 1)
	f = append(f, make([]byte, 12)...)
	f = le16(f, 224)
	f = le16(f, 0x102)
	opt := le16(nil, 0x10b)
	opt = append(opt, make([]byte, 90)...)
	opt = le32(opt, 16)
	dirs := make([]byte, 128)
	binary.LittleEndian.PutUint32(dirs[16:], va)
	binary.LittleEndian.PutUint32(dirs[20:], uint32(len(r)))
	f = append(f, opt...)
	f = append(f, dirs...)
	f = append(f, ".rsrc\x00\x00\x00"...)
	f = le32(f, len(r))
	f = le32(f, va)
	f = le32(f, len(r))
	f = le32(f, sectionOff)
	f = append(f, make([]byte, 16)...)
	f = append(f, make([]byte, sectionOff-len(f))...)
	return append(f, r...)
}

func TestProgramIcon(t *testing.T) {
	icon := pngIcon(t, 64, color.NRGBA{10, 20, 30, 255})
	data := program(icon)
	entries, err := ProgramIcon(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(entries) != 1 || !bytes.Equal(entries[0].Data, icon) {
		t.Fatalf("ProgramIcon: %d entries, %v", len(entries), err)
	}
	imgs, err := Pick(entries)
	if err != nil || imgs[64] == nil || imgs[16] == nil || imgs[128] != nil {
		t.Fatalf("Pick: %v", err)
	}
	// Every cut fails or finds the icon, and never reads past the end.
	for n := range data {
		ProgramIcon(bytes.NewReader(data[:n]), int64(n))
	}
	// A program without resources has no icon.
	plain := append([]byte(nil), data...)
	binary.LittleEndian.PutUint32(plain[64+24+96+16:], 0)
	if _, err := ProgramIcon(bytes.NewReader(plain), int64(len(plain))); err != ErrNoIcon {
		t.Errorf("program without resources: %v", err)
	}
	// An icon size past the section is refused.
	big := append([]byte(nil), data...)
	leaf := 0x200 + 128
	binary.LittleEndian.PutUint32(big[leaf+4:], uint32(len(data)))
	if _, err := ProgramIcon(bytes.NewReader(big), int64(len(big))); err == nil {
		t.Error("icon past its section read")
	}
}

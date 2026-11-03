// Package winicon reads the icon of a Wine app (Plane 290): the icons in an
// .ico file, or the first icon group in a program's resources. Both come
// from the app, so every count, offset and size is checked against fixed
// limits before it is used, and nothing is read past them.
package winicon

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
)

// Limits on what the app's files may ask for.
const (
	// MaxICOBytes is the largest .ico file read.
	MaxICOBytes = 8 << 20
	// MaxEntries is the most icons one file or group may list.
	MaxEntries = 64
	// MaxEntryBytes is the largest single icon image.
	MaxEntryBytes = 4 << 20
	// MaxSide is the widest icon decoded; larger ones are skipped.
	MaxSide = 1024
)

// Sizes are the icon sizes written to the icon theme, in pixels.
var Sizes = []int{16, 22, 24, 32, 48, 64, 96, 128, 256}

// ErrNoIcon says the file has no icon this package can read.
var ErrNoIcon = errors.New("no icon")

// Entry is one icon image as stored: a PNG file or a device-independent
// bitmap with its mask.
type Entry struct {
	// Width and Height as listed; 0 stands for 256.
	Width, Height int
	BitCount      int
	Data          []byte
}

// ParseICO lists the icons in an .ico file.
func ParseICO(data []byte) ([]Entry, error) {
	if len(data) > MaxICOBytes {
		return nil, fmt.Errorf("icon file is larger than %d bytes", MaxICOBytes)
	}
	if len(data) < 6 || binary.LittleEndian.Uint16(data[0:]) != 0 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		return nil, ErrNoIcon
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	if count == 0 {
		return nil, ErrNoIcon
	}
	if count > MaxEntries {
		return nil, fmt.Errorf("icon file lists %d icons, more than %d", count, MaxEntries)
	}
	if len(data) < 6+16*count {
		return nil, errors.New("icon file is cut short")
	}
	entries := make([]Entry, 0, count)
	for i := range count {
		e := data[6+16*i:]
		size := binary.LittleEndian.Uint32(e[8:])
		offset := binary.LittleEndian.Uint32(e[12:])
		if size == 0 || size > MaxEntryBytes || uint64(offset)+uint64(size) > uint64(len(data)) {
			continue
		}
		entries = append(entries, Entry{
			Width:    side(e[0]),
			Height:   side(e[1]),
			BitCount: int(binary.LittleEndian.Uint16(e[6:])),
			Data:     data[offset : offset+size],
		})
	}
	if len(entries) == 0 {
		return nil, ErrNoIcon
	}
	return entries, nil
}

func side(b byte) int {
	if b == 0 {
		return 256
	}
	return int(b)
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// Decode gives an icon's pixels. It refuses icons that are not square or
// are wider than MaxSide, and checks a PNG's size before decoding it.
func Decode(e Entry) (*image.NRGBA, error) {
	if bytes.HasPrefix(e.Data, pngMagic) {
		cfg, err := png.DecodeConfig(bytes.NewReader(e.Data))
		if err != nil {
			return nil, err
		}
		if cfg.Width != cfg.Height || cfg.Width < 1 || cfg.Width > MaxSide {
			return nil, fmt.Errorf("icon is %dx%d", cfg.Width, cfg.Height)
		}
		img, err := png.Decode(bytes.NewReader(e.Data))
		if err != nil {
			return nil, err
		}
		return toNRGBA(img), nil
	}
	return decodeDIB(e.Data)
}

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			out.Set(x, y, color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)))
		}
	}
	return out
}

// decodeDIB decodes an icon bitmap: a BITMAPINFOHEADER, the palette for
// 8 bits or fewer, the colour rows and the 1-bit mask rows, both bottom up
// and padded to 4 bytes. The header's height counts both.
func decodeDIB(d []byte) (*image.NRGBA, error) {
	if len(d) < 40 {
		return nil, ErrNoIcon
	}
	hdr := binary.LittleEndian.Uint32(d[0:])
	w := int(int32(binary.LittleEndian.Uint32(d[4:])))
	h2 := int(int32(binary.LittleEndian.Uint32(d[8:])))
	bpp := int(binary.LittleEndian.Uint16(d[14:]))
	compression := binary.LittleEndian.Uint32(d[16:])
	used := binary.LittleEndian.Uint32(d[32:])
	if hdr < 40 || hdr > 256 || uint64(hdr) > uint64(len(d)) {
		return nil, ErrNoIcon
	}
	if w < 1 || w > 256 || h2 != 2*w {
		return nil, fmt.Errorf("icon bitmap is %dx%d", w, h2/2)
	}
	h := w
	switch {
	case compression == 0 && (bpp == 1 || bpp == 4 || bpp == 8 || bpp == 24 || bpp == 32):
	case compression == 3 && bpp == 32:
		// Bit fields with the usual masks; others are not used in icons.
	default:
		return nil, fmt.Errorf("icon bitmap of %d bits, compression %d", bpp, compression)
	}
	pos := int(hdr)
	if compression == 3 && hdr == 40 {
		pos += 12
	}
	var palette []color.NRGBA
	if bpp <= 8 {
		n := 1 << bpp
		if used != 0 && used < uint32(n) {
			n = int(used)
		}
		if pos+4*n > len(d) {
			return nil, errors.New("icon bitmap is cut short")
		}
		palette = make([]color.NRGBA, n)
		for i := range palette {
			p := d[pos+4*i:]
			palette[i] = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
		}
		pos += 4 * n
	}
	stride := (w*bpp + 31) / 32 * 4
	maskStride := (w + 31) / 32 * 4
	if pos+stride*h > len(d) {
		return nil, errors.New("icon bitmap is cut short")
	}
	pixels := d[pos : pos+stride*h]
	var mask []byte
	if end := pos + stride*h + maskStride*h; end <= len(d) {
		mask = d[pos+stride*h : end]
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for y := range h {
		row := pixels[(h-1-y)*stride:]
		for x := range w {
			var c color.NRGBA
			switch bpp {
			case 32:
				p := row[4*x:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: p[3]}
				anyAlpha = anyAlpha || p[3] != 0
			case 24:
				p := row[3*x:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
			default:
				bit := x * bpp
				idx := int(row[bit/8]>>(8-bpp-bit%8)) & (1<<bpp - 1)
				if idx < len(palette) {
					c = palette[idx]
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	// The mask makes pixels clear, unless a 32-bit icon carries its own
	// alpha. An icon without a mask stays opaque.
	if mask != nil && !(bpp == 32 && anyAlpha) {
		for y := range h {
			row := mask[(h-1-y)*maskStride:]
			for x := range w {
				if row[x/8]&(0x80>>(x%8)) != 0 {
					img.Pix[y*img.Stride+4*x+3] = 0
				}
			}
		}
	}
	return img, nil
}

// Pick chooses one image for each size in Sizes from entries: the one of
// that size with the most colours, or else one scaled down from the next
// larger image. Only sizes no larger than the largest image are filled.
// Entries that do not decode are skipped.
func Pick(entries []Entry) (map[int]*image.NRGBA, error) {
	type found struct {
		img   *image.NRGBA
		depth int
	}
	best := map[int]found{}
	for _, e := range entries {
		img, err := Decode(e)
		if err != nil {
			continue
		}
		s := img.Rect.Dx()
		depth := e.BitCount
		if bytes.HasPrefix(e.Data, pngMagic) {
			depth = 33
		}
		if cur, ok := best[s]; !ok || depth > cur.depth {
			best[s] = found{img, depth}
		}
	}
	if len(best) == 0 {
		return nil, ErrNoIcon
	}
	out := map[int]*image.NRGBA{}
	for _, s := range Sizes {
		if f, ok := best[s]; ok {
			out[s] = f.img
			continue
		}
		// The smallest image larger than s, to scale down from.
		var src *image.NRGBA
		for size, f := range best {
			if size > s && (src == nil || size < src.Rect.Dx()) {
				src = f.img
			}
		}
		if src != nil {
			out[s] = scaleDown(src, s)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoIcon
	}
	return out, nil
}

// scaleDown shrinks a square image to s by averaging the source pixels
// each target pixel covers, weighted by their alpha.
func scaleDown(src *image.NRGBA, s int) *image.NRGBA {
	n := src.Rect.Dx()
	out := image.NewNRGBA(image.Rect(0, 0, s, s))
	for ty := range s {
		y0, y1 := ty*n/s, (ty+1)*n/s
		for tx := range s {
			x0, x1 := tx*n/s, (tx+1)*n/s
			var r, g, b, a, count uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					p := src.Pix[y*src.Stride+4*x:]
					pa := uint64(p[3])
					r += uint64(p[0]) * pa
					g += uint64(p[1]) * pa
					b += uint64(p[2]) * pa
					a += pa
					count++
				}
			}
			if a == 0 {
				continue
			}
			q := out.Pix[ty*out.Stride+4*tx:]
			q[0], q[1], q[2] = uint8(r/a), uint8(g/a), uint8(b/a)
			q[3] = uint8(a / count)
		}
	}
	return out
}

// ReadAll reads at most limit bytes from r, and fails if there are more.
func ReadAll(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file is larger than %d bytes", limit)
	}
	return data, nil
}

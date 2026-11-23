package stick

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowPixelSize(t *testing.T) {
	for _, c := range []struct{ w, h, px int }{
		{1280, 800, 21},  // cells 13x27, 98x29
		{1920, 1080, 29}, // cells 18x36, 106x30
		{3840, 2160, 59},
		{2880, 1920, 52}, // 3:2
		{1024, 768, 17},
		{500, 300, 0}, // too small for 88x29 at any size from 8
	} {
		if got := windowPixelSize(c.w, c.h); got != c.px {
			t.Errorf("%dx%d: pixel size %d, want %d", c.w, c.h, got, c.px)
		}
	}
}

// The cell foot draws at the chosen size must leave 88x29. foot rounds the
// advance to the nearest pixel and the ascent and descent up.
func TestWindowCellsFit(t *testing.T) {
	for _, s := range [][2]int{{1280, 800}, {1366, 768}, {1920, 1080}, {1920, 1200}, {2560, 1440}, {2880, 1920}, {3840, 2160}} {
		px := windowPixelSize(s[0], s[1])
		cw := (2*jadeAdvance*px + jadeUnitsPerEm) / (2 * jadeUnitsPerEm)
		ch := (jadeAscent*px+jadeUnitsPerEm-1)/jadeUnitsPerEm + (jadeDescent*px+jadeUnitsPerEm-1)/jadeUnitsPerEm
		if s[0]/cw < screenCols || s[1]/ch < screenRows {
			t.Errorf("%dx%d at %d px: %dx%d cells", s[0], s[1], px, s[0]/cw, s[1]/ch)
		}
	}
}

func TestConsoleCellWidth(t *testing.T) {
	for _, c := range []struct{ w, h, cw int }{
		{1280, 800, 13},  // the stick has 12: 106x33
		{1920, 1080, 18}, // 106x30
		{3840, 2160, 37}, // the stick has 36: 106x30
		{800, 600, 9},    // under the smallest the stick has: kernel font
	} {
		if got := consoleCellWidth(c.w, c.h); got != c.cw {
			t.Errorf("%dx%d: cell width %d, want %d", c.w, c.h, got, c.cw)
		}
	}
}

func TestScreenPixels(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		data string
		w, h int
		ok   bool
	}{
		{"1280,800\n", 1280, 800, true},
		{"1920,1080", 1920, 1080, true},
		{"0,800\n", 0, 800, false},
		{"1280 800\n", 0, 0, false},
		{"123456,800\n", 0, 0, false},
		{"1280,800\n1280,800\n", 0, 0, false},
		{"", 0, 0, false},
	} {
		p := filepath.Join(dir, "size")
		if err := os.WriteFile(p, []byte(c.data), 0o600); err != nil {
			t.Fatal(err)
		}
		w, h, ok := screenPixels(p)
		if ok != c.ok || (ok && (w != c.w || h != c.h)) {
			t.Errorf("%q: %d,%d %v", c.data, w, h, ok)
		}
	}
	if _, _, ok := screenPixels(filepath.Join(dir, "missing")); ok {
		t.Error("missing file read as a size")
	}
}

// The metrics above must be the font's own: head.unitsPerEm, hhea ascent,
// descent, line gap and widest advance.
func TestJadeCodeMetrics(t *testing.T) {
	data, err := os.ReadFile("../../../themes/brand/openjade/fonts/JadeCode-VF.ttf")
	if err != nil {
		t.Fatal(err)
	}
	be := binary.BigEndian
	table := func(tag string) []byte {
		n := int(be.Uint16(data[4:]))
		for i := 0; i < n; i++ {
			rec := data[12+16*i:]
			if string(rec[:4]) == tag {
				off, size := be.Uint32(rec[8:]), be.Uint32(rec[12:])
				return data[off : off+size]
			}
		}
		t.Fatalf("no %s table", tag)
		return nil
	}
	head, hhea := table("head"), table("hhea")
	got := [5]int{int(be.Uint16(head[18:])), int(int16(be.Uint16(hhea[4:]))), -int(int16(be.Uint16(hhea[6:]))), int(int16(be.Uint16(hhea[8:]))), int(be.Uint16(hhea[10:]))}
	want := [5]int{jadeUnitsPerEm, jadeAscent, jadeDescent, 0, jadeAdvance}
	if got != want {
		t.Errorf("units per em, ascent, descent, line gap, widest advance: %v, want %v", got, want)
	}
}

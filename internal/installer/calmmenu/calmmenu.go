// Package calmmenu draws the calm text menu from THEMEBRAIN section 18:
// a page in a frame with the same margin on every side of the terminal
// (see FrameFor), grown evenly around a short menu (see Render), with an
// optional logo on top. Colours come from the 16 colour slots only, so the same output
// works in foot and on the Linux console.
package calmmenu

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Width is where text wraps on a terminal that shows no page, such as
// the installed system's text between menus (see Wrap).
const Width = 66

// ANSI codes. Dim uses slot 8 (bright black), not faint, so it shows the
// brand grey on every terminal.
const (
	bold   = "\x1b[1m"
	dim    = "\x1b[90m"
	accent = "\x1b[36m"
	reset  = "\x1b[0m"
)

// Clear resets the colours and the scroll region, clears the screen and
// its scrollback and puts the cursor home. A page that showed recovery
// material must not stay in the scrollback of a terminal emulator or ssh
// client.
const Clear = "\x1b[0m\x1b[r\x1b[2J\x1b[3J\x1b[H"

// Item is one numbered choice.
type Item struct {
	Key   string // what to type, "1"
	Label string // "Install gjallarOS"
	Desc  string // optional second line, dim
}

// Field is one status pair on the line under the rule.
type Field struct {
	Name  string // "Network", dim
	Value string // "Cable, connected"
}

// Menu is everything one screen shows.
type Menu struct {
	Title  string   // bold
	Status []Field  // one line, separated by four spaces
	Body   []string // plain text under the status line, wrapped to the page
	Groups [][]Item // a blank line between groups
	Note   string   // optional text above the hint, such as an error
	Hint   string   // optional, dim
	Prompt string   // "Choice: "
}

// Logo is a picture drawn above the block, for example a sixel.
// Cols and Rows are its size in cells; Rows 0 means no logo.
type Logo struct {
	Cols, Rows int
}

// Place says where things landed, so the caller can draw the logo and
// leave the cursor after the prompt. Rows and columns start at 1.
type Place struct {
	LogoRow, LogoCol     int
	PromptRow, PromptCol int
	Frame                Frame // the page's frame, for text written after it (see ColumnIn)
}

type span struct{ style, text string }

// lines are the menu's rows for a page width cells wide.
func (m Menu) lines(width int) [][]span {
	var ls [][]span
	ls = append(ls, []span{{bold, m.Title}})
	ls = append(ls, []span{{dim, strings.Repeat("─", width)}})
	if len(m.Status) > 0 {
		var l []span
		for i, f := range m.Status {
			sep := ""
			if i > 0 {
				sep = "    "
			}
			l = append(l, span{dim, sep + f.Name + " "}, span{"", f.Value})
		}
		ls = append(ls, l)
	}
	if len(m.Body) > 0 {
		ls = append(ls, nil)
		for _, l := range strings.Split(wrapAt(strings.Join(m.Body, "\n"), width), "\n") {
			ls = append(ls, []span{{"", l}})
		}
	}
	// Numbers are right-aligned to the widest one, so labels start in one
	// column when a list reaches 10.
	kw := 0
	for _, g := range m.Groups {
		for _, it := range g {
			kw = max(kw, utf8.RuneCountInString(it.Key))
		}
	}
	indent := strings.Repeat(" ", kw+3)
	for _, g := range m.Groups {
		ls = append(ls, nil)
		for _, it := range g {
			key := strings.Repeat(" ", kw-utf8.RuneCountInString(it.Key)) + it.Key
			ls = append(ls, []span{{accent, " " + key + "  "}, {"", it.Label}})
			if it.Desc != "" {
				ls = append(ls, []span{{dim, indent + it.Desc}})
			}
		}
	}
	ls = append(ls, nil)
	if m.Note != "" {
		for _, l := range strings.Split(wrapAt(m.Note, width), "\n") {
			ls = append(ls, []span{{"", l}})
		}
		ls = append(ls, nil)
	}
	if m.Hint != "" {
		ls = append(ls, []span{{dim, m.Hint}})
	}
	ls = append(ls, []span{{"", m.Prompt}})
	return ls
}

// Render clears the screen and draws the menu for a terminal of size sz:
// the page in the terminal's frame (see FrameFor), centred from top to
// bottom, the logo above it centred across the screen. A page smaller
// than the frame gets a smaller frame, its margin grown until the space
// at the sides is as wide as the space above and below the page is tall,
// the page wrapped again at each width (see fitFrame); Place.Frame is the
// frame used. The logo is left out when it does not fit beside the page;
// then Place.LogoRow is 0. A page taller than the frame starts at its top
// and scrolls inside it. With colour false Render writes plain text (for
// logs and tests) but still positions every line. Text from outside, such
// as a network name, should be made printable before it reaches the menu;
// Render turns any control character that still gets here into '?' as
// well, so no field can send the terminal a control sequence.
func Render(w io.Writer, m Menu, logo Logo, sz Size, colour bool) (Place, error) {
	f := FrameFor(sz)
	ls := m.lines(f.Width)
	if logo.Cols > sz.Cols || logo.Rows+1+len(ls) > f.Height {
		logo = Logo{}
	}
	gap := 0
	if logo.Rows > 0 {
		gap = 1 // one empty row between logo and title
	}
	// The frame shrinks while the space at one side is still no wider
	// than the space above or below the page is tall (half of the rows the
	// page leaves free, in columns of the same length, see Size.across),
	// the page wrapped again at each width. No word may be cut, the page
	// must still fit from top to bottom, and the logo must not stick out
	// past the frame.
	f = fitFrame(sz, func(n Frame) bool {
		nl := m.lines(n.Width)
		h := logo.Rows + gap + len(nl)
		return widest(nl) <= n.Width && logo.Cols <= n.Width && h <= n.Height && n.Col-1 <= sz.across(sz.Rows-h)/2
	})
	ls = m.lines(f.Width)
	height := logo.Rows + gap + len(ls)
	x0 := f.Col
	y0 := f.Row + max(0, (f.Height-height)/2)

	var b strings.Builder
	b.WriteString(Clear)
	b.WriteString(f.scroll())
	p := Place{Frame: f}
	if logo.Rows > 0 {
		p.LogoRow = y0
		p.LogoCol = max(0, (sz.Cols-logo.Cols)/2) + 1
	}
	top := y0 + logo.Rows + gap
	// Lines go out in order with a line end after each, so a page taller
	// than the frame starts at its top and scrolls inside it.
	fmt.Fprintf(&b, "\x1b[%d;1H", top)
	for i, l := range ls {
		if i > 0 {
			b.WriteString("\r\n")
		}
		if len(l) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%dG", x0)
		used := 0
		for _, s := range l {
			t := clip(printable(s.text), f.Width-used)
			used += utf8.RuneCountInString(t)
			if colour && s.style != "" {
				b.WriteString(s.style + t + reset)
			} else {
				b.WriteString(t)
			}
		}
	}
	p.PromptRow = min(top+len(ls)-1, f.bottom())
	p.PromptCol = x0 + min(utf8.RuneCountInString(m.Prompt), f.Width)
	fmt.Fprintf(&b, "\x1b[%dG", p.PromptCol)
	_, err := io.WriteString(w, b.String())
	return p, err
}

// widest is the width of the widest of ls in cells.
func widest(ls [][]span) int {
	w := 0
	for _, l := range ls {
		n := 0
		for _, s := range l {
			n += utf8.RuneCountInString(printable(s.text))
		}
		w = max(w, n)
	}
	return w
}

// printable turns control characters, text-direction marks and broken
// UTF-8 into '?'.
func printable(s string) string {
	ok := true
	for _, r := range s {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Sixel is a checked sixel picture: one DCS string and nothing else, so
// drawing it cannot send the terminal any other control sequence.
type Sixel struct {
	Data          []byte
	Width, Height int // pixels, from the raster attributes
}

// ParseSixel checks data and reads its size from the raster attributes
// ("Pan;Pad;Ph;Pv) that img2sixel writes after the q.
func ParseSixel(data []byte) (Sixel, error) {
	start := bytes.Index(data, []byte("\x1bP"))
	if start != 0 {
		return Sixel{}, errors.New("does not start with a DCS")
	}
	q := bytes.IndexByte(data, 'q')
	if q < 0 || !bytes.HasSuffix(data, []byte("\x1b\\")) {
		return Sixel{}, errors.New("not a sixel DCS")
	}
	body := data[q+1 : len(data)-2]
	for _, c := range body {
		// Sixel data is printable ASCII; line ends are allowed between
		// bands. Anything else (ESC, other C0, C1, UTF-8) is refused.
		if (c < 0x20 || c > 0x7e) && c != '\n' && c != '\r' {
			return Sixel{}, fmt.Errorf("byte %#x inside the sixel", c)
		}
	}
	for _, c := range data[2:q] {
		if (c < '0' || c > '9') && c != ';' {
			return Sixel{}, fmt.Errorf("byte %#x in the sixel parameters", c)
		}
	}
	if len(body) == 0 || body[0] != '"' {
		return Sixel{}, errors.New("no raster attributes")
	}
	end := bytes.IndexAny(body[1:], "#!-$?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~")
	if end < 0 {
		return Sixel{}, errors.New("raster attributes do not end")
	}
	f := strings.Split(string(body[1:1+end]), ";")
	if len(f) != 4 {
		return Sixel{}, errors.New("raster attributes need four numbers")
	}
	w, e1 := strconv.Atoi(f[2])
	h, e2 := strconv.Atoi(f[3])
	if e1 != nil || e2 != nil || w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return Sixel{}, errors.New("bad picture size")
	}
	return Sixel{Data: data, Width: w, Height: h}, nil
}

// Cells is the size of the picture in cells of cw by ch pixels.
func (s Sixel) Cells(cw, ch int) Logo {
	if cw <= 0 || ch <= 0 {
		return Logo{}
	}
	return Logo{Cols: (s.Width + cw - 1) / cw, Rows: (s.Height + ch - 1) / ch}
}

// Draw writes the picture at p's logo place and puts the cursor back.
func (s Sixel) Draw(w io.Writer, p Place) error {
	if p.LogoRow == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "\x1b7\x1b[%d;%dH", p.LogoRow, p.LogoCol); err != nil {
		return err
	}
	if _, err := w.Write(s.Data); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\x1b8")
	return err
}

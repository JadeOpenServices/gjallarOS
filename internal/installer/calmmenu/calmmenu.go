// Package calmmenu draws the calm text menu from THEMEBRAIN section 18:
// a 66 column block, centred in the terminal, with an optional logo on
// top. Colours come from the 16 colour slots only, so the same output
// works in foot and on the Linux console.
package calmmenu

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Width is the block width in columns.
const Width = 66

// ANSI codes. Dim uses slot 8 (bright black), not faint, so it shows the
// brand grey on every terminal.
const (
	bold   = "\x1b[1m"
	dim    = "\x1b[90m"
	accent = "\x1b[36m"
	reset  = "\x1b[0m"
)

// Clear resets the colours, clears the screen and puts the cursor home.
const Clear = "\x1b[0m\x1b[2J\x1b[H"

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
	Groups [][]Item // a blank line between groups
	Note   string   // optional line above the hint, such as an error
	Hint   string   // dim
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
}

type span struct{ style, text string }

func (m Menu) lines() [][]span {
	var ls [][]span
	ls = append(ls, []span{{bold, m.Title}})
	ls = append(ls, []span{{dim, strings.Repeat("─", Width)}})
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
	for _, g := range m.Groups {
		ls = append(ls, nil)
		for _, it := range g {
			ls = append(ls, []span{{accent, fmt.Sprintf(" %s  ", it.Key)}, {"", it.Label}})
			if it.Desc != "" {
				ls = append(ls, []span{{dim, "    " + it.Desc}})
			}
		}
	}
	ls = append(ls, nil)
	if m.Note != "" {
		ls = append(ls, []span{{"", m.Note}}, nil)
	}
	ls = append(ls, []span{{dim, m.Hint}})
	ls = append(ls, []span{{"", m.Prompt}})
	return ls
}

// Height is how many rows the menu takes without a logo.
func (m Menu) Height() int { return len(m.lines()) }

// Render clears the screen and draws the menu for a terminal of cols by
// rows cells. With colour false it writes plain text (for logs and tests)
// but still positions every line. Text from outside, such as a network
// name, must be made printable before it reaches the menu.
func Render(w io.Writer, m Menu, logo Logo, cols, rows int, colour bool) (Place, error) {
	ls := m.lines()
	width := min(Width, cols)
	gap := 0
	if logo.Rows > 0 {
		gap = 1 // one empty row between logo and title
	}
	height := logo.Rows + gap + len(ls)
	x0 := max(0, (cols-width)/2) + 1
	y0 := max(0, (rows-height)/2) + 1

	var b strings.Builder
	b.WriteString(Clear)
	p := Place{}
	if logo.Rows > 0 {
		p.LogoRow = y0
		p.LogoCol = max(0, (cols-logo.Cols)/2) + 1
	}
	top := y0 + logo.Rows + gap
	for i, l := range ls {
		if len(l) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH", top+i, x0)
		used := 0
		for _, s := range l {
			t := clip(s.text, width-used)
			used += utf8.RuneCountInString(t)
			if colour && s.style != "" {
				b.WriteString(s.style + t + reset)
			} else {
				b.WriteString(t)
			}
		}
	}
	p.PromptRow = top + len(ls) - 1
	p.PromptCol = x0 + min(utf8.RuneCountInString(m.Prompt), width)
	fmt.Fprintf(&b, "\x1b[%d;%dH", p.PromptRow, p.PromptCol)
	_, err := io.WriteString(w, b.String())
	return p, err
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

package calmmenu

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var csi = regexp.MustCompile(`^\x1b\[([\d;]*)([A-Za-z])`)

// screen replays the output into a grid of runes: cursor moves (CUP and
// CHA), carriage return and line feed, with scrolling at the bottom.
func screen(t *testing.T, out string, cols, rows int) []string {
	t.Helper()
	blank := func() []rune { return []rune(strings.Repeat(" ", cols)) }
	g := make([][]rune, rows)
	for i := range g {
		g[i] = blank()
	}
	r, c := 0, 0
	top, bot := 0, rows-1 // scroll region
	for len(out) > 0 {
		if m := csi.FindStringSubmatch(out); m != nil {
			n := strings.Split(m[1], ";")
			switch m[2] {
			case "H":
				r, c = 0, 0
				if len(n) == 2 {
					r, _ = strconv.Atoi(n[0])
					c, _ = strconv.Atoi(n[1])
					r, c = r-1, c-1
				}
			case "G":
				c, _ = strconv.Atoi(n[0])
				c--
			case "A":
				k, _ := strconv.Atoi(n[0])
				r = max(0, r-max(1, k))
			case "J":
				for i := c; i < cols; i++ {
					g[r][i] = ' '
				}
				for i := r + 1; i < rows; i++ {
					g[i] = blank()
				}
			case "K":
				for i := c; i < cols; i++ {
					g[r][i] = ' '
				}
			case "r":
				top, bot = 0, rows-1
				if len(n) == 2 {
					top, _ = strconv.Atoi(n[0])
					bot, _ = strconv.Atoi(n[1])
					top, bot = top-1, bot-1
				}
				r, c = 0, 0
			}
			out = out[len(m[0]):]
			continue
		}
		ch, size := utf8.DecodeRuneInString(out)
		out = out[size:]
		switch ch {
		case '\r':
			c = 0
		case '\n':
			if r == bot {
				copy(g[top:bot], g[top+1:bot+1])
				g[bot] = blank()
			} else if r < rows-1 {
				r++
			}
		default:
			if c >= cols {
				t.Fatalf("row %d runs past column %d", r+1, cols)
			}
			g[r][c] = ch
			c++
		}
	}
	s := make([]string, rows)
	for i := range g {
		s[i] = strings.TrimRight(string(g[i]), " ")
	}
	return s
}

// example is a menu shaped like the stick's, with made-up values.
func example(note string) Menu {
	return Menu{
		Title:  "gjallarOS installer",
		Status: []Field{{"Network", "Cable, connected"}, {"Keyboard", "gb"}, {"Channel", "stable"}},
		Groups: [][]Item{
			{{"1", "Install gjallarOS", "Set up gjallarOS on this computer"}},
			{{"2", "Recovery", "Unlock an installed gjallarOS with its recovery key"}, {"3", "Shell", "A command line. Type exit to come back here"}},
			{{"4", "Network", ""}, {"5", "Keyboard layout", ""}, {"6", "Advanced", ""}},
			{{"7", "Restart", ""}, {"8", "Power off", ""}},
		},
		Note:   note,
		Hint:   "Type a number and press Enter.",
		Prompt: "Choice: ",
	}
}

func TestCentredOn120x33(t *testing.T) {
	var b strings.Builder
	p, err := Render(&b, example(""), Logo{Cols: 21, Rows: 4}, 120, 33, false)
	if err != nil {
		t.Fatal(err)
	}
	s := screen(t, b.String(), 120, 33)
	// 4 logo rows, 1 gap, 21 menu lines = 26. The margin grows from 2 rows
	// to 3 (27 rows high) and 6 columns, so rows 4-30 and columns 7-114.
	// y0 = 4 + (27-26)/2 = 4.
	if p.Frame != (Frame{Col: 7, Row: 4, Width: 108, Height: 27}) {
		t.Errorf("frame %+v", p.Frame)
	}
	if p.LogoRow != 4 || p.LogoCol != 50 {
		t.Errorf("logo at %d,%d, want 4,50", p.LogoRow, p.LogoCol)
	}
	if got := s[8]; got != strings.Repeat(" ", 6)+"gjallarOS installer" {
		t.Errorf("title row %q", got)
	}
	if got := s[9]; got != strings.Repeat(" ", 6)+strings.Repeat("─", 108) {
		t.Errorf("rule does not fill the frame: %q", got)
	}
	if p.PromptRow != 29 || p.PromptCol != 7+len("Choice: ") {
		t.Errorf("prompt at %d,%d", p.PromptRow, p.PromptCol)
	}
	if !strings.Contains(s[12], " 1  Install gjallarOS") {
		t.Errorf("item row %q", s[12])
	}
}

// A short menu gets the same space on every side (two columns for each
// row), on wide screens and on 4:3 ones: 640x480 and 800x600 on the
// console (8x16 cells), 1024x768 in foot (about 11x24).
func TestShortMenuHasEvenSpace(t *testing.T) {
	for _, c := range []struct {
		cols, rows int
		want       Frame
	}{
		{116, 33, Frame{Col: 13, Row: 7, Width: 92, Height: 21}},
		{80, 30, Frame{Col: 9, Row: 5, Width: 64, Height: 22}},
		{100, 37, Frame{Col: 17, Row: 9, Width: 68, Height: 21}},
		{93, 32, Frame{Col: 11, Row: 6, Width: 73, Height: 22}},
	} {
		var b strings.Builder
		p, err := Render(&b, example(""), Logo{}, c.cols, c.rows, false)
		if err != nil {
			t.Fatal(err)
		}
		if p.Frame != c.want {
			t.Errorf("%dx%d: frame %+v, want %+v", c.cols, c.rows, p.Frame, c.want)
		}
		s := screen(t, b.String(), c.cols, c.rows)
		inBlock(t, s, p.Frame.Col, p.Frame.Width)
		first, last := -1, -1
		for i, l := range s {
			if strings.TrimSpace(l) != "" {
				last = i
				if first < 0 {
					first = i
				}
			}
		}
		above, below := first, c.rows-1-last
		if above < p.Frame.Row-1 || below < p.Frame.Row-1 || above-below > 1 || below-above > 1 {
			t.Errorf("%dx%d: %d rows above the page, %d below, margin %d", c.cols, c.rows, above, below, p.Frame.Row-1)
		}
	}
}

// The frame never gets so narrow that a row of the page wraps once more
// or is cut: every row the base frame shows is shown whole.
func TestFitKeepsEveryRow(t *testing.T) {
	m := example("The network did not answer. Check the cable or pick a wireless network, then try again from this menu.")
	m.Groups = append(m.Groups, []Item{{"9", "A rather long label that is wider than the rest of the menu put together", ""}})
	for _, size := range [][2]int{{80, 30}, {100, 37}, {116, 33}, {160, 50}} {
		cols, rows := size[0], size[1]
		var b strings.Builder
		p, err := Render(&b, m, Logo{}, cols, rows, false)
		if err != nil {
			t.Fatal(err)
		}
		s := screen(t, b.String(), cols, rows)
		inBlock(t, s, p.Frame.Col, p.Frame.Width)
		page := strings.Join(s, "\n")
		for _, want := range []string{" 9  " + m.Groups[4][0].Label, "    " + m.Groups[1][0].Desc} {
			if !strings.Contains(page, want) {
				t.Errorf("%dx%d: %q cut:\n%s", cols, rows, want, page)
			}
		}
		shown := 0
		for _, l := range s {
			if strings.TrimSpace(l) != "" {
				shown++
			}
		}
		base := 0
		for _, l := range m.lines(FrameFor(cols, rows).Width) {
			if len(l) > 0 {
				base++
			}
		}
		if shown != base {
			t.Errorf("%dx%d: %d rows shown, %d in the base frame", cols, rows, shown, base)
		}
	}
}

func TestNoteSitsAboveTheHint(t *testing.T) {
	m := example("Type one of the numbers 1 to 8.")
	if n := len(m.lines(Width)) - len(example("").lines(Width)); n != 2 {
		t.Fatalf("note adds %d rows, want 2", n)
	}
	var b strings.Builder
	p, err := Render(&b, m, Logo{}, 80, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	s := screen(t, b.String(), 80, 30)
	if !strings.HasSuffix(s[p.PromptRow-4], "Type one of the numbers 1 to 8.") || !strings.HasSuffix(s[p.PromptRow-2], "press Enter.") {
		t.Errorf("note or hint misplaced:\n%s", strings.Join(s, "\n"))
	}
}

func TestNarrowTerminalClips(t *testing.T) {
	var b strings.Builder
	if _, err := Render(&b, example("a note that is longer than forty columns for sure"), Logo{}, 40, 25, false); err != nil {
		t.Fatal(err)
	}
	screen(t, b.String(), 40, 25) // fails if any line runs past column 40
}

func TestColourCodes(t *testing.T) {
	var b strings.Builder
	Render(&b, example("x"), Logo{}, 120, 33, true)
	out := b.String()
	for _, c := range []string{bold + "gjallarOS installer" + reset, accent + " 1  " + reset, dim + "Type a number"} {
		if !strings.Contains(out, c) {
			t.Errorf("missing %q", c)
		}
	}
	if strings.Contains(out, "\x1b[2m") || strings.Contains(out, "\x1b[31m") {
		t.Error("faint or red is used; dim must be slot 8 and errors plain")
	}
}

func TestParseSixel(t *testing.T) {
	good := "\x1bPq\"1;1;323;128#0;2;6;9;13#0!323~-\x1b\\"
	s, err := ParseSixel([]byte(good))
	if err != nil || s.Width != 323 || s.Height != 128 {
		t.Fatalf("ParseSixel = %+v, %v", s, err)
	}
	if l := s.Cells(10, 25); l.Cols != 33 || l.Rows != 6 {
		t.Errorf("cells %+v, want 33x6", l)
	}
	if l := s.Cells(0, 25); l.Rows != 0 {
		t.Errorf("no pixel size still gave %+v", l)
	}
	// Loophole check: only one DCS reaches the terminal.
	for name, bad := range map[string]string{
		"esc inside":    "\x1bPq\"1;1;2;2#0\x1b]2;title\x07!2~\x1b\\",
		"c1 inside":     "\x1bPq\"1;1;2;2\x9b2J\x1b\\",
		"text before":   "x\x1bPq\"1;1;2;2!2~\x1b\\",
		"no end":        "\x1bPq\"1;1;2;2!2~",
		"no raster":     "\x1bPq#0!2~\x1b\\",
		"huge":          "\x1bPq\"1;1;99999;2!2~\x1b\\",
		"odd parameter": "\x1bP0;x;0q\"1;1;2;2!2~\x1b\\",
	} {
		if _, err := ParseSixel([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSixelDrawRestoresCursor(t *testing.T) {
	s, err := ParseSixel([]byte("\x1bPq\"1;1;2;2!2~\x1b\\"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := s.Draw(&b, Place{LogoRow: 3, LogoCol: 7}); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "\x1b7\x1b[3;7H"+string(s.Data)+"\x1b8" {
		t.Errorf("Draw wrote %q", got)
	}
	b.Reset()
	s.Draw(&b, Place{})
	if b.Len() != 0 {
		t.Error("drew without a logo place")
	}
}

// The logo files the stick ships, when the test runs next to a build.
func TestBuiltLogo(t *testing.T) {
	dir := os.Getenv("GJALLAR_STICK_LOGO")
	if dir == "" {
		t.Skip("GJALLAR_STICK_LOGO not set")
	}
	for _, name := range []string{"logo-1x.six", "logo-2x.six"} {
		data, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseSixel(data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// With 10 items the numbers are right-aligned, so every label and every
// description starts in the same column.
func TestTwoDigitKeysAlign(t *testing.T) {
	m := example("")
	m.Groups = append(m.Groups[:3:3], []Item{{"9", "Queue the key again", "When the blue screen was missed"}, {"10", "Power off", ""}})
	var b strings.Builder
	if _, err := Render(&b, m, Logo{}, 120, 33, false); err != nil {
		t.Fatal(err)
	}
	labels := map[int]bool{}
	descs := map[int]bool{}
	for _, row := range screen(t, b.String(), 120, 33) {
		for _, l := range []string{"Install gjallarOS", "Queue the key again", "Power off", "Restart"} {
			if i := strings.Index(row, l); i >= 0 && !strings.Contains(row, "Type") {
				labels[i] = true
			}
		}
		for _, d := range []string{"Set up gjallarOS", "When the blue screen"} {
			if i := strings.Index(row, d); i >= 0 {
				descs[i] = true
			}
		}
		if strings.Contains(row, " 9  Queue") && !strings.Contains(row, "  9  Queue") {
			t.Errorf("9 is not right-aligned under 10: %q", row)
		}
	}
	if len(labels) != 1 || len(descs) != 1 {
		t.Errorf("labels start in columns %v, descriptions in %v; want one each", labels, descs)
	}
}

// Outside text such as network names may hold escape codes; Render shows
// them as '?' and never sends them to the terminal (baka-c7 item 4).
func TestRenderEscapesOutsideText(t *testing.T) {
	var b strings.Builder
	m := Menu{
		Title:  "Network",
		Body:   []string{"Body \x1b[2J text"},
		Groups: [][]Item{{{Key: "1", Label: "Cafe\x1b[2J‮\x07", Desc: "dim\x1b]0;x\x07"}}},
		Prompt: "Choice: ",
	}
	if _, err := Render(&b, m, Logo{}, 80, 24, true); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if n := strings.Count(out, "\x1b[2J"); n != 1 {
		t.Fatalf("clear sequence %d times, want only the page's own:\n%q", n, out)
	}
	for _, bad := range []string{"‮", "\x07", "\x1b]"} {
		if strings.Contains(out, bad) {
			t.Fatalf("raw %q reached the terminal:\n%q", bad, out)
		}
	}
	if !strings.Contains(out, "Cafe?[2J??") {
		t.Fatalf("label not shown escaped:\n%q", out)
	}
}

// On every screen size, not a list of known ones, the menu's frame has
// the same margin on all four sides and shows the same rows as the base
// frame, none cut.
func TestFitOnEverySize(t *testing.T) {
	m := example("")
	for cols := 40; cols <= 240; cols++ {
		for rows := 15; rows <= 80; rows++ {
			var b strings.Builder
			p, err := Render(&b, m, Logo{}, cols, rows, false)
			if err != nil {
				t.Fatal(err)
			}
			f, base := p.Frame, FrameFor(cols, rows)
			if f != base && f != withMargin(cols, rows, f.Row-1) {
				t.Fatalf("%dx%d: frame %+v has uneven margins", cols, rows, f)
			}
			if f.Width < 20 || f.Height < 5 || f.Row < base.Row || f.Col < base.Col {
				t.Fatalf("%dx%d: frame %+v smaller than allowed (base %+v)", cols, rows, f, base)
			}
			want, got := m.lines(base.Width), m.lines(f.Width)
			if len(got) != len(want) || widest(got) > f.Width && widest(want) <= base.Width {
				t.Fatalf("%dx%d: frame %+v wraps or cuts the page", cols, rows, f)
			}
		}
	}
}

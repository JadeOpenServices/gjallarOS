package calmmenu

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var cup = regexp.MustCompile(`\x1b\[(\d+);(\d+)H`)

// screen replays the cursor moves into a grid of runes.
func screen(t *testing.T, out string, cols, rows int) []string {
	t.Helper()
	g := make([][]rune, rows)
	for i := range g {
		g[i] = []rune(strings.Repeat(" ", cols))
	}
	out = strings.TrimPrefix(out, Clear)
	locs := cup.FindAllStringSubmatchIndex(out, -1)
	for i, l := range locs {
		r, _ := strconv.Atoi(out[l[2]:l[3]])
		c, _ := strconv.Atoi(out[l[4]:l[5]])
		end := len(out)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		for j, ch := range []rune(out[l[1]:end]) {
			if c-1+j >= cols {
				t.Fatalf("row %d runs past column %d", r, cols)
			}
			g[r-1][c-1+j] = ch
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
	// 4 logo rows, 1 gap, 21 menu lines = 26, so y0 = (33-26)/2 = 3.
	if p.LogoRow != 4 || p.LogoCol != 50 {
		t.Errorf("logo at %d,%d, want 4,50", p.LogoRow, p.LogoCol)
	}
	if got := s[8]; got != strings.Repeat(" ", 27)+"gjallarOS installer" {
		t.Errorf("title row %q", got)
	}
	if got := s[9]; utf8.RuneCountInString(strings.TrimLeft(got, " ")) != Width {
		t.Errorf("rule is not %d wide: %q", Width, got)
	}
	if p.PromptRow != 29 || p.PromptCol != 28+len("Choice: ") {
		t.Errorf("prompt at %d,%d", p.PromptRow, p.PromptCol)
	}
	if !strings.Contains(s[12], " 1  Install gjallarOS") {
		t.Errorf("item row %q", s[12])
	}
}

func TestNoteSitsAboveTheHint(t *testing.T) {
	m := example("Type one of the numbers 1 to 8.")
	if m.Height() != example("").Height()+2 {
		t.Fatalf("note adds %d rows, want 2", m.Height()-example("").Height())
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

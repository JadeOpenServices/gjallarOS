package calmmenu

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPageBodyWrapsInBlock(t *testing.T) {
	m := Menu{
		Title:  "Network",
		Body:   []string{"Pick a release channel. Stable gets tested updates. Staging and dev get them earlier and break more often."},
		Groups: [][]Item{{{"1", "stable", ""}, {"2", "staging", ""}, {"3", "dev", ""}}},
		Prompt: "Channel: ",
	}
	var b bytes.Buffer
	p, err := Render(&b, m, Logo{}, Size{Cols: 60, Rows: 24}, false)
	if err != nil {
		t.Fatal(err)
	}
	s := screen(t, b.String(), 60, 24)
	f := p.Frame
	inBlock(t, s, f.Col, f.Width)
	// Every word of the body is there, in order, on rows of the frame.
	if !strings.Contains(strings.Join(strings.Fields(strings.Join(s, " ")), " "), m.Body[0]) {
		t.Fatalf("body not wrapped in the frame:\n%s", strings.Join(s, "\n"))
	}
	if p.PromptCol != f.Col+len("Channel: ") {
		t.Fatalf("prompt column %d", p.PromptCol)
	}
}

func TestTallPageScrollsFromTop(t *testing.T) {
	body := make([]string, 40)
	for i := range body {
		body[i] = "line"
	}
	var b bytes.Buffer
	p, err := Render(&b, Menu{Title: "Steps", Body: body, Prompt: "> "}, Logo{}, Size{Cols: 76, Rows: 24}, false)
	if err != nil {
		t.Fatal(err)
	}
	s := screen(t, b.String(), 76, 24)
	f := FrameFor(Size{Cols: 76, Rows: 24})
	if !strings.Contains(b.String(), "\x1b[2;1H") || p.PromptRow != f.bottom() {
		t.Fatalf("tall page: row %d\n%q", p.PromptRow, b.String()[:60])
	}
	if s[0] != "" || s[23] != "" || s[p.PromptRow-1] != "  > " && s[p.PromptRow-1] != "  >" {
		t.Fatalf("tall page scrolled into the margin:\n%s", strings.Join(s, "\n"))
	}
}

func TestColumnSetsColumnAfterEachLineEnd(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, Size{Cols: 76, Rows: 24})
	io.WriteString(c, "one\ntwo\r50%\rdone\n")
	// The cursor goes to the column right after a line end, so a prompt
	// written by another program under the Column starts there too.
	want := "\x1b[3Gone\n\x1b[3Gtwo\r\x1b[3G50%\r\x1b[3Gdone\n\x1b[3G"
	if b.String() != want {
		t.Fatalf("got %q", b.String())
	}
	if c.Terminal() != io.Writer(&b) {
		t.Fatal("Terminal must return the writer under the Column")
	}
}

func TestHeadDrawsTitleAndRule(t *testing.T) {
	var b bytes.Buffer
	if _, err := Head(&b, "Installing", Size{Cols: 76, Rows: 24}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\x1b[2;3HInstalling\r\n\x1b[3G─") {
		t.Fatalf("got %q", b.String())
	}
}

// The title comes from the environment of a child program; Head shows
// it as text and keeps it on its row.
func TestHeadTitleIsPlainText(t *testing.T) {
	var b bytes.Buffer
	if _, err := Head(&b, "Ins\x1b[2Jtall"+strings.Repeat("x", 200), Size{Cols: 76, Rows: 24}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Count(b.String(), "\x1b[2J") != strings.Count(Clear, "\x1b[2J") {
		t.Fatalf("escape in the title reached the terminal: %q", b.String())
	}
	s := screen(t, b.String(), 76, 24)
	inBlock(t, s, 3, 72)
	if !strings.HasPrefix(s[1], "  Ins") {
		t.Fatalf("title row %q", s[1])
	}
}

func TestPageOnConsole(t *testing.T) {
	m := Menu{
		Title:  "Release channel",
		Body:   []string{"Pick a release channel. Stable gets tested updates. Staging and dev get them earlier and break more often."},
		Groups: [][]Item{{{"1", "stable", ""}, {"2", "staging", ""}, {"3", "dev", ""}}},
		Hint:   "Type a number and press Enter.",
		Prompt: "Channel [stable]: ",
	}
	var b strings.Builder
	p, err := Render(&b, m, Logo{}, Size{Cols: 80, Rows: 25}, false)
	if err != nil {
		t.Fatal(err)
	}
	s := screen(t, b.String(), 80, 25)
	inBlock(t, s, p.Frame.Col, p.Frame.Width)
	pad := strings.Repeat(" ", p.Frame.Col-1)
	if !strings.Contains(strings.Join(s, "\n"), "\n"+pad+"Release channel\n"+pad+"─") {
		t.Errorf("page:\n%s", strings.Join(s, "\n"))
	}
}

// inBlock fails when a row of s has text left of the block or past it.
func inBlock(t *testing.T, s []string, col, width int) {
	t.Helper()
	for i, l := range s {
		r := []rune(l)
		if len(r) > col-1+width {
			t.Errorf("row %d runs past the block: %q", i+1, l)
		}
		if strings.TrimSpace(l) != "" && strings.TrimSpace(string(r[:min(len(r), col-1)])) != "" {
			t.Errorf("row %d starts left of the block: %q", i+1, l)
		}
	}
}

func TestColumnBreaksLongLinesInBlock(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, Size{Cols: 60, Rows: 16})
	io.WriteString(c, "Machine support: best effort. No ODDC model matches this machine; nothing optional is switched on.\n")
	io.WriteString(c, "building '/nix/store/00000000000000000000000000000000-nixos-system-example-26.05.drv'...\n")
	s := screen(t, b.String(), 60, 16)
	inBlock(t, s, c.Col, c.PageWidth())
	if s[0] != "  Machine support: best effort. No ODDC model matches this" ||
		s[1] != "  machine; nothing optional is switched on." {
		t.Errorf("no break at a space:\n%s", strings.Join(s, "\n"))
	}
	if s[3] != "  xos-system-example-26.05.drv'..." {
		t.Errorf("long word:\n%s", strings.Join(s, "\n"))
	}
}

func TestColumnNarrowTerminal(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, Size{Cols: 18, Rows: 10})
	io.WriteString(c, strings.Repeat("word ", 20)+"\n")
	s := screen(t, b.String(), 18, 10)
	inBlock(t, s, 1, 18)
}

func TestColumnEscapesTakeNoRoom(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, Size{Cols: 76, Rows: 24})
	// A colour code split over two writes, as a pipe may deliver it.
	io.WriteString(c, "\x1b[32")
	io.WriteString(c, ";1m"+strings.Repeat("x", c.PageWidth())+"\x1b[0m\n")
	io.WriteString(c, "\x1b]0;title\x07ok\n")
	if strings.Contains(b.String(), "\x1b[32\x1b[") || strings.Count(b.String(), "\r\n") != 0 {
		t.Fatalf("got %q", b.String())
	}
	s := screen(t, b.String(), 76, 4)
	if s[0] != "  "+strings.Repeat("x", c.PageWidth()) {
		t.Errorf("escapes counted as text:\n%s", strings.Join(s, "\n"))
	}
}

func TestColumnRewritesBrokenProgressLine(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, Size{Cols: 76, Rows: 24})
	w := c.PageWidth()
	io.WriteString(c, "first\r\n")
	io.WriteString(c, "\r"+strings.Repeat("y", w+10))
	io.WriteString(c, "\r\x1b[Kdone\r\n")
	io.WriteString(c, strings.Repeat("z", w+5)+"\r\nnext\r\n")
	s := screen(t, b.String(), 76, 8)
	want := []string{"first", "done", strings.Repeat("z", w), "zzzzz", "next"}
	for i, l := range want {
		if s[i] != "  "+l {
			t.Fatalf("row %d:\n%s", i+1, strings.Join(s, "\n"))
		}
	}
	inBlock(t, s, 3, w)
}

func TestFrameMarginIsEvenOnAllSides(t *testing.T) {
	for _, c := range []struct {
		sz   Size
		want Frame
	}{
		{Size{Cols: 76, Rows: 24}, Frame{Col: 3, Row: 2, Width: 72, Height: 22}},
		{Size{Cols: 18, Rows: 4}, Frame{Col: 1, Row: 1, Width: 18, Height: 4}},
		// No pixel size, as on the console: a cell twice as tall as wide.
		{Size{Cols: 76, Rows: 40}, Frame{Col: 5, Row: 3, Width: 68, Height: 36}},
		// Cells of 10x30 pixels: three columns for each margin row.
		{Size{Cols: 76, Rows: 40, XPixel: 760, YPixel: 1200}, Frame{Col: 7, Row: 3, Width: 64, Height: 36}},
		// A cell lower than wide, or more than four times as tall as wide,
		// is a wrong report: 2:1 again.
		{Size{Cols: 76, Rows: 40, XPixel: 760, YPixel: 200}, Frame{Col: 5, Row: 3, Width: 68, Height: 36}},
		{Size{Cols: 76, Rows: 40, XPixel: 760, YPixel: 4000}, Frame{Col: 5, Row: 3, Width: 68, Height: 36}},
		{Size{Cols: 76, Rows: 40, XPixel: -760, YPixel: 1200}, Frame{Col: 5, Row: 3, Width: 68, Height: 36}},
		// Wider than FrameWidth: cut to it and centred, full height kept.
		{Size{Cols: 80, Rows: 24}, Frame{Col: 5, Row: 2, Width: 72, Height: 22}},
		{Size{Cols: 120, Rows: 40}, Frame{Col: 25, Row: 3, Width: 72, Height: 36}},
		{Size{Cols: 117, Rows: 33}, Frame{Col: 23, Row: 3, Width: 72, Height: 29}},
	} {
		if got := FrameFor(c.sz); got != c.want {
			t.Errorf("FrameFor(%+v) = %+v, want %+v", c.sz, got, c.want)
		}
	}
}

func TestColumnHangsListItems(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, Size{Cols: 44, Rows: 16})
	io.WriteString(c, "  3. Pick Yes (the screen starts on No; press down, then Enter)\n")
	io.WriteString(c, "- a bullet that runs on past the edge of this narrow frame\n")
	s := screen(t, b.String(), 44, 16)
	inBlock(t, s, c.Col, c.PageWidth())
	if s[1] != "       press down, then Enter)" || s[3] != "    this narrow frame" {
		t.Errorf("continuation rows do not hang under the text:\n%s", strings.Join(s, "\n"))
	}
}

func TestWrapForLeavesPagesToTheColumn(t *testing.T) {
	long := strings.Repeat("word ", 30)
	c := NewColumn(io.Discard, Size{Cols: 120, Rows: 33})
	if got := WrapFor(c, long); got != long {
		t.Errorf("text for a Column was wrapped: %q", got)
	}
	if got := WrapFor(logger{c}, long); got != long {
		t.Errorf("text for a writer over a Column was wrapped: %q", got)
	}
	if got := WrapFor(io.Discard, long); got != Wrap(long) {
		t.Errorf("text for a plain terminal was not wrapped: %q", got)
	}
}

// logger stands for a writer that logs and writes on to a terminal.
type logger struct{ term io.Writer }

func (l logger) Write(p []byte) (int, error) { return l.term.Write(p) }
func (l logger) Terminal() io.Writer         { return l.term }

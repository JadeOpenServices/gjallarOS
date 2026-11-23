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
	for _, l := range m.lines() {
		n := 0
		for _, s := range l {
			n += len([]rune(s.text))
		}
		if n > Width {
			t.Fatalf("line over %d: %v", Width, l)
		}
	}
	var b bytes.Buffer
	p, err := Render(&b, m, Logo{}, 120, 40, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\x1b[28Gdev get them earlier and break more often.") {
		t.Fatalf("body not in the block:\n%q", b.String())
	}
	if p.PromptCol != 28+len("Channel: ") {
		t.Fatalf("prompt column %d", p.PromptCol)
	}
}

func TestTallPageScrollsFromTop(t *testing.T) {
	body := make([]string, 40)
	for i := range body {
		body[i] = "line"
	}
	var b bytes.Buffer
	p, err := Render(&b, Menu{Title: "Steps", Body: body, Prompt: "> "}, Logo{}, 80, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\x1b[1;1H") || p.PromptRow != 24 {
		t.Fatalf("tall page: row %d\n%q", p.PromptRow, b.String()[:60])
	}
}

func TestColumnSetsColumnAfterEachLineEnd(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, 80)
	io.WriteString(c, "one\ntwo\r50%\rdone\n")
	want := "\x1b[8Gone\n\x1b[8Gtwo\r\x1b[8G50%\r\x1b[8Gdone\n"
	if b.String() != want {
		t.Fatalf("got %q", b.String())
	}
	if c.Terminal() != io.Writer(&b) {
		t.Fatal("Terminal must return the writer under the Column")
	}
}

func TestHeadDrawsTitleAndRule(t *testing.T) {
	var b bytes.Buffer
	if _, err := Head(&b, "Installing", 80, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\x1b[8GInstalling\r\n\x1b[8G─") {
		t.Fatalf("got %q", b.String())
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
	if _, err := Render(&b, m, Logo{}, 80, 25, false); err != nil {
		t.Fatal(err)
	}
	s := screen(t, b.String(), 80, 25)
	if s[6] != strings.Repeat(" ", 7)+"Release channel" || !strings.HasPrefix(s[10], strings.Repeat(" ", 7)+"dev get") {
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
	c := NewColumn(&b, 100)
	io.WriteString(c, "Machine support: best effort. No ODDC model matches this machine; nothing optional is switched on.\n")
	io.WriteString(c, "building '/nix/store/00000000000000000000000000000000-nixos-system-example-26.05.drv'...\n")
	s := screen(t, b.String(), 100, 8)
	inBlock(t, s, c.Col, Width)
	if s[0] != strings.Repeat(" ", 17)+"Machine support: best effort. No ODDC model matches this machine;" ||
		s[1] != strings.Repeat(" ", 17)+"nothing optional is switched on." {
		t.Errorf("no break at a space:\n%s", strings.Join(s, "\n"))
	}
	if s[3] != strings.Repeat(" ", 17)+"-example-26.05.drv'..." {
		t.Errorf("long word:\n%s", strings.Join(s, "\n"))
	}
}

func TestColumnNarrowTerminal(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, 30)
	io.WriteString(c, strings.Repeat("word ", 20)+"\n")
	s := screen(t, b.String(), 30, 10)
	inBlock(t, s, 1, 30)
}

func TestColumnEscapesTakeNoRoom(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, 80)
	// A colour code split over two writes, as a pipe may deliver it.
	io.WriteString(c, "\x1b[32")
	io.WriteString(c, ";1m"+strings.Repeat("x", Width)+"\x1b[0m\n")
	io.WriteString(c, "\x1b]0;title\x07ok\n")
	if strings.Contains(b.String(), "\x1b[32\x1b[") || strings.Count(b.String(), "\r\n") != 0 {
		t.Fatalf("got %q", b.String())
	}
	s := screen(t, b.String(), 80, 4)
	if s[0] != strings.Repeat(" ", 7)+strings.Repeat("x", Width) {
		t.Errorf("escapes counted as text:\n%s", strings.Join(s, "\n"))
	}
}

func TestColumnRewritesBrokenProgressLine(t *testing.T) {
	var b bytes.Buffer
	c := NewColumn(&b, 80)
	io.WriteString(c, "first\r\n")
	io.WriteString(c, "\r"+strings.Repeat("y", Width+10))
	io.WriteString(c, "\r\x1b[Kdone\r\n")
	io.WriteString(c, strings.Repeat("z", Width+5)+"\r\nnext\r\n")
	s := screen(t, b.String(), 80, 8)
	want := []string{"first", "done", strings.Repeat("z", Width), "zzzzz", "next"}
	for i, w := range want {
		if s[i] != strings.Repeat(" ", 7)+w {
			t.Fatalf("row %d:\n%s", i+1, strings.Join(s, "\n"))
		}
	}
	inBlock(t, s, 8, Width)
}

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

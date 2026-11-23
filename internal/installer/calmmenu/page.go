package calmmenu

import (
	"fmt"
	"io"
	"strings"
)

// A page is a Menu with Body text, and maybe no Groups: every screen
// between menus is drawn with Render, so it sits in the same centred
// block with the same title, rule, numbers, hint and prompt (section 18).
// One question is one page. Text that leads up to a question is its Body,
// a list to pick from is its Groups, and the question is its Prompt.

// Head starts a page for running output, such as a step that prints as
// it works: it clears the screen and draws the title and rule at the top
// of the block. Output then goes through a Column. The next question is a
// new page drawn with Render.
func Head(w io.Writer, title string, cols int, colour bool) (*Column, error) {
	c := NewColumn(w, cols)
	var b strings.Builder
	b.WriteString(Clear)
	t, r := title, strings.Repeat("─", min(Width, cols))
	if colour {
		t, r = bold+t+reset, dim+r+reset
	}
	fmt.Fprintf(&b, "\x1b[%dG%s\r\n\x1b[%dG%s\r\n\r\n", c.Col, t, c.Col, r)
	_, err := io.WriteString(w, b.String())
	return c, err
}

// Column keeps running output in the block: each line starts at the
// block's first column and breaks at the block's width, so no line runs
// past the block to the screen edge. A line breaks at a space when the
// word after it came in the same write, else at the width. Escape
// sequences take no room, also when one is split over two writes. When a
// program rewrites a line that broke with a bare carriage return (a
// progress bar), the rows of the old line are cleared first. Characters
// count one column each.
type Column struct {
	W     io.Writer
	Col   int
	width int  // the block's width on this terminal
	mid   bool // inside a line, the column is set
	cells int  // columns used in the row
	rows  int  // rows the line broke into below its first
	cr    bool // the last byte was a carriage return
	esc   escState
	word  []byte // a word not yet written, with its escape sequences
	wide  []bool // for each byte of word: it takes a column
	wordN int    // the word's columns
}

type escState int

const (
	escNone escState = iota
	escStart
	escCSI
	escString // OSC, DCS and the like, up to BEL or ESC \
	escStringEnd
)

// NewColumn is a Column for a terminal cols wide.
func NewColumn(w io.Writer, cols int) *Column {
	width := min(Width, cols)
	return &Column{W: w, Col: max(0, (cols-width)/2) + 1, width: width}
}

func (c *Column) Write(p []byte) (int, error) {
	var b strings.Builder
	for _, ch := range p {
		c.byte(&b, ch)
	}
	c.flushWord(&b)
	if _, err := io.WriteString(c.W, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Column) byte(b *strings.Builder, ch byte) {
	if c.esc != escNone {
		c.escByte(ch)
		c.add(b, ch, 0)
		return
	}
	switch {
	case ch == '\n':
		c.flushWord(b)
		b.WriteByte(ch)
		c.mid, c.cells, c.rows, c.cr = false, 0, 0, false
	case ch == '\r':
		c.flushWord(b)
		b.WriteByte(ch)
		c.mid, c.cells, c.cr = false, 0, true
	case ch == 0x1b:
		c.esc = escStart
		c.add(b, ch, 0)
	case ch == ' ' || ch == '\t':
		c.flushWord(b)
		n := 1
		if ch == '\t' {
			n = 8 - c.cells%8
		}
		for range n {
			c.space(b)
		}
	case ch < 0x20 || ch == 0x7f:
		c.add(b, ch, 0)
	case ch >= 0x80 && ch < 0xc0: // inside a UTF-8 character
		c.add(b, ch, 0)
	default:
		c.add(b, ch, 1)
	}
}

func (c *Column) escByte(ch byte) {
	switch c.esc {
	case escStart:
		switch ch {
		case '[':
			c.esc = escCSI
		case ']', 'P', '_', '^', 'X':
			c.esc = escString
		default:
			c.esc = escNone
		}
	case escCSI:
		if ch >= 0x40 && ch <= 0x7e {
			c.esc = escNone
		}
	case escString:
		switch ch {
		case 0x07:
			c.esc = escNone
		case 0x1b:
			c.esc = escStringEnd
		}
	case escStringEnd:
		c.esc = escNone
	}
}

// add puts a byte that takes n columns into the word, or writes it at
// once when no word is open and it takes no room.
func (c *Column) add(b *strings.Builder, ch byte, n int) {
	if n == 0 && len(c.word) == 0 {
		c.start(b)
		b.WriteByte(ch)
		return
	}
	c.word = append(c.word, ch)
	c.wide = append(c.wide, n > 0)
	c.wordN += n
}

// start places the cursor when a line starts, and clears the rows of a
// broken line that is being written again.
func (c *Column) start(b *strings.Builder) {
	if c.cr && c.rows > 0 {
		fmt.Fprintf(b, "\x1b[%dA\x1b[J", c.rows)
		c.rows = 0
	}
	c.cr = false
	if !c.mid {
		fmt.Fprintf(b, "\x1b[%dG", c.Col)
		c.mid = true
	}
}

func (c *Column) space(b *strings.Builder) {
	c.start(b)
	if c.width > 0 && c.cells >= c.width {
		c.lineBreak(b)
		return // no space at the start of the next row
	}
	b.WriteByte(' ')
	c.cells++
}

func (c *Column) lineBreak(b *strings.Builder) {
	fmt.Fprintf(b, "\r\n\x1b[%dG", c.Col)
	c.cells = 0
	c.rows++
}

// flushWord writes the open word. It goes on the next row when it does
// not fit in this one but fits in a row of its own; a longer word breaks
// at the width.
func (c *Column) flushWord(b *strings.Builder) {
	if len(c.word) == 0 {
		return
	}
	c.start(b)
	if c.width > 0 && c.cells > 0 && c.cells+c.wordN > c.width && c.wordN <= c.width {
		c.lineBreak(b)
	}
	for i, ch := range c.word {
		if c.wide[i] {
			if c.width > 0 && c.cells >= c.width {
				c.lineBreak(b)
			}
			c.cells++
		}
		b.WriteByte(ch)
	}
	c.word, c.wide, c.wordN = c.word[:0], c.wide[:0], 0
}

// Terminal returns the writer under the Column. Code that must find the
// real terminal behind wrapping writers (gjallarOS prompt.terminal, which
// decides where a recovery key may be shown) has to see through a Column,
// or it would take the Column for the terminal and skip its check.
func (c *Column) Terminal() io.Writer { return c.W }

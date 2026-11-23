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
// it works, on a terminal cols by rows cells: it clears the screen, draws
// the title and rule at the top of the page's frame (see FrameFor) and
// lets the terminal scroll only inside the frame. Output then goes
// through the Column it returns. The next question is a new page drawn
// with Render.
func Head(w io.Writer, title string, cols, rows int, colour bool) (*Column, error) {
	f := FrameFor(cols, rows)
	c := NewColumn(w, cols, rows)
	var b strings.Builder
	b.WriteString(Clear)
	b.WriteString(f.scroll())
	t, r := clip(printable(title), f.Width), strings.Repeat("─", f.Width)
	if colour {
		t, r = bold+t+reset, dim+r+reset
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH%s\r\n\x1b[%dG%s\r\n\r\n", f.Row, c.Col, t, c.Col, r)
	_, err := io.WriteString(w, b.String())
	return c, err
}

// Column keeps running output in the page's frame: each line starts at
// the frame's first column and breaks at its width, so no line runs into
// the margin. A line breaks at a space when the word after it came in the
// same write, else at the width. The rows a line breaks into start under
// its text: after its leading spaces, and after a list mark such as "2."
// or "-" (see isMark). Escape sequences take no room, also when one is
// split over two writes. When a program rewrites a line that broke with a
// bare carriage return (a progress bar), the rows of the old line are
// cleared first. After a line end the cursor waits at the frame's first
// column, so a program that writes to the terminal itself, such as a
// password prompt, starts there too. Characters count one column each.
type Column struct {
	W      io.Writer
	Col    int
	width  int  // the frame's width on this terminal
	mid    bool // inside a line, the column is set
	cells  int  // columns used in the row
	rows   int  // rows the line broke into below its first
	cr     bool // the last byte was a carriage return
	esc    escState
	word   []byte // a word not yet written, with its escape sequences
	wide   []bool // for each byte of word: it takes a column
	wordN  int    // the word's columns
	head   headState
	mark   []byte // the line's first word while it may be a list mark
	indent int    // where the line's further rows start, from Col
}

// headState is how far a line has got past what sets its indent.
type headState int

const (
	headLead headState = iota // in the spaces that start the line
	headWord                  // in the line's first word
	headDone
)

type escState int

const (
	escNone escState = iota
	escStart
	escCSI
	escString // OSC, DCS and the like, up to BEL or ESC \
	escStringEnd
)

// NewColumn is a Column in the frame of a terminal cols by rows cells.
func NewColumn(w io.Writer, cols, rows int) *Column {
	return ColumnIn(w, FrameFor(cols, rows))
}

// ColumnIn is a Column in frame f, such as the frame a menu was drawn in
// (Place.Frame), for text written under the menu.
func ColumnIn(w io.Writer, f Frame) *Column {
	return &Column{W: w, Col: f.Col, width: f.Width}
}

// PageWidth is the width text wraps at, so a program that runs under the
// Column can be told how wide its lines may be.
func (c *Column) PageWidth() int { return c.width }

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
		fmt.Fprintf(b, "\n\x1b[%dG", c.Col)
		c.mid, c.cells, c.rows, c.cr = true, 0, 0, false
		c.newLine()
	case ch == '\r':
		c.flushWord(b)
		b.WriteByte(ch)
		c.mid, c.cells, c.cr = false, 0, true
		c.newLine()
	case ch == 0x1b:
		c.esc = escStart
		c.add(b, ch, 0)
	case ch == ' ' || ch == '\t':
		c.flushWord(b)
		if c.head == headWord {
			if isMark(string(c.mark)) {
				c.indent = c.cells + 1
			}
			c.head = headDone
		}
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
		if c.head == headLead {
			c.head, c.indent, c.mark = headWord, c.cells, c.mark[:0]
		}
		c.add(b, ch, 1)
	}
}

// newLine starts the indent of a new line over.
func (c *Column) newLine() { c.head, c.indent = headLead, 0 }

// hang is the indent of the line's further rows, none when it would
// leave too little room (see hangOrNone).
func (c *Column) hang() int { return hangOrNone(c.indent, c.width) }

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
	if n > 0 && c.head == headWord {
		if len(c.mark) >= 8 { // longer than any list mark
			c.head = headDone
		}
		c.mark = append(c.mark, ch)
	}
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
	h := c.hang()
	fmt.Fprintf(b, "\r\n\x1b[%dG", c.Col+h)
	c.cells = h
	c.rows++
}

// flushWord writes the open word. It goes on the next row when it does
// not fit in this one but fits in a further row of its own; a longer word
// breaks at the width.
func (c *Column) flushWord(b *strings.Builder) {
	if len(c.word) == 0 {
		return
	}
	c.start(b)
	if h := c.hang(); c.width > 0 && c.cells > h && c.cells+c.wordN > c.width && c.wordN <= c.width-h {
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

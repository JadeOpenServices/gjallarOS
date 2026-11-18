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
// block's first column. Our own text is wrapped with Wrap before it gets
// here. Output of other programs is passed on as it is, and a line longer
// than the space to the right of the block wraps where the terminal puts it.
type Column struct {
	W   io.Writer
	Col int
	mid bool // inside a line, the column is set
}

// NewColumn is a Column for a terminal cols wide.
func NewColumn(w io.Writer, cols int) *Column {
	return &Column{W: w, Col: max(0, (cols-min(Width, cols))/2) + 1}
}

func (c *Column) Write(p []byte) (int, error) {
	var b strings.Builder
	for _, ch := range p {
		switch {
		case ch == '\n' || ch == '\r':
			c.mid = false
		case !c.mid:
			fmt.Fprintf(&b, "\x1b[%dG", c.Col)
			c.mid = true
		}
		b.WriteByte(ch)
	}
	if _, err := io.WriteString(c.W, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Terminal returns the writer under the Column. Code that must find the
// real terminal behind wrapping writers (gjallarOS prompt.terminal, which
// decides where a recovery key may be shown) has to see through a Column,
// or it would take the Column for the terminal and skip its check.
func (c *Column) Terminal() io.Writer { return c.W }

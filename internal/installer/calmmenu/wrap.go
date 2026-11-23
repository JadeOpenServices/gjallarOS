package calmmenu

import (
	"io"
	"strings"
	"unicode/utf8"
)

// Wrap breaks plain output at Width columns on spaces, for the text
// between menus on a terminal that shows no page (THEMEBRAIN section 18).
// Lines that already fit stay as they are. A continuation line hangs
// under the text of its line (see hang). Words are never split or
// changed; a single word wider than the block stays whole.
func Wrap(text string) string { return wrapAt(text, Width) }

// WrapFor is text as it should be written to w: as it is when w wraps
// text itself (see PageWidth), such as a page column that wraps at the
// page's width, else wrapped at Width.
func WrapFor(w io.Writer, text string) string {
	if PageWidth(w) > 0 {
		return text
	}
	return Wrap(text)
}

// PageWidth is the width of the page column w writes into, or 0 when it
// writes into none. w is in one when it is a Column or writes into one
// (it has a PageWidth method), maybe behind writers that log and name
// the writer under them with Terminal.
func PageWidth(w io.Writer) int {
	for {
		if p, ok := w.(interface{ PageWidth() int }); ok {
			return p.PageWidth()
		}
		t, ok := w.(interface{ Terminal() io.Writer })
		if !ok {
			return 0
		}
		w = t.Terminal()
	}
}

func wrapAt(text string, width int) string {
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, wrapLine(line, width)...)
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string, width int) []string {
	if utf8.RuneCountInString(line) <= width {
		return []string{line}
	}
	lead := len(line) - len(strings.TrimLeft(line, " "))
	indent := strings.Repeat(" ", hang(line, width))
	words := strings.Fields(line)
	var out []string
	cur := line[:lead] + words[0]
	for _, w := range words[1:] {
		if utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) > width {
			out = append(out, cur)
			cur = indent + w
			continue
		}
		cur += " " + w
	}
	return append(out, cur)
}

// hang is how far the continuation rows of line are indented when it
// breaks at width: under the text after its leading spaces, and after a
// list mark (see isMark) when one starts the text.
func hang(line string, width int) int {
	text := strings.TrimLeft(line, " ")
	n := len(line) - len(text)
	if mark, _, ok := strings.Cut(text, " "); ok && isMark(mark) {
		n += len(mark) + 1
	}
	return hangOrNone(n, width)
}

// hangOrNone drops an indent of half the width or more, so a row always
// keeps room for words.
func hangOrNone(n, width int) int {
	if n*2 >= width {
		return 0
	}
	return n
}

// isMark reports whether word marks a list item: a step "12." or a
// choice "2)" or a bullet "-". The text after it lines up on every row.
func isMark(word string) bool {
	if word == "-" {
		return true
	}
	i := 0
	for i < len(word) && word[i] >= '0' && word[i] <= '9' {
		i++
	}
	return i > 0 && i == len(word)-1 && (word[i] == '.' || word[i] == ')')
}

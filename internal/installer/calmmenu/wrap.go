package calmmenu

import (
	"strings"
	"unicode/utf8"
)

// Wrap breaks plain output at Width columns on spaces, for the text
// between menus (THEMEBRAIN section 18). Lines that already fit stay as
// they are. A continuation line keeps the leading spaces of its line, and
// a step such as "  3. " indents its continuation to the text after the
// number. Words are never split or changed; a single word wider than the
// block stays whole.
func Wrap(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, wrapLine(line)...)
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string) []string {
	if utf8.RuneCountInString(line) <= Width {
		return []string{line}
	}
	lead := len(line) - len(strings.TrimLeft(line, " "))
	indent := strings.Repeat(" ", lead+stepMark(line[lead:]))
	words := strings.Fields(line)
	var out []string
	cur := line[:lead] + words[0]
	for _, w := range words[1:] {
		if utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) > Width {
			out = append(out, cur)
			cur = indent + w
			continue
		}
		cur += " " + w
	}
	return append(out, cur)
}

// stepMark reports the width of a "12. " step number at the start of s,
// or 0.
func stepMark(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(s) && s[i] == '.' && s[i+1] == ' ' {
		return i + 2
	}
	return 0
}

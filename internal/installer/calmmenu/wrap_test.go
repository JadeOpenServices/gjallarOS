package calmmenu

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWrap(t *testing.T) {
	in := "This gjallarOS is not encrypted. Anyone who had this computer for a few minutes could have\n" +
		"  12. Yes (the screen starts on No; press down, then Enter) and a bit more text to wrap here\n" +
		"short line\n" +
		"     AB:CD:EF fingerprint stays"
	got := Wrap(in)
	for _, l := range strings.Split(got, "\n") {
		if utf8.RuneCountInString(l) > Width {
			t.Errorf("too wide: %q", l)
		}
	}
	if strings.Join(strings.Fields(got), " ") != strings.Join(strings.Fields(in), " ") {
		t.Errorf("words changed:\n%s", got)
	}
	t.Log("\n" + got)
	if !strings.Contains(got, "\n      ") {
		t.Errorf("step continuation not indented:\n%s", got)
	}
}

package secureboot

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
)

// TestScreensWrapKeepFingerprints checks the MokManager walks: no line is
// wider than the calm menu, and every fingerprint and hash line comes out
// byte for byte, so it still matches what MokManager shows.
func TestScreensWrapKeepFingerprints(t *testing.T) {
	f, _ := missedVisit(t)
	kek := microsoftKEK2011(t)
	lists, err := signatureLists(slices.Concat(x509SignatureList(kek), x509SignatureList(f.other)))
	if err != nil || len(lists) != 2 {
		t.Fatalf("%v %d", err, len(lists))
	}
	sums := []string{strings.Repeat("ab", 32), strings.Repeat("cd", 32)}
	lists = append(lists,
		SignatureList{Present: true, Hashes: 1, FileSums: sums[:1]},
		SignatureList{Present: true, Hashes: 2, FileSums: sums})
	// The walks are written through calmmenu.WrapFor; a terminal without
	// pages gets them wrapped as here.
	screens := map[string]string{
		"steps":          calmmenu.Wrap(mokVisit{key: f.current, del: lists}.steps()),
		"mokManager":     calmmenu.Wrap(mokManagerSteps(f.current, true, "gjallarctl secureboot continue")),
		"hashDelete":     calmmenu.Wrap(hashDeleteNotice(&hashDelete{sums: sums})),
		"requeueSummary": calmmenu.Wrap("The installed gjallarOS has the key \"EXAMPLE a rather long key name for this check\", fingerprint\n" + sha1Fingerprint(f.current, "  ") + "\n"),
	}
	exact := [][]string{
		strings.Split(sha1Fingerprint(f.current, stepIndent), "\n"),
		strings.Split(sha1Fingerprint(kek, stepIndent), "\n"),
		strings.Split(sha1Fingerprint(f.current, "  "), "\n"),
		{stepIndent + spacedHex(sums[0][:16]), stepIndent + spacedHex(sums[1][:16])},
		{"  " + spacedHex(sums[0][:16]), "  " + spacedHex(sums[1][:16])},
	}
	for name, text := range screens {
		lines := strings.Split(text, "\n")
		for _, l := range lines {
			if n := utf8.RuneCountInString(l); n > 66 {
				t.Errorf("%s: line of %d runes: %q", name, n, l)
			}
		}
		found := 0
		for _, group := range exact {
			for _, want := range group {
				if slices.Contains(lines, want) {
					found++
				}
			}
		}
		if found == 0 {
			t.Errorf("%s holds no fingerprint or hash line byte for byte:\n%s", name, text)
		}
	}
	for _, group := range exact {
		for _, l := range group {
			if calmmenu.Wrap(l) != l {
				t.Errorf("Wrap changed %q", l)
			}
		}
	}
	if line := "  SHA256 " + spacedHex(sums[0][:16]); calmmenu.Wrap(line) != line {
		t.Errorf("Wrap changed %q", line)
	}
	for _, want := range []string{strings.Join(exact[0], "\n"), strings.Join(exact[1], "\n"), exact[3][0], exact[3][1]} {
		if !strings.Contains(screens["steps"], want) {
			t.Errorf("steps lack %q:\n%s", want, screens["steps"])
		}
	}
	if !strings.Contains(screens["mokManager"], strings.Join(exact[0], "\n")) || !strings.Contains(screens["hashDelete"], strings.Join(exact[4], "\n")) || !strings.Contains(screens["requeueSummary"], strings.Join(exact[2], "\n")) {
		t.Errorf("a fingerprint or hash block changed:\n%s\n%s\n%s", screens["mokManager"], screens["hashDelete"], screens["requeueSummary"])
	}
}

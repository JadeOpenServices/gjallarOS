package safefs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// longNames fills dir with count empty files of 200-byte names, plus the
// extra names given, and returns what the listing costs.
func longNames(t *testing.T, dir string, count int, extra ...string) int64 {
	t.Helper()
	var cost int64
	for i := range count {
		n := fmt.Sprintf("%03d", i) + strings.Repeat("n", 197)
		write(t, filepath.Join(dir, n), "")
		cost += nameCost(n)
	}
	for _, n := range extra {
		cost += nameCost(n)
	}
	return cost
}

func setNameCap(t *testing.T, n int64) {
	t.Helper()
	old := MaxNameBytes
	MaxNameBytes = n
	t.Cleanup(func() { MaxNameBytes = old })
}

// walks runs Measure, Copy and, for the top folder, Names on src under the
// current cap and returns their errors.
func walks(t *testing.T, src string) (measure, copy, names error) {
	t.Helper()
	_, measure = measureTree(t, src, MeasureOptions{MaxEntries: 1 << 20})
	_, copy = copyTree(t, src, t.TempDir(), 1<<30, nil)
	_, names = mustDir(t, src).Names(".", -1)
	return measure, copy, names
}

// L130: one folder of long names over the byte cap is refused by every
// walk, though its entry count is small; at the cap it passes.
func TestNameBytesOneFolder(t *testing.T) {
	src := t.TempDir()
	cost := longNames(t, src, 20)

	setNameCap(t, cost-1)
	m, c, n := walks(t, src)
	for name, err := range map[string]error{"measure": m, "copy": c, "names": n} {
		if !errors.Is(err, ErrTooMany) {
			t.Errorf("%s over the cap: %v", name, err)
		}
	}

	setNameCap(t, cost)
	if m, c, n := walks(t, src); m != nil || c != nil || n != nil {
		t.Fatalf("at the cap: %v, %v, %v", m, c, n)
	}
}

// L130: the lists of the folders above stay held while the walk is below
// them. A chain of folders that each fit alone is refused once their sum
// is over the cap; two sibling folders that each fit are not, since the
// first list is let go before the second is read.
func TestNameBytesHeldByAncestors(t *testing.T) {
	chain := t.TempDir()
	top := longNames(t, chain, 10, "z")
	mid := longNames(t, filepath.Join(chain, "z"), 10, "z")
	low := longNames(t, filepath.Join(chain, "z/z"), 10)

	setNameCap(t, top+mid+low-1)
	if m, c, _ := walks(t, chain); !errors.Is(m, ErrTooMany) || !errors.Is(c, ErrTooMany) {
		t.Fatalf("chain over the cap: %v, %v", m, c)
	}
	setNameCap(t, top+mid+low)
	if m, c, _ := walks(t, chain); m != nil || c != nil {
		t.Fatalf("chain at the cap: %v, %v", m, c)
	}

	side := t.TempDir()
	root := longNames(t, side, 0, "a", "b")
	a := longNames(t, filepath.Join(side, "a"), 10)
	b := longNames(t, filepath.Join(side, "b"), 10)
	setNameCap(t, root+max(a, b))
	if m, c, _ := walks(t, side); m != nil || c != nil {
		t.Fatalf("siblings: %v, %v", m, c)
	}
}

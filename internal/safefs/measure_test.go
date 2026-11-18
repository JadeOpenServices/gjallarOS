package safefs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func measureTree(t *testing.T, src string, opt MeasureOptions) (Size, error) {
	t.Helper()
	own(t, src)
	if opt.SourceUID == 0 && !opt.AnyOwner {
		opt.SourceUID = fixtureUID()
	}
	if opt.MaxEntries == 0 {
		opt.MaxEntries = 1000
	}
	var size Size
	var err error
	withTimeout(t, func() { size, err = Measure(mustDir(t, src), opt) })
	return size, err
}

// A planted link to a big file elsewhere, a FIFO and a sparse file: the
// link is counted as a link, the FIFO is never opened, and what Measure
// says is never less than what Copy then uses.
func TestMeasureCountsLikeCopy(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "docs/a"), string(bytes.Repeat([]byte("a"), 5000)))
	write(t, filepath.Join(src, "docs/deeper/b"), "b")
	big := filepath.Join(t.TempDir(), "big")
	write(t, big, string(bytes.Repeat([]byte("x"), 1<<20)))
	if err := os.Symlink(big, filepath.Join(src, "docs/to-big")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(big), filepath.Join(src, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	sparse, err := os.Create(filepath.Join(src, "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sparse.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	sparse.Close()

	size, err := measureTree(t, src, MeasureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := Size{Files: 3, Folders: 2, Links: 2, Skipped: 1, Bytes: 5000 + 1 + 64<<20}
	want.Used = want.Bytes + 7*EntryCost
	if size != want {
		t.Fatalf("size %+v, want %+v", size, want)
	}
	stats, err := copyTree(t, src, dst, size.Used, nil)
	if err != nil {
		t.Fatalf("a copy within the measured size failed: %v", err)
	}
	if stats.Used > size.Used {
		t.Fatalf("copy used %d, measured %d", stats.Used, size.Used)
	}
}

func TestMeasureLeavesOutSkip(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "Nextcloud/big"), string(bytes.Repeat([]byte("n"), 1<<20)))
	write(t, filepath.Join(src, "work/Cloud/c"), "cc")
	write(t, filepath.Join(src, "work/keep"), "k")
	size, err := measureTree(t, src, MeasureOptions{Skip: map[string]bool{"Nextcloud": true, "work/Cloud": true}})
	if err != nil {
		t.Fatal(err)
	}
	if size.Left != 2 || size.Files != 1 || size.Bytes != 1 || size.Folders != 1 {
		t.Fatalf("size %+v", size)
	}
}

// Copy leaves out the same paths as Measure, and a copy within the
// measured size fits: what stays behind is not copied and not charged.
func TestCopyLeavesOutSkip(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "Nextcloud/big"), string(bytes.Repeat([]byte("n"), 1<<20)))
	write(t, filepath.Join(src, "work/Cloud/c"), "cc")
	write(t, filepath.Join(src, "work/keep"), "k")
	write(t, filepath.Join(src, "work/Cloudy"), "y")
	skip := map[string]bool{"Nextcloud": true, "work/Cloud": true}
	size, err := measureTree(t, src, MeasureOptions{Skip: skip})
	if err != nil {
		t.Fatal(err)
	}
	var stats Stats
	withTimeout(t, func() {
		stats, err = Copy(mustDir(t, src), mustDir(t, dst), CopyOptions{
			SourceUID: fixtureUID(), UID: fixtureUID(), GID: fixtureGID(), Budget: size.Used, Skip: skip,
		})
	})
	if err != nil {
		t.Fatalf("a copy within the measured size failed: %v", err)
	}
	if stats.Left != 2 || stats.Files != 2 || stats.Used != size.Used {
		t.Fatalf("stats %+v, size %+v", stats, size)
	}
	for _, p := range []string{"Nextcloud", "work/Cloud"} {
		if _, err := os.Lstat(filepath.Join(dst, p)); !os.IsNotExist(err) {
			t.Errorf("%s was copied: %v", p, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dst, "work/Cloudy")); err != nil || string(got) != "y" {
		t.Errorf("a name that only starts like a skipped one stayed behind: %q %v", got, err)
	}
}

// A tree of many empty files stops at the cap instead of keeping root busy.
func TestMeasureEntryCap(t *testing.T) {
	src := t.TempDir()
	for i := range 50 {
		write(t, filepath.Join(src, "e", fmt.Sprintf("f%02d", i)), "")
	}
	if _, err := measureTree(t, src, MeasureOptions{MaxEntries: 40}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("got %v", err)
	}
	if _, err := measureTree(t, src, MeasureOptions{MaxEntries: 51}); err != nil {
		t.Fatalf("51 entries fit the cap of 51: %v", err)
	}
	// One folder over its own cap, though the whole tree fits.
	if _, err := measureTree(t, src, MeasureOptions{MaxEntries: 1000, MaxFolder: 49}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("folder cap: got %v", err)
	}
	if _, err := measureTree(t, src, MeasureOptions{MaxEntries: 1000, MaxFolder: 50}); err != nil {
		t.Fatalf("50 entries fit the folder cap of 50: %v", err)
	}
}

func TestMeasureDepthCap(t *testing.T) {
	src := t.TempDir()
	deep := src
	for range MaxDepth + 2 {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := measureTree(t, src, MeasureOptions{}); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("got %v", err)
	}
}

func TestMeasureRefuses(t *testing.T) {
	dir := mustDir(t, t.TempDir())
	for name, opt := range map[string]MeasureOptions{
		"no cap":      {SourceUID: os.Getuid(), MaxEntries: 0},
		"no owner":    {MaxEntries: 10},
		"other owner": {SourceUID: os.Getuid() + 1, MaxEntries: 10},
	} {
		if _, err := Measure(dir, opt); err == nil {
			t.Errorf("%s: measured", name)
		}
	}
	if _, err := Measure(dir, MeasureOptions{AnyOwner: true, MaxEntries: 10}); err != nil {
		t.Errorf("any owner: %v", err)
	}
}

// Entries of other owners are not counted and not entered, unless the
// caller counts every owner.
func TestMeasureForeign(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("giving a file to another user needs root")
	}
	src := t.TempDir()
	write(t, filepath.Join(src, "mine"), "m")
	write(t, filepath.Join(src, "root-only/secret"), "ss")
	for _, p := range []string{src, filepath.Join(src, "mine")} {
		if err := os.Lchown(p, 1000, 100); err != nil {
			t.Fatal(err)
		}
	}
	size, err := Measure(mustDir(t, src), MeasureOptions{SourceUID: 1000, MaxEntries: 10})
	if err != nil || size.Foreign != 1 || size.Files != 1 || size.Bytes != 1 {
		t.Fatalf("size %+v, %v", size, err)
	}
	size, err = Measure(mustDir(t, src), MeasureOptions{AnyOwner: true, MaxEntries: 10})
	if err != nil || size.Foreign != 0 || size.Files != 2 || size.Bytes != 3 {
		t.Fatalf("any owner: size %+v, %v", size, err)
	}
}

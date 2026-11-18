package freespace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/freshdiskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

const gib = 1 << 30

func TestPlanFits(t *testing.T) {
	in := Input{
		DiskBytes:   2000398934016,
		Recovery:    true,
		BaseClosure: 30 * gib,
		StoreImport: 40 * gib,
		FileCache:   40 * gib,
		Categories: []Category{
			{Name: Home, Bytes: 300 * gib, Chosen: true},
			{Name: Nextcloud, Bytes: 500 * gib, Chosen: true},
			{Name: Branches, Bytes: 1 * gib, Chosen: false},
		},
	}
	r := Plan(in)
	root := freshdiskplan.RootBytes(in.DiskBytes, true)
	if r.Root != root || r.Swap != freshdiskplan.InstallSwap(root) || r.Reserve != root/20 {
		t.Fatalf("result %+v", r)
	}
	if r.Available != root-r.Swap-30*gib || r.Needed != 880*gib+r.Reserve || !r.Fits || r.Short != 0 {
		t.Fatalf("result %+v", r)
	}
	if len(r.Biggest) != 2 || r.Biggest[0].Name != Nextcloud || r.Biggest[1].Name != Home {
		t.Fatalf("biggest %+v", r.Biggest)
	}
}

func TestPlanShort(t *testing.T) {
	r := Plan(Input{
		DiskBytes:   64 * gib,
		BaseClosure: 30 * gib,
		Categories: []Category{
			{Name: "a", Bytes: 1 * gib, Chosen: true},
			{Name: "b", Bytes: 9 * gib, Chosen: true},
			{Name: "c", Bytes: 5 * gib, Chosen: true},
			{Name: "d", Bytes: 7 * gib, Chosen: true},
			{Name: "e", Bytes: 99 * gib},
		},
	})
	// Small root: the reserve is the 10 GiB floor.
	if r.Reserve != MinReserve || r.Fits || r.Short != r.Needed-r.Available || r.Needed != 22*gib+MinReserve {
		t.Fatalf("result %+v", r)
	}
	var names []string
	for _, c := range r.Biggest {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "b,d,c" {
		t.Fatalf("biggest %v", names)
	}
}

// A disk too small for even the base system, and sizes that would wrap:
// nothing underflows into a huge free space or wraps into a small need.
func TestPlanNoWrap(t *testing.T) {
	r := Plan(Input{DiskBytes: 1 * gib, BaseClosure: 30 * gib})
	if r.Available != 0 || r.Fits {
		t.Fatalf("tiny disk %+v", r)
	}
	r = Plan(Input{
		DiskBytes: 2000 * gib,
		Categories: []Category{
			{Name: "x", Bytes: ^uint64(0) - 5, Chosen: true},
			{Name: "y", Bytes: 10, Chosen: true},
		},
	})
	if r.Needed != ^uint64(0) || r.Fits {
		t.Fatalf("wrap %+v", r)
	}
}

func TestNextcloudWarn(t *testing.T) {
	for _, c := range []struct {
		nc, free uint64
		want     bool
	}{
		{50 * gib, 1000 * gib, false},
		{50*gib + 1, 1000 * gib, true},
		{10 * gib, 40 * gib, false},
		{10*gib + 1, 40 * gib, true},
	} {
		if got := NextcloudWarn(c.nc, c.free); got != c.want {
			t.Errorf("%d of %d: %v", c.nc, c.free, got)
		}
	}
}

// oldDisk lays out a made-up old top level for the account "example".
func oldDisk(t *testing.T, cfg string) string {
	t.Helper()
	if os.Geteuid() != 0 && (os.Getuid() < minUserID || os.Getuid() > maxUserID) {
		t.Skip("the fixture home needs an account id between 1000 and 59999")
	}
	top := t.TempDir()
	home := filepath.Join(top, homeDir, "example")
	put(t, filepath.Join(home, "docs/a"), strings.Repeat("a", 100))
	put(t, filepath.Join(home, "Work/notes"), "notes")
	put(t, filepath.Join(home, "Work/Cloud/c"), "cc")
	put(t, filepath.Join(home, "Nextcloud/big"), strings.Repeat("n", 1000))
	put(t, filepath.Join(home, "Nextcloud/sub/c"), strings.Repeat("s", 10))
	if cfg != "" {
		put(t, filepath.Join(home, nextcloudCfg), cfg)
	}
	put(t, filepath.Join(top, checkoutGit, "HEAD"), strings.Repeat("h", 20))
	if err := os.MkdirAll(filepath.Join(top, checkoutGit, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(home, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(home, "Linked")); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		// Root runs the tests: the home goes to a normal account, as on a
		// real disk; the checkout stays root's.
		err := filepath.Walk(home, func(p string, _ os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(p, 1000, 100)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return top
}

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func measure(t *testing.T, top, username string) (Sizes, error) {
	t.Helper()
	d, err := safefs.OpenDir(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	done := make(chan struct{})
	var s Sizes
	go func() { s, err = Measure(d, username); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Measure hangs")
	}
	return s, err
}

var cfgAll = strings.Join([]string{
	`[General]`,
	`0\Folders\1\localPath=/home/example/docs/`,
	`[Accounts]`,
	`0\Folders\1\localPath=/home/example/Nextcloud/`,
	`0\FoldersWithPlaceholders\1\localPath=/home/example/Nextcloud/`,
	`0\Folders\2\localPath=/home/example/Nextcloud/sub`,
	`0\Multifolders\1\localPath=/home/example/Work/Cloud/`,
	`0\Folders\4\localPath=/home/example/../other`,
	`0\Folders\5\localPath=relative/path`,
	`0\Folders\6\localPath="/home/example/Work"`,
	`0\Folders\7\localPath=/home/other/Cloud`,
	`0\Folders\8\localPath=/home/example/Gone`,
	`0\Folders\9\localPath=/home/example/Linked`,
	`0\Folders\10\localPath=/home/example`,
	`0\Folders\11\localPath=/home/example/Work\\Cloud`,
	`0\Folders\1\journalPath=/home/example/docs`,
	`[Proxy]`,
	`0\Folders\12\localPath=/home/example/docs`,
}, "\n") + "\n"

// Every sync folder kind, a nested one, a planted link, paths that leave
// the home or are written oddly: each sync folder is measured once, every
// odd line stays inside the home size, and nothing is counted twice.
func TestMeasureCategories(t *testing.T) {
	top := oldDisk(t, cfgAll)
	s, err := measure(t, top, "example")
	if err != nil {
		t.Fatal(err)
	}
	const e = safefs.EntryCost
	// Folders 1 and 3; the nested sub is inside folder 1.
	nc := uint64(1000+10+3*e+e) + uint64(2+e+e)
	// docs, docs/a, Work, Work/notes, .config, .config/Nextcloud, the cfg,
	// Linked (a link, counted, not entered).
	home := uint64(100+5+len(cfgAll)) + 8*e
	want := Sizes{
		Home: home, Nextcloud: nc, Branches: 20 + 2*e,
		SyncFolders: 2, Skipped: 1,
		// sub (nested), ../other, relative, quoted, /home/other, Linked,
		// the whole home, the escaped one.
		Odd: 8,
	}
	if s != want {
		t.Fatalf("sizes %+v, want %+v", s, want)
	}
}

func TestMeasureNoCfg(t *testing.T) {
	s, err := measure(t, oldDisk(t, ""), "example")
	if err != nil {
		t.Fatal(err)
	}
	if s.Nextcloud != 0 || s.SyncFolders != 0 || s.Odd != 0 || s.Home == 0 || s.Branches == 0 {
		t.Fatalf("sizes %+v", s)
	}
}

// nextcloud.cfg replaced by a FIFO or a link to a file elsewhere: never
// opened, counted as odd, the home still measured.
func TestMeasurePlantedCfg(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "cfg")
	put(t, outside, cfgAll)
	for name, plant := range map[string]func(string) error{
		"fifo": func(p string) error { return unix.Mkfifo(p, 0o600) },
		"link": func(p string) error { return os.Symlink(outside, p) },
	} {
		top := oldDisk(t, "")
		cfg := filepath.Join(top, homeDir, "example", nextcloudCfg)
		if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := plant(cfg); err != nil {
			t.Fatal(err)
		}
		s, err := measure(t, top, "example")
		if err != nil || s.Odd != 1 || s.SyncFolders != 0 || s.Home == 0 {
			t.Errorf("%s: sizes %+v, %v", name, s, err)
		}
	}
}

// A planted link in place of the home or the checkout history is refused,
// not followed.
func TestMeasurePlantedLinks(t *testing.T) {
	victim := t.TempDir()
	put(t, filepath.Join(victim, "secret"), "s")

	top := oldDisk(t, "")
	home := filepath.Join(top, homeDir, "example")
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, home); err != nil {
		t.Fatal(err)
	}
	if _, err := measure(t, top, "example"); !errors.Is(err, ErrNoHome) {
		t.Fatalf("home link: %v", err)
	}

	top = oldDisk(t, "")
	git := filepath.Join(top, checkoutGit)
	if err := os.RemoveAll(git); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, git); err != nil {
		t.Fatal(err)
	}
	if _, err := measure(t, top, "example"); !errors.Is(err, safefs.ErrNotPlain) {
		t.Fatalf("checkout link: %v", err)
	}
}

func TestMeasureRefuses(t *testing.T) {
	top := oldDisk(t, "")
	for _, name := range []string{"", "../example", "Example", "nobody-here"} {
		if _, err := measure(t, top, name); err == nil {
			t.Errorf("%q: measured", name)
		}
	}
}

func TestMeasureNoCheckout(t *testing.T) {
	top := oldDisk(t, "")
	if err := os.RemoveAll(filepath.Join(top, "@config")); err != nil {
		t.Fatal(err)
	}
	s, err := measure(t, top, "example")
	if err != nil || s.Branches != 0 {
		t.Fatalf("sizes %+v, %v", s, err)
	}
}

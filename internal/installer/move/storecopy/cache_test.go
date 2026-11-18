package storecopy

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// The shapes nix 2.34 writes for `nix copy --to file://...?compression=none`
// (checked against a real export). The hashes are fixed strings of nix's
// base32 letters; Settle does not hash, nix does on import.
const (
	pathHash = "0123456789abcdfghijklmnpqrsvwxyz"
	refHash  = "zyxwvsrqpnmlkjihgfdcba9876543210"
	narFile  = "1b8m8a7n1y5d6n7z3k0q5w2h9f4c6v3x0j8l2g7p4r1s9i5d0a2b.nar"
	sig      = "cache.nixos.org-1:" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/AbCdEfGhIj=="
)

var narHashLine = "sha256:" + strings.TrimSuffix(narFile, ".nar")

func narinfoText(over map[string]string, size int) string {
	fields := [][2]string{
		{"StorePath", "/nix/store/" + pathHash + "-hello-2.12.1"},
		{"URL", "nar/" + narFile},
		{"Compression", "none"},
		{"FileHash", narHashLine},
		{"FileSize", strconv.Itoa(size)},
		{"NarHash", narHashLine},
		{"NarSize", strconv.Itoa(size)},
		{"References", pathHash + "-hello-2.12.1 " + refHash + "-glibc-2.40-66"},
		{"Deriver", refHash + "-hello-2.12.1.drv"},
		{"Sig", sig},
	}
	var b strings.Builder
	for _, f := range fields {
		v, ok := over[f[0]]
		if !ok {
			v = f[1]
		}
		if v == "-" {
			continue
		}
		b.WriteString(f[0] + ": " + v + "\n")
	}
	for k, v := range over {
		if !strings.Contains("StorePath URL Compression FileHash FileSize NarHash NarSize References Deriver Sig", k) {
			b.WriteString(k + ": " + v + "\n")
		}
	}
	return b.String()
}

func put(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// export makes a cache as one exported path leaves it.
func export(t *testing.T, over map[string]string) string {
	t.Helper()
	c := t.TempDir()
	nar := strings.Repeat("n", 100)
	put(t, filepath.Join(c, cacheInfo), cacheInfoText)
	put(t, filepath.Join(c, pathHash+".narinfo"), narinfoText(over, len(nar)))
	put(t, filepath.Join(c, narDir, narFile), nar)
	for _, d := range []string{realisationsDir, logDir} {
		if err := os.Mkdir(filepath.Join(c, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(c, 0o777); err != nil {
		t.Fatal(err)
	}
	return c
}

func settle(t *testing.T, c string, budget int64) (Stats, error) {
	t.Helper()
	d, err := safefs.OpenDir(c)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	return Settle(d, os.Getuid(), os.Getgid(), budget)
}

func TestSettleExport(t *testing.T) {
	c := export(t, nil)
	if err := os.Chmod(filepath.Join(c, narDir, narFile), 0o666); err != nil {
		t.Fatal(err)
	}
	s, err := settle(t, c, 1<<20)
	if err != nil || s != (Stats{Paths: 1, Bytes: 100}) {
		t.Fatalf("%+v, %v", s, err)
	}
	for p, mode := range map[string]os.FileMode{
		".": 0o755, narDir: 0o755, logDir: 0o755,
		cacheInfo: 0o644, pathHash + ".narinfo": 0o644, narDir + "/" + narFile: 0o644,
	} {
		fi, err := os.Lstat(filepath.Join(c, p))
		if err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v", p, fi.Mode(), err)
		}
	}
	// A path with content addressing and no signature passes the check;
	// nix itself decides on it at import.
	c = export(t, map[string]string{"Sig": "-", "CA": "fixed:r:sha256:" + strings.TrimSuffix(narFile, ".nar"), "Deriver": "-"})
	if _, err := settle(t, c, 1<<20); err != nil {
		t.Errorf("CA path: %v", err)
	}
	// Nothing exported: no nar folder.
	c = t.TempDir()
	put(t, filepath.Join(c, cacheInfo), cacheInfoText)
	if s, err := settle(t, c, 1<<20); err != nil || s.Paths != 0 {
		t.Errorf("empty: %+v, %v", s, err)
	}
}

// L62: what a changed old disk or a broken export could leave for root's
// nix to read is refused, and nothing outside the cache is touched.
func TestSettleRefuses(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	put(t, outside, strings.Repeat("s", 100))
	if err := os.Chmod(outside, 0o600); err != nil {
		t.Fatal(err)
	}
	nar := func(c string) string { return filepath.Join(c, narDir, narFile) }
	cases := map[string]struct {
		over  map[string]string
		plant func(t *testing.T, c string)
		want  error
	}{
		"link NAR": {plant: func(t *testing.T, c string) {
			os.Remove(nar(c))
			if err := os.Symlink(outside, nar(c)); err != nil {
				t.Fatal(err)
			}
		}, want: safefs.ErrNotPlain},
		"FIFO NAR": {plant: func(t *testing.T, c string) {
			os.Remove(nar(c))
			if err := unix.Mkfifo(nar(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: safefs.ErrNotPlain},
		"link nar folder": {plant: func(t *testing.T, c string) {
			os.RemoveAll(filepath.Join(c, narDir))
			if err := os.Symlink(filepath.Dir(outside), filepath.Join(c, narDir)); err != nil {
				t.Fatal(err)
			}
		}, want: safefs.ErrNotPlain},
		"link cache info": {plant: func(t *testing.T, c string) {
			os.Remove(filepath.Join(c, cacheInfo))
			if err := os.Symlink(outside, filepath.Join(c, cacheInfo)); err != nil {
				t.Fatal(err)
			}
		}, want: safefs.ErrNotPlain},
		"hard link NAR": {plant: func(t *testing.T, c string) {
			if err := os.Link(nar(c), filepath.Join(c, logDir, "x")); err != nil {
				t.Fatal(err)
			}
		}, want: ErrCache},
		"URL climbs out":      {over: map[string]string{"URL": "nar/../../" + filepath.Base(outside)}, want: ErrCache},
		"URL other name":      {over: map[string]string{"URL": "nar/" + strings.Repeat("a", 52) + ".nar"}, want: ErrCache},
		"compressed":          {over: map[string]string{"Compression": "xz"}, want: ErrCache},
		"size differs":        {over: map[string]string{"FileSize": "99", "NarSize": "99"}, want: ErrCache},
		"file and nar differ": {over: map[string]string{"FileSize": "101"}, want: ErrCache},
		"other store path":    {over: map[string]string{"StorePath": "/nix/store/" + refHash + "-hello-2.12.1"}, want: ErrCache},
		"other store dir":     {over: map[string]string{"StorePath": "/tmp/store/" + pathHash + "-hello-2.12.1"}, want: ErrCache},
		"unknown key":         {over: map[string]string{"Trusted": "1"}, want: ErrCache},
		"key twice":           {over: map[string]string{"NarSize": "100\nNarSize: 100"}, want: ErrCache},
		"no NarHash":          {over: map[string]string{"NarHash": "-"}, want: ErrCache},
		"reference with path": {over: map[string]string{"References": "/etc/shadow"}, want: ErrCache},
		"extra top entry": {plant: func(t *testing.T, c string) {
			put(t, filepath.Join(c, "x.ls"), "")
		}, want: ErrCache},
		"extra NAR": {plant: func(t *testing.T, c string) {
			put(t, filepath.Join(c, narDir, strings.Repeat("a", 52)+".nar"), "")
		}, want: ErrCache},
		"NAR missing": {plant: func(t *testing.T, c string) {
			os.Remove(nar(c))
		}, want: ErrCache},
		"NAR used twice": {plant: func(t *testing.T, c string) {
			data, _ := os.ReadFile(filepath.Join(c, pathHash+".narinfo"))
			other := strings.ReplaceAll(string(data), pathHash, refHash)
			put(t, filepath.Join(c, refHash+".narinfo"), other)
		}, want: ErrCache},
		"log not empty": {plant: func(t *testing.T, c string) {
			put(t, filepath.Join(c, logDir, "x"), "")
		}, want: ErrCache},
		"cache info changed": {plant: func(t *testing.T, c string) {
			put(t, filepath.Join(c, cacheInfo), cacheInfoText+"Priority: 1\n")
		}, want: ErrCache},
		"other store in cache info": {plant: func(t *testing.T, c string) {
			put(t, filepath.Join(c, cacheInfo), "StoreDir: /tmp/stor\n")
		}, want: ErrCache},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := export(t, tc.over)
			if tc.plant != nil {
				tc.plant(t, c)
			}
			if _, err := settle(t, c, 1<<20); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if fi, err := os.Stat(outside); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("outside file changed: %v, %v", fi.Mode(), err)
			}
		})
	}
}

func TestSettleBudget(t *testing.T) {
	if _, err := settle(t, export(t, nil), 99); !errors.Is(err, ErrCache) {
		t.Errorf("over budget: %v", err)
	}
	if _, err := settle(t, export(t, nil), 100); err != nil {
		t.Errorf("at budget: %v", err)
	}
}

package nettrust

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadVerdictTrustsOnlyFreshRootVerdict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vpn-trust.json")
	if err := WriteStatus(path, Verdict{Trust: TrustHome, Reason: "r", Bypass: []string{}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if verdict, ok := ReadVerdict(path, now); os.Geteuid() == 0 && (!ok || verdict.Trust != TrustHome) {
		t.Fatal("a fresh root verdict is not home")
	}
	if _, ok := ReadVerdict(path, now); os.Geteuid() != 0 && ok {
		t.Fatal("a verdict not owned by root counts as home")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadVerdict(link, now); ok {
		t.Fatal("a link counts as home")
	}
}

// The verdict file stays as it is while the verdict does not change; the
// stamp next to it carries the freshness.
func TestReadVerdictFollowsTheStamp(t *testing.T) {
	old := rootFileOwner
	rootFileOwner = uint32(os.Getuid())
	t.Cleanup(func() { rootFileOwner = old })
	path := filepath.Join(t.TempDir(), "vpn-trust.json")
	v := Verdict{Trust: TrustHome, Reason: "synthetic", Bypass: []string{}}
	if err := WriteStatus(path, v); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if got, expiry, ok := ReadVerdictExpiry(path, now); !ok || got.Trust != TrustHome || expiry.Before(now.Add(VerdictMaxAge-time.Minute)) {
		t.Fatalf("a fresh verdict does not count: %v %v %v", got, expiry, ok)
	}
	if _, ok := ReadVerdict(path, now.Add(VerdictMaxAge+time.Minute)); ok {
		t.Fatal("a stale verdict counts")
	}

	hourAgo := now.Add(-time.Hour)
	for _, p := range []string{path, CheckedPath(path)} {
		if err := os.Chtimes(p, hourAgo, hourAgo); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := ReadVerdict(path, now); ok {
		t.Fatal("a verdict with an old stamp counts")
	}
	if err := WriteStatus(path, v); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(hourAgo) {
		t.Fatalf("unchanged verdict rewritten: %v", err)
	}
	if _, ok := ReadVerdict(path, now); !ok {
		t.Fatal("a renewed unchanged verdict does not count")
	}

	// A stamp for another verdict vouches for nothing.
	if err := os.WriteFile(path, []byte(`{"trust":"home","reason":"other","bypass":[]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadVerdict(path, now); ok {
		t.Fatal("a stamp for another verdict counts")
	}
	if err := WriteStatus(path, v); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(CheckedPath(path), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadVerdict(path, now); ok {
		t.Fatal("a stamp others can write counts")
	}
	if err := os.Remove(CheckedPath(path)); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadVerdict(path, now); ok {
		t.Fatal("a verdict without a stamp counts")
	}
}

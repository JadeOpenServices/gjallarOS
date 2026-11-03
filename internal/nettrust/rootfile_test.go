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
	if err := os.WriteFile(path, []byte(`{"trust":"home","reason":"r","bypass":[],"exitNode":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if verdict, ok := ReadVerdict(path, now); os.Geteuid() == 0 && (!ok || verdict.Trust != TrustHome) {
		t.Fatal("a fresh root verdict is not home")
	}
	if _, ok := ReadVerdict(path, now); os.Geteuid() != 0 && ok {
		t.Fatal("a verdict not owned by root counts as home")
	}
	if _, ok := ReadVerdict(path, now.Add(VerdictMaxAge+time.Minute)); ok {
		t.Fatal("a stale verdict counts as home")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadVerdict(link, now); ok {
		t.Fatal("a link counts as home")
	}
}

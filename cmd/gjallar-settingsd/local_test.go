package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/localchanges"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// SYNTHETIC folder contents, not taken from any machine.
var (
	testFolder  = map[string][]byte{"home/editor.nix": []byte("{ programs.example.enable = true; }\n")}
	otherFolder = map[string][]byte{"system/extra.nix": []byte("{ }\n")}
)

// localFixture points the service at a developer-mode folder in a
// temporary directory. ReadRoot asks for root as owner, so the test reads
// it with ReadDir, which takes the same files.
func localFixture(t *testing.T, developerMode bool) *fixture {
	t.Helper()
	f := newFixture(t)
	f.svc.localPath = filepath.Join(filepath.Dir(f.path), "developer", "local")
	if err := os.Mkdir(filepath.Dir(f.svc.localPath), 0o755); err != nil {
		t.Fatal(err)
	}
	f.svc.readLocal = func(dir string) (localchanges.Tree, error) {
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			return localchanges.Tree{}, nil
		}
		return localchanges.ReadDir(dir)
	}
	f.setDeveloperMode(t, developerMode)
	return f
}

func (f *fixture) setDeveloperMode(t *testing.T, on bool) {
	t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(on)
	updated, _, err := config.ChangeText(data, map[string]json.RawMessage{"developerMode": value})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, updated, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) localHash(t *testing.T) string {
	t.Helper()
	tree, err := f.svc.readLocal(f.svc.localPath)
	if err != nil {
		t.Fatal(err)
	}
	return tree.Hash()
}

func TestLocalChangeNeedsDeveloperMode(t *testing.T) {
	f := localFixture(t, false)
	if _, err := f.svc.RequestLocal(context.Background(), ":1.10", testFolder); !errors.Is(err, errRefused) {
		t.Fatalf("request with developer mode off: %v, want refused", err)
	}
}

func TestLocalChangeWaitsAndSwapsFolder(t *testing.T) {
	f := localFixture(t, true)
	ctx := context.Background()
	change, err := f.svc.RequestLocal(ctx, ":1.10", testFolder)
	if err != nil {
		t.Fatal(err)
	}
	if change.Tier != config.TierSecurity || change.Local == nil || len(change.Local.Files) != 1 {
		t.Fatalf("change = %+v", change)
	}
	if _, err := f.svc.RequestLocal(ctx, ":1.10", otherFolder); !errors.Is(err, errRefused) {
		t.Fatalf("second folder change: %v, want refused", err)
	}
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply at once: %v, want refused", err)
	}
	f.sys.home = true
	f.now = f.now.Add(settings.ReadingDelay)
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.localHash(t); got != change.Local.NewHash {
		t.Fatalf("folder hash = %s, want %s", got, change.Local.NewHash)
	}
	if len(f.sys.asked) != 1 || f.sys.asked[0] != "org.gjallaros.settings.security local" {
		t.Fatalf("password checks = %v", f.sys.asked)
	}
	if len(f.sys.rebuilt) != 1 {
		t.Fatalf("rebuilds = %v", f.sys.rebuilt)
	}
	if _, err := f.svc.RequestLocal(ctx, ":1.10", testFolder); !errors.Is(err, errRefused) {
		t.Fatalf("asking for the folder root already holds: %v, want refused", err)
	}
}

func TestLocalChangeRefusedWhenFolderOrModeChanged(t *testing.T) {
	f := localFixture(t, true)
	ctx := context.Background()
	f.sys.home = true
	change, err := f.svc.RequestLocal(ctx, ":1.10", testFolder)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := localchanges.Check(otherFolder)
	if err != nil {
		t.Fatal(err)
	}
	if err := localchanges.Replace(f.svc.localPath, tree); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(settings.ReadingDelay)
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply after the folder changed: %v, want refused", err)
	}
	if got := f.localHash(t); got != tree.Hash() {
		t.Fatal("a refused change wrote the folder")
	}
	if pending, _ := f.svc.Pending(ctx, ":1.10"); len(pending) != 0 {
		t.Fatalf("a stale change still waits: %v", pending)
	}

	change, err = f.svc.RequestLocal(ctx, ":1.10", testFolder)
	if err != nil {
		t.Fatal(err)
	}
	f.setDeveloperMode(t, false)
	f.now = f.now.Add(settings.ReadingDelay)
	if err := f.svc.Apply(ctx, ":1.10", change.ID, ""); !errors.Is(err, errRefused) {
		t.Fatalf("apply after developer mode went off: %v, want refused", err)
	}
	if len(f.sys.rebuilt) != 0 {
		t.Fatalf("rebuilds = %v", f.sys.rebuilt)
	}
}

func TestLocalChangeRefusesBadFolder(t *testing.T) {
	f := localFixture(t, true)
	bad := map[string][]byte{"home/../../etc/x.nix": []byte("{ }\n")}
	if _, err := f.svc.RequestLocal(context.Background(), ":1.10", bad); !errors.Is(err, errRefused) {
		t.Fatalf("request with a path that leaves the folder: %v, want refused", err)
	}
}

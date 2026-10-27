package release

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInspect(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deployment", "release-policy.json"), []byte(`{"release":"26.05"}`), 0644); err != nil {
		t.Fatal(err)
	}
	osr := filepath.Join(root, "os-release")
	if err := os.WriteFile(osr, []byte("ID=nixos\nVERSION_ID=\"26.05\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	expected, actual, err := Inspect(root, osr)
	if err != nil || expected != "26.05" || actual != "26.05" {
		t.Fatalf("%q %q %v", expected, actual, err)
	}
}

func TestExpected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "deployment", "release-policy.json"),
		[]byte(`{"release":"26.05"}`),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	expected, err := Expected(root)
	if err != nil {
		t.Fatal(err)
	}
	if expected != "26.05" {
		t.Fatalf("expected=%q", expected)
	}
}

func TestExpectedRejectsMissingRelease(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "deployment", "release-policy.json"),
		[]byte(`{"release":""}`),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := Expected(root); err == nil {
		t.Fatal("empty pinned release accepted")
	}
}

func TestPinnedUsesFlakeLock(t *testing.T) {
	data, err := os.ReadFile("../../../flake.lock")
	if err != nil {
		t.Skip("flake.lock is not in this source tree")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "flake.lock"), data, 0644); err != nil {
		t.Fatal(err)
	}
	expected, err := Expected("../../..")
	if err != nil {
		t.Fatal(err)
	}
	src, err := Pinned(root, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(src.URL, "https://github.com/") || !strings.HasSuffix(src.URL, ".tar.gz") || !strings.HasPrefix(src.NarHash, "sha256-") {
		t.Fatalf("%+v", src)
	}
	if _, err := Pinned(root, "1.0"); err == nil {
		t.Fatal("lock for another release accepted")
	}
}

func TestPinnedRejectsUnlocked(t *testing.T) {
	root := t.TempDir()
	lock := `{"root":"root","nodes":{"root":{"inputs":{"nixpkgs":"nixpkgs"}},"nixpkgs":{"locked":{"type":"github","owner":"nixos","repo":"nixpkgs","rev":"main"},"original":{"ref":"nixos-26.05"}}}}`
	if err := os.WriteFile(filepath.Join(root, "flake.lock"), []byte(lock), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Pinned(root, "26.05"); err == nil {
		t.Fatal("branch name accepted as a locked revision")
	}
}

func TestCommandsUseFetchedTree(t *testing.T) {
	got := Commands(Source{URL: "https://github.com/nixos/nixpkgs/archive/x.tar.gz"}, "/nix/store/tree", "/etc/nixos/configuration.nix")
	want := "nixos-rebuild boot -I nixpkgs=/nix/store/tree -I nixos-config=/etc/nixos/configuration.nix"
	if len(got) != 3 || strings.Join(got[2], " ") != want || slices.Contains(got[2], "--upgrade") {
		t.Fatalf("%q", got)
	}
}

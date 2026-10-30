package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMakeRunDir(t *testing.T) {
	runtimeDir := t.TempDir()
	dir, err := makeRunDir(runtimeDir, "synthetic-1", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("run dir %v %v", info, err)
	}
	if _, err := makeRunDir(runtimeDir, "synthetic-1", os.Getuid()); err == nil {
		t.Error("existing run dir reused")
	}
	if _, err := makeRunDir(runtimeDir, "synthetic-2", os.Getuid()+1); err == nil {
		t.Error("folder of another uid accepted")
	}

	open := t.TempDir()
	if err := os.Mkdir(filepath.Join(open, "gjallar-wine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := makeRunDir(open, "synthetic-1", os.Getuid()); err == nil {
		t.Error("open parent accepted")
	}

	linked := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linked, "gjallar-wine")); err != nil {
		t.Fatal(err)
	}
	if _, err := makeRunDir(linked, "synthetic-1", os.Getuid()); err == nil {
		t.Error("linked parent accepted")
	}
}

func TestParseLock(t *testing.T) {
	sym, refer, argv, err := parseLock([]string{"--sym", "/p", "--sym", "/tmp", "--refer", "/", "--", "/bin/wine", "app.exe", "--sym"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sym, []string{"/p", "/tmp"}) || !slices.Equal(refer, []string{"/"}) ||
		!slices.Equal(argv, []string{"/bin/wine", "app.exe", "--sym"}) {
		t.Fatalf("sym %v refer %v argv %v", sym, refer, argv)
	}
	for _, args := range [][]string{
		nil,
		{"--sym"},
		{"--sym", "rel", "--", "/bin/wine"},
		{"--refer", "/", "--"},
		{"--", "wine"},
		{"--other", "/x", "--", "/bin/wine"},
		{"--sym", "/p"},
	} {
		if _, _, _, err := parseLock(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
}

package snapshots

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, name := range []string{"home.20261021T0300+0200", "home.20261021T0300-0130_2"} {
		if !ValidName(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{"", "home", "config.20261021T0300+0200", "home.20261021T0300+0200/..", "../home.20261021T0300+0200", "home.20261021T0300+0200_12345", "home.20261021T03+0200"} {
		if ValidName(name) {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestNewest(t *testing.T) {
	got := Newest([]string{"home.20261021T0300+0200", "junk", "home.20261021T0200+0000", "home.20261021T0300+0200_1"})
	want := []string{"home.20261021T0200+0000", "home.20261021T0300+0200_1", "home.20261021T0300+0200"}
	if !slices.Equal(got, want) {
		t.Fatalf("Newest = %v, want %v", got, want)
	}
}

func TestInHome(t *testing.T) {
	name, rel, err := InHome("/home/tester", "/home/tester/docs/notes.txt")
	if err != nil || name != "tester" || rel != "docs/notes.txt" {
		t.Fatalf("InHome = %q %q %v", name, rel, err)
	}
	for _, tc := range [][2]string{
		{"/home/tester", "/home/tester"},
		{"/home/tester", "/home/other/x"},
		{"/home/tester", "/home/tester/../other/x"},
		{"/home/tester", "docs/notes.txt"},
		{"/home/tester", "/home/testerx/notes.txt"},
		{"/var/lib/tester", "/var/lib/tester/x"},
		{"/home/a/b", "/home/a/b/x"},
	} {
		if _, _, err := InHome(tc[0], tc[1]); !errors.Is(err, ErrRefused) {
			t.Errorf("InHome(%q, %q) = %v, want refused", tc[0], tc[1], err)
		}
	}
}

func TestOpen(t *testing.T) {
	uid := os.Getuid()
	snap := t.TempDir()
	home := filepath.Join(snap, "tester")
	if err := os.MkdirAll(filepath.Join(home, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "docs", "notes.txt"), []byte("SYNTHETIC notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("SYNTHETIC outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "docs"), filepath.Join(home, "linkdir")); err != nil {
		t.Fatal(err)
	}

	f, err := Open(snap, "tester", "docs/notes.txt", uid)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "SYNTHETIC notes" {
		t.Fatalf("read %q", data)
	}

	for _, rel := range []string{"link.txt", "linkdir/notes.txt", "../tester/docs/notes.txt", "docs"} {
		if f, err := Open(snap, "tester", rel, uid); !errors.Is(err, ErrRefused) {
			if f != nil {
				f.Close()
			}
			t.Errorf("Open(%q) = %v, want refused", rel, err)
		}
	}
	if _, err := Open(snap, "tester", "gone.txt", uid); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := Open(snap, "nobody-here", "docs/notes.txt", uid); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing home: %v", err)
	}
	if _, err := Open(snap, "tester", "docs/notes.txt", uid+1); !errors.Is(err, ErrRefused) {
		t.Errorf("other uid: %v", err)
	}
}

func TestRestoredName(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"notes.txt", "home.20261021T0300+0200"}: "notes.restored-20261021T0300.txt",
		{".bashrc", "home.20261021T0300-0130_2"}: ".bashrc.restored-20261021T0300_2",
		{"Makefile", "home.20261021T0300+0000"}:  "Makefile.restored-20261021T0300",
		{"a.tar.gz", "home.20261021T0300+0200"}:  "a.tar.restored-20261021T0300.gz",
	} {
		if got := RestoredName(in[0], in[1]); got != want {
			t.Errorf("RestoredName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

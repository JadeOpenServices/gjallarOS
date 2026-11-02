package winesandbox

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartLog(t *testing.T) {
	home := t.TempDir()
	if _, _, err := ReadStartLog(home, "synthetic"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no log yet: %v", err)
	}
	l, err := OpenStartLog(home, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := l.Stream(&out)
	line := []byte(strings.Repeat("S", 1023) + "\n") // SYNTHETIC output
	for range 300 {
		if n, err := w.Write(line); n != len(line) || err != nil {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 300*len(line) {
		t.Errorf("passed on %d bytes", out.Len())
	}
	dir := StartLogDir(home)
	for _, name := range []string{"synthetic.log", "synthetic.log.1"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > StartLogCap || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %d bytes, %v", name, info.Size(), info.Mode())
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("log folder: %v %v", info.Mode(), err)
	}
	b, when, err := ReadStartLog(home, "synthetic")
	if err != nil || when.IsZero() {
		t.Fatal(err)
	}
	if len(b) > 2*StartLogCap || len(b) < StartLogCap || !bytes.HasSuffix(b, line) {
		t.Errorf("read %d bytes", len(b))
	}

	// A new start drops the last one, older part too.
	l, err = OpenStartLog(home, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	l.Stream(io.Discard).Write([]byte("SYNTHETIC\n"))
	l.Close()
	if b, _, err := ReadStartLog(home, "synthetic"); err != nil || string(b) != "SYNTHETIC\n" {
		t.Errorf("new start: %q %v", b, err)
	}

	// One write over the cap keeps its end only.
	l, _ = OpenStartLog(home, "synthetic")
	big := append(bytes.Repeat([]byte("A"), StartLogCap), "SYNTHETIC end\n"...)
	l.Stream(io.Discard).Write(big)
	l.Close()
	if b, _, _ := ReadStartLog(home, "synthetic"); len(b) > StartLogCap || !bytes.HasSuffix(b, []byte("SYNTHETIC end\n")) {
		t.Errorf("big write kept %d bytes", len(b))
	}

	if err := DropStartLog(home, "synthetic"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadStartLog(home, "synthetic"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dropped log: %v", err)
	}
	if err := DropStartLog(t.TempDir(), "synthetic"); err != nil {
		t.Errorf("no log folder: %v", err)
	}
	for _, id := range []string{"../x", ""} {
		if _, err := OpenStartLog(home, id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

func TestStartLogRefusesLinks(t *testing.T) {
	home := t.TempDir()
	dir := StartLogDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("SYNTHETIC"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "synthetic.log")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadStartLog(home, "synthetic"); err == nil {
		t.Error("read through a link")
	}
	if l, err := OpenStartLog(home, "synthetic"); err == nil {
		l.Close()
		if b, _ := os.ReadFile(outside); string(b) != "SYNTHETIC" {
			t.Error("wrote through a link")
		}
	}
	if err := os.Remove(filepath.Join(dir, "synthetic.log")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "synthetic.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadStartLog(home, "synthetic"); err == nil {
		t.Error("read a folder")
	}

	// The log folder itself as a link.
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Dir(StartLogDir(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), StartLogDir(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStartLog(home, "synthetic"); err == nil {
		t.Error("log folder link accepted")
	}
	if _, _, err := ReadStartLog(home, "synthetic"); err == nil {
		t.Error("read from a log folder link")
	}
}

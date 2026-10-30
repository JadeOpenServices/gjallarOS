package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
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

// fakeWine writes SYNTHETIC wine and wineserver scripts; wineserver notes
// how it was called in calls.
func fakeWine(t *testing.T, wine string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	scripts := map[string]string{
		"wine":       "#!/bin/sh\n" + wine + "\n",
		"wineserver": "#!/bin/sh\necho \"$1\" >> " + calls + "\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "wine"), calls
}

func TestStay(t *testing.T) {
	wine, calls := fakeWine(t, "exit 3")
	if code := stay([]string{wine}, nil, io.Discard); code != 3 {
		t.Errorf("exit code %d, want 3", code)
	}
	if got, err := os.ReadFile(calls); err != nil || string(got) != "-w\n" {
		t.Errorf("wineserver called %q %v, want -w", got, err)
	}
	if code := stay([]string{"wine"}, nil, io.Discard); code != 2 {
		t.Errorf("relative Wine accepted: %d", code)
	}
}

func TestStaySignal(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	wine, calls := fakeWine(t, "trap 'exit 7' TERM; echo > "+ready+"; while :; do sleep 0.05; done")
	signals := make(chan os.Signal, 1)
	result := make(chan int, 1)
	go func() { result <- stay([]string{wine}, signals, io.Discard) }()
	// The script has its trap once it writes to ready.
	if _, err := os.ReadFile(ready); err != nil {
		t.Fatal(err)
	}
	signals <- syscall.SIGTERM
	select {
	case code := <-result:
		if code != 7 {
			t.Errorf("exit code %d, want 7", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stay did not end")
	}
	if got, err := os.ReadFile(calls); err != nil || string(got) != "-k\n" {
		t.Errorf("wineserver called %q %v, want -k", got, err)
	}
}

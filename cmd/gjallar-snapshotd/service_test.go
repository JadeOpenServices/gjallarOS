package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/snapshots"
)

type fakeSystem struct {
	uid   int
	deny  bool
	asked []string
}

func (f *fakeSystem) UID(context.Context, string) (int, error) { return f.uid, nil }

func (f *fakeSystem) Authorize(_ context.Context, _, message string) error {
	f.asked = append(f.asked, message)
	if f.deny {
		return errDenied
	}
	return nil
}

// newTestService makes two SYNTHETIC home snapshots: the older one has
// old.txt, the newer one has notes.txt.
func newTestService(t *testing.T) (*service, *fakeSystem) {
	t.Helper()
	root := t.TempDir()
	write := func(snapshot, name, text string) {
		dir := filepath.Join(root, "home", snapshot, "tester")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("home.20261021T020000+0200", "old.txt", "SYNTHETIC old")
	write("home.20261021T030000+0200", "notes.txt", "SYNTHETIC notes")
	if err := os.MkdirAll(filepath.Join(root, "home", "stray"), 0o700); err != nil {
		t.Fatal(err)
	}
	sys := &fakeSystem{uid: os.Getuid()}
	return &service{sys: sys, root: root, home: func(int) (string, error) { return "/home/tester", nil }}, sys
}

func read(t *testing.T, file *os.File) string {
	t.Helper()
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestListNewestFirst(t *testing.T) {
	svc, _ := newTestService(t)
	names, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "home.20261021T030000+0200" {
		t.Fatalf("List = %v", names)
	}
}

func TestRestoreFindsNewestSnapshotWithFile(t *testing.T) {
	svc, sys := newTestService(t)
	file, name, err := svc.Restore(context.Background(), ":1.5", "/home/tester/old.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "home.20261021T020000+0200" || read(t, file) != "SYNTHETIC old" {
		t.Fatalf("restored from %s", name)
	}
	if len(sys.asked) != 1 {
		t.Fatalf("password asked %d times", len(sys.asked))
	}
}

func TestRestoreFromNamedSnapshot(t *testing.T) {
	svc, _ := newTestService(t)
	if _, _, err := svc.Restore(context.Background(), ":1.5", "/home/tester/old.txt", "home.20261021T030000+0200"); !errors.Is(err, snapshots.ErrNotFound) {
		t.Fatalf("old.txt from the newer snapshot: %v", err)
	}
	file, _, err := svc.Restore(context.Background(), ":1.5", "/home/tester/notes.txt", "home.20261021T030000+0200")
	if err != nil {
		t.Fatal(err)
	}
	if read(t, file) != "SYNTHETIC notes" {
		t.Fatal("wrong content")
	}
}

// Requests that break a rule are refused before the password dialog.
func TestRestoreRefusesBeforeAsking(t *testing.T) {
	svc, sys := newTestService(t)
	for _, tc := range [][2]string{
		{"/home/other/notes.txt", ""},
		{"/etc/shadow", ""},
		{"/home/tester/notes.txt", "../home.20261021T030000+0200"},
		{"/home/tester/notes.txt", "stray"},
		{"/home/tester/notes.txt", "home.20261021T040000+0200"},
	} {
		if _, _, err := svc.Restore(context.Background(), ":1.5", tc[0], tc[1]); !errors.Is(err, snapshots.ErrRefused) {
			t.Errorf("Restore(%q, %q) = %v, want refused", tc[0], tc[1], err)
		}
	}
	if len(sys.asked) != 0 {
		t.Fatalf("password asked for refused requests: %v", sys.asked)
	}
}

func TestRestoreDenied(t *testing.T) {
	svc, sys := newTestService(t)
	sys.deny = true
	if _, _, err := svc.Restore(context.Background(), ":1.5", "/home/tester/notes.txt", ""); !errors.Is(err, errDenied) {
		t.Fatalf("Restore without password = %v", err)
	}
}

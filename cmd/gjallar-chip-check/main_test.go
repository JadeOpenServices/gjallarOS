package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

func TestChipReason(t *testing.T) {
	for _, test := range []struct {
		support diskcrypto.PCRLockSupport
		err     error
		want    string
	}{
		{diskcrypto.PCRLockSupported, nil, ""},
		{diskcrypto.PCRLockObsolete, nil, "the security chip lacks the PolicyAuthorizeNV command that the startup policy needs"},
		{diskcrypto.PCRLockPartial, nil, "the security chip lacks parts of TPM 2.0 that the startup policy needs"},
		{diskcrypto.PCRLockNoTPM, nil, "no usable TPM 2.0 security chip was found"},
		{diskcrypto.PCRLockSupported, errors.New("boom"), "the security chip could not be checked (boom)"},
	} {
		got := chipReason(context.Background(), func(context.Context) (diskcrypto.PCRLockSupport, error) {
			return test.support, test.err
		})
		if got != test.want {
			t.Errorf("%q/%v: got %q, want %q", test.support, test.err, got, test.want)
		}
	}
}

// TestWriteFactPlantedLinks plants links where root writes (L182): the link
// targets must stay untouched, and a loose folder or a linked folder is
// refused.
func TestWriteFactPlantedLinks(t *testing.T) {
	uid := os.Getuid()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "chip-check.json")
	for _, link := range []string{path + ".tmp", path} {
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFact(path, []byte("{}\n"), uid); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("link target changed: %q", got)
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("fact is not a regular file: %v %v", fi, err)
	}
	if _, err := os.Lstat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file left: %v", err)
	}

	loose := t.TempDir()
	if err := os.Chmod(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := writeFact(filepath.Join(loose, "chip-check.json"), []byte("{}\n"), uid); err == nil {
		t.Fatal("folder writable by others was accepted")
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(dir, linked); err != nil {
		t.Fatal(err)
	}
	if err := writeFact(filepath.Join(linked, "chip-check.json"), []byte("{}\n"), uid); err == nil {
		t.Fatal("linked folder was accepted")
	}
	if err := writeFact(path, []byte("{}\n"), uid+1); err == nil {
		t.Fatal("folder of another owner was accepted")
	}
}

// TestWriteFactFIFOAtTmp: a FIFO at the temporary name is removed, never
// opened, so root does not block on it.
func TestWriteFactFIFOAtTmp(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "chip-check.json")
	if err := syscall.Mkfifo(path+".tmp", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFact(path, []byte("{}\n"), os.Getuid()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("fact is not a regular file: %v %v", fi, err)
	}
}

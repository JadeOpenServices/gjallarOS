package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRecoveryKeyboardForPicksTheFirstLayout(t *testing.T) {
	cases := []struct {
		layout, variant string
		want            recoveryKeyboard
	}{
		{"de", "", recoveryKeyboard{Layout: "de"}},
		{"us,de", "intl,", recoveryKeyboard{Layout: "us", Variant: "intl"}},
		{"de", "nodeadkeys", recoveryKeyboard{Layout: "de", Variant: "nodeadkeys"}},
		{"", "", recoveryKeyboard{Layout: "us"}},
	}
	for _, c := range cases {
		got, err := recoveryKeyboardFor(c.layout, c.variant)
		if err != nil || got != c.want {
			t.Fatalf("recoveryKeyboardFor(%q, %q) = %+v, %v; want %+v", c.layout, c.variant, got, err, c.want)
		}
	}
}

func TestRecoveryKeyboardForRefusesOddValues(t *testing.T) {
	for _, c := range [][2]string{
		{"de+nodeadkeys", ""},
		{"../x", ""},
		{"de", "a b"},
		{"de", "\"x\""},
	} {
		if _, err := recoveryKeyboardFor(c[0], c[1]); err == nil {
			t.Fatalf("recoveryKeyboardFor(%q, %q) accepted", c[0], c[1])
		}
	}
}

func TestWriteRecoveryKeyboardReplacesALinkWithoutFollowing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, recoveryKeyboardFile)); err != nil {
		t.Fatal(err)
	}
	if err := writeRecoveryKeyboard(dir, recoveryKeyboard{Layout: "de"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "keep" {
		t.Fatalf("link target changed: %q", data)
	}
	data, err := os.ReadFile(filepath.Join(dir, recoveryKeyboardFile))
	if err != nil || string(data) != "{\"layout\":\"de\",\"variant\":\"\"}\n" {
		t.Fatalf("keyboard file = %q, %v", data, err)
	}
}

func TestWriteRecoveryKeyboardReplacesAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, recoveryKeyboardFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRecoveryKeyboard(dir, recoveryKeyboard{Layout: "us"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(dir, recoveryKeyboardFile))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("keyboard file is not regular: %v %v", info, err)
	}
}

func TestWriteRecoveryKeyboardRefusesALinkedGeneratedFolder(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "generated")); err != nil {
		t.Fatal(err)
	}
	if err := writeRecoveryKeyboard(dir, recoveryKeyboard{Layout: "us"}); err == nil {
		t.Fatal("linked generated/ accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote through the link: %v", entries)
	}
}

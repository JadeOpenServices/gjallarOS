package tty

import (
	"os"
	"testing"
)

func TestCharacterDevicesAreNotTerminals(t *testing.T) {
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		file, err := os.Open(path)
		if err != nil {
			t.Skipf("%s: %v", path, err)
		}
		if Is(file) {
			t.Errorf("%s counted as a terminal", path)
		}
		file.Close()
	}
}

func TestPipeAndFileAreNotTerminals(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if Is(r) || Is(w) {
		t.Error("pipe counted as a terminal")
	}
	file, err := os.CreateTemp(t.TempDir(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if Is(file) {
		t.Error("regular file counted as a terminal")
	}
	if Is(nil) {
		t.Error("nil counted as a terminal")
	}
}

package movehelper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/prepared"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

var testDigest = strings.Repeat("ab", 32)

// oldTop is a stand-in for the old disk's top level with an empty @ root.
func oldTop(t *testing.T) (string, func() *safefs.Dir) {
	t.Helper()
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, "@", "var", "lib", "gjallarOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	return top, func() *safefs.Dir {
		d, err := safefs.OpenDir(top)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
}

func recordAt(top string) string {
	return filepath.Join(top, "@", "var", "lib", "gjallarOS", prepared.Name)
}

func writeRecord(t *testing.T, top string, pin bool) {
	t.Helper()
	raw := `{"version":1,"time":"` + time.Date(2026, 11, 18, 5, 0, 0, 0, time.UTC).Format(time.RFC3339) +
		`","pin":` + map[bool]string{true: "true", false: "false"}[pin] + `,"header":"` + testDigest + `"}` + "\n"
	if err := os.WriteFile(recordAt(top), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadPrepared(t *testing.T) {
	top, open := oldTop(t)
	if s, pin := readPrepared(open(), testDigest); s != PrepareNone || pin {
		t.Fatalf("no record: %s %v", s, pin)
	}
	writeRecord(t, top, true)
	if s, pin := readPrepared(open(), testDigest); s != PrepareFound || !pin {
		t.Fatalf("record: %s %v", s, pin)
	}
	if s, pin := readPrepared(open(), strings.Repeat("cd", 32)); s != PrepareStale || pin {
		t.Fatalf("other header: %s %v", s, pin)
	}
	if s, pin := readPrepared(open(), ""); s != PrepareStale || pin {
		t.Fatalf("no digest: %s %v", s, pin)
	}
	if err := os.WriteFile(recordAt(top), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, pin := readPrepared(open(), testDigest); s != PrepareUnreadable || pin {
		t.Fatalf("damaged record: %s %v", s, pin)
	}
}

// A link at the record, pointing at a good record elsewhere, is not
// followed: the record counts as unreadable.
func TestReadPreparedRefusesLink(t *testing.T) {
	top, open := oldTop(t)
	elsewhere := t.TempDir()
	writeRecord(t, top, true)
	if err := os.Rename(recordAt(top), filepath.Join(elsewhere, "r")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "r"), recordAt(top)); err != nil {
		t.Fatal(err)
	}
	if s, pin := readPrepared(open(), testDigest); s != PrepareUnreadable || pin {
		t.Fatalf("linked record: %s %v", s, pin)
	}
}

// A link on the way to the record is not followed either.
func TestReadPreparedRefusesLinkedFolder(t *testing.T) {
	top, open := oldTop(t)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "gjallarOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(top, "@", "var", "lib")
	if err := os.RemoveAll(lib); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, lib); err != nil {
		t.Fatal(err)
	}
	// A good record sits behind the link.
	writeRecord(t, top, true)
	if _, err := os.Stat(filepath.Join(other, "gjallarOS", prepared.Name)); err != nil {
		t.Fatal(err)
	}
	if s, _ := readPrepared(open(), testDigest); s != PrepareUnreadable {
		t.Fatalf("record behind a linked folder: %s", s)
	}
}

// A FIFO at the record is refused without opening it, so the read cannot
// hang the helper.
func TestReadPreparedRefusesFIFO(t *testing.T) {
	top, open := oldTop(t)
	if err := unix.Mkfifo(recordAt(top), 0o600); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		s   string
		pin bool
	}
	done := make(chan answer, 1)
	d := open()
	go func() {
		s, pin := readPrepared(d, testDigest)
		done <- answer{s, pin}
	}()
	select {
	case a := <-done:
		if a.s != PrepareUnreadable || a.pin {
			t.Fatalf("FIFO record: %s %v", a.s, a.pin)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read hung on the FIFO")
	}
}

// A record over the size cap is not read.
func TestReadPreparedRefusesBigFile(t *testing.T) {
	top, open := oldTop(t)
	if err := os.WriteFile(recordAt(top), make([]byte, prepared.MaxSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, _ := readPrepared(open(), testDigest); s != PrepareUnreadable {
		t.Fatalf("big record: %s", s)
	}
}

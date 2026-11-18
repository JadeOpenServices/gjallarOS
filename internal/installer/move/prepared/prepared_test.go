package prepared

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var digest = strings.Repeat("ab", 32)

func asOwner(t *testing.T) {
	t.Helper()
	old := ownerUID
	ownerUID = uint32(os.Geteuid())
	t.Cleanup(func() { ownerUID = old })
}

func record() Record {
	return Record{Version: Version, Time: time.Date(2026, 11, 18, 5, 0, 0, 0, time.UTC), PIN: true, Header: digest}
}

func TestWriteThenParse(t *testing.T) {
	asOwner(t)
	dir := t.TempDir()
	if err := Write(dir, record()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r != record() || !r.Fits(digest) || r.Fits(strings.Repeat("cd", 32)) {
		t.Fatalf("record = %+v", r)
	}
	st, err := os.Lstat(filepath.Join(dir, Name))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestParseRefuses(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field": `{"version":1,"time":"2026-11-18T05:00:00Z","pin":true,"header":"` + digest + `","key":"x"}`,
		"version":       `{"version":2,"time":"2026-11-18T05:00:00Z","pin":true,"header":"` + digest + `"}`,
		"short digest":  `{"version":1,"time":"2026-11-18T05:00:00Z","pin":true,"header":"abcd"}`,
		"upper digest":  `{"version":1,"time":"2026-11-18T05:00:00Z","pin":true,"header":"` + strings.ToUpper(digest) + `"}`,
		"no time":       `{"version":1,"pin":true,"header":"` + digest + `"}`,
		"two values":    `{"version":1,"time":"2026-11-18T05:00:00Z","pin":true,"header":"` + digest + `"}{}`,
		"not json":      `version=1`,
		"over size cap": `{"version":1,"time":"2026-11-18T05:00:00Z","pin":true,"header":"` + digest + `"}` + strings.Repeat(" ", MaxSize),
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestFitsNeedsADigest(t *testing.T) {
	if (Record{}).Fits("") {
		t.Fatal("an empty record fits an empty digest")
	}
}

// A link planted at the record name is replaced, not followed.
func TestWriteReplacesPlantedLink(t *testing.T) {
	asOwner(t)
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, Name)); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, record()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("the link target was written: %q", got)
	}
	st, err := os.Lstat(filepath.Join(dir, Name))
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("record is not a plain file: %v %v", st, err)
	}
}

// A FIFO planted at the record name is replaced without being opened, so
// the write cannot hang on it.
func TestWriteReplacesPlantedFIFO(t *testing.T) {
	asOwner(t)
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, Name), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Write(dir, record()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write hung on the FIFO")
	}
	st, err := os.Lstat(filepath.Join(dir, Name))
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("record is not a plain file: %v %v", st, err)
	}
}

func TestWriteRefusesLinkedFolder(t *testing.T) {
	asOwner(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "lib")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, record()); err == nil {
		t.Fatal("a linked folder was written to")
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("written through the link: %v", entries)
	}
}

func TestWriteRefusesFolderOthersCanWrite(t *testing.T) {
	asOwner(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, record()); err == nil {
		t.Fatal("a folder anyone can write to was used")
	}
}

func TestWriteRefusesFolderOfAnotherOwner(t *testing.T) {
	old := ownerUID
	ownerUID = uint32(os.Geteuid()) + 1
	t.Cleanup(func() { ownerUID = old })
	if err := Write(t.TempDir(), record()); err == nil {
		t.Fatal("a folder of another owner was used")
	}
}

func TestWriteRefusesIncompleteRecord(t *testing.T) {
	asOwner(t)
	r := record()
	r.Header = ""
	if err := Write(t.TempDir(), r); err == nil {
		t.Fatal("a record without digest was written")
	}
}

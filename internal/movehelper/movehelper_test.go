package movehelper

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

func run(t *testing.T, base, req string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(base, strings.NewReader(req), &out)
	return out.String(), err
}

func TestDirsMakesClosedFolders(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	if _, err := run(t, base, `{"op":"dirs"}`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{base, filepath.Join(base, Old), filepath.Join(base, New)} {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", p, fi.Mode(), err)
		}
	}
	// A second run accepts its own folders.
	if _, err := run(t, base, `{"op":"dirs"}`); err != nil {
		t.Fatal(err)
	}
}

// L58: folders someone else prepared stop the move.
func TestDirsRefusesPlanted(t *testing.T) {
	outside := t.TempDir()
	for name, plant := range map[string]func(base string) error{
		"link": func(base string) error { return os.Symlink(outside, base) },
		"open": func(base string) error { return os.Mkdir(base, 0o777) },
		"new link": func(base string) error {
			return twoSteps(base, func() error { return os.Symlink(outside, filepath.Join(base, New)) })
		},
		"old file": func(base string) error {
			return twoSteps(base, func() error { return os.WriteFile(filepath.Join(base, Old), nil, 0o600) })
		},
	} {
		base := filepath.Join(t.TempDir(), "gjallar-move")
		if err := plant(base); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, base, `{"op":"dirs"}`); err == nil {
			t.Errorf("%s: planted folder accepted", name)
		}
		if fi, _ := os.Stat(outside); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s: changed the link target", name)
		}
	}
}

func twoSteps(base string, then func() error) error {
	if err := os.Mkdir(base, 0o700); err != nil {
		return err
	}
	return then()
}

func TestRequestChecks(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	for _, req := range []string{
		`{"op":"rm"}`,
		`{"op":"dirs","path":"/etc"}`,
		`{"op":"dirs"}{"op":"dirs"}`,
		`{"op":"copy","src":"/etc","dst":"home/a","uid":1000,"gid":100,"budget":1}`,
		`{"op":"copy","src":"home/a","dst":"../x","uid":1000,"gid":1000,"budget":1}`,
		`{"op":"copy","src":"home/a","dst":"home/a","uid":0,"gid":0,"budget":1}`,
		`{"op":"copy","src":"home/a","dst":"home/a","srcUid":0,"uid":1000,"gid":100,"budget":1}`,
		`{"op":"copy","src":"home/a","dst":"home/a","uid":1000,"gid":1,"budget":1}`,
		`{"op":"copy","src":"home/a","dst":"home/a","uid":65534,"gid":100,"budget":1}`,
		`{"op":"copy","src":"home/a","dst":"home/a","uid":1000,"gid":1000}`,
		`{"op":"copy","src":"home/./a","dst":"home/a","uid":1000,"gid":1000,"budget":1}`,
		`{"op":"dirs","x":"` + strings.Repeat("a", MaxRequest) + `"}`,
	} {
		if _, err := run(t, base, req); err == nil {
			t.Errorf("accepted %.80s", req)
		}
	}
}

func TestCopy(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	if err := Dirs(base); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, Old, "home/a")
	if err := os.MkdirAll(filepath.Join(home, "Documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Documents/n.txt"), []byte("note"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, New, "home/a"), 0o700); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	if os.Geteuid() == 0 {
		// As root (the VM run) the old home belongs to a normal user, as
		// on a real disk.
		uid, gid = minUserID, usersGID
		for _, p := range []string{home, filepath.Join(home, "Documents"), filepath.Join(home, "Documents/n.txt")} {
			if err := os.Lchown(p, uid, gid); err != nil {
				t.Fatal(err)
			}
		}
	} else if uid < minUserID {
		t.Skip("the test user is not a normal user")
	}
	req, _ := json.Marshal(Request{Op: "copy", Src: "home/a", Dst: "home/a", SrcUID: uid, UID: uid, GID: gid, Budget: 1 << 20})
	out, err := run(t, base, string(req))
	if err != nil {
		t.Fatal(err)
	}
	var stats safefs.Stats
	if err := json.Unmarshal([]byte(out), &stats); err != nil || stats.Files != 1 || stats.Folders != 1 {
		t.Fatalf("%q %v", out, err)
	}
	if got, err := os.ReadFile(filepath.Join(base, New, "home/a/Documents/n.txt")); err != nil || string(got) != "note" {
		t.Fatalf("%q %v", got, err)
	}

	// A link planted as the target home is refused.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, New, "home/b")); err != nil {
		t.Fatal(err)
	}
	req, _ = json.Marshal(Request{Op: "copy", Src: "home/a", Dst: "home/b", SrcUID: uid, UID: uid, GID: gid, Budget: 1 << 20})
	if _, err := run(t, base, string(req)); err == nil {
		t.Fatal("copied through a planted link")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside: %v", entries)
	}
}

func TestOpenRefusesBeforeTouchingDisks(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	long := strings.Repeat("A", 2*maxKey)
	for name, req := range map[string]string{
		"key too long":  `{"op":"open","disk":"/dev/vda","key":"` + long + `"}`,
		"no disk":       `{"op":"open","key":"QUFB"}`,
		"no disk check": `{"op":"check"}`,
		"unknown field": `{"op":"open","disk":"/dev/vda","path":"/dev/vda2"}`,
		"key not bytes": `{"op":"open","disk":"/dev/vda","key":"not base64!"}`,
	} {
		if out, err := run(t, base, req); err == nil {
			t.Errorf("%s: accepted: %s", name, out)
		}
	}
}

// Close finds nothing open and says so without failing.
func TestCloseNothingOpen(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	out, err := run(t, base, `{"op":"close"}`)
	if err != nil || strings.TrimSpace(out) != "{}" {
		t.Fatalf("got %q, %v", out, err)
	}
}

// Inventory and sizes read only the old disk that open mounted: a plain
// folder at Old, or none at all, is not looked at.
func TestInventoryNeedsOpenDisk(t *testing.T) {
	for _, op := range []string{"inventory", "sizes"} {
		testNeedsOpenDisk(t, op)
	}
}

func testNeedsOpenDisk(t *testing.T, op string) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	for name, prep := range map[string]func() error{
		"no folders": func() error { return nil },
		"not mounted": func() error {
			_, err := run(t, base, `{"op":"dirs"}`)
			return err
		},
	} {
		if err := prep(); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, base, `{"op":"`+op+`"}`)
		var res RefusalResult
		if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Refusal == nil || res.Refusal.Reason != "not-open" {
			t.Errorf("%s %s: got %q, %v", op, name, out, err)
		}
	}
}

// L58: a second helper run while one holds the folder is turned away.
func TestSecondRunRefused(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gjallar-move")
	unlock, err := lock(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, base, `{"op":"close"}`); err == nil || !strings.Contains(err.Error(), "another move step") {
		t.Fatalf("second run: %v", err)
	}
	unlock()
	if _, err := run(t, base, `{"op":"close"}`); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
}

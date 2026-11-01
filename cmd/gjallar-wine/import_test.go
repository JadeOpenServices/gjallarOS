package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// oldPrefix lays out a SYNTHETIC prefix like the old launcher made: the
// home's folders linked in, Z: on /, data on C: and one program.
func oldPrefix(t *testing.T, f *fakeSetup) string {
	t.Helper()
	old := filepath.Join(f.home, ".local/share/wine/win11")
	writeFile(t, filepath.Join(old, "system.reg"), []byte("SYNTHETIC"))
	writeFile(t, filepath.Join(old, "drive_c/SyntheticData/ledger.db"), []byte("SYNTHETIC data"))
	writeFile(t, filepath.Join(old, "drive_c/Program Files/Synthetic/app.exe"), []byte("SYNTHETIC"))
	writeFile(t, filepath.Join(old, "drive_c/ProgramData/Microsoft/Windows/Start Menu/Programs/app.lnk"),
		lnk(`C:\Program Files\Synthetic\app.exe`))
	for _, l := range [][2]string{
		{"../drive_c", "dosdevices/c:"},
		{"/", "dosdevices/z:"},
		{f.home, "drive_c/users/tester/Documents"},
		{f.home, "drive_c/users/tester/Desktop"},
		{"/nix/store", "drive_c/store"},
		{"../../..", "drive_c/users/up"},
		{"SyntheticData/ledger.db", "drive_c/ledger.db"},
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(old, l[1])), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(l[0], filepath.Join(old, l[1])); err != nil {
			t.Fatal(err)
		}
	}
	return old
}

func TestImport(t *testing.T) {
	f := newFakeSetup(t)
	old := oldPrefix(t, f)
	var out strings.Builder
	if err := importCmd([]string{"Synthetic Books", old}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, paths, err := winesandbox.LoadApp(f.home, "synthetic-books")
	if err != nil || a.Exe != `C:\Program Files\Synthetic\app.exe` {
		t.Fatalf("app %+v %v", a, err)
	}
	if data, err := os.ReadFile(filepath.Join(paths.Prefix, "drive_c/SyntheticData/ledger.db")); err != nil || string(data) != "SYNTHETIC data" {
		t.Errorf("data %q %v", data, err)
	}
	if got, err := os.Readlink(filepath.Join(paths.Prefix, "drive_c/ledger.db")); err != nil || got != "SyntheticData/ledger.db" {
		t.Errorf("inner link %q %v", got, err)
	}
	for _, gone := range []string{"dosdevices/z:", "drive_c/store", "drive_c/users/up"} {
		if _, err := os.Lstat(filepath.Join(paths.Prefix, gone)); err == nil {
			t.Errorf("%s kept", gone)
		}
	}
	if got, err := os.Readlink(filepath.Join(paths.Prefix, "drive_c/users/tester/Documents")); err != nil || got != filepath.Join(paths.Files, "Documents") {
		t.Errorf("Documents -> %q %v", got, err)
	}
	if info, err := os.Lstat(filepath.Join(paths.Prefix, "drive_c/users/tester/Desktop")); err != nil || !info.IsDir() {
		t.Errorf("Desktop %v %v", info, err)
	}
	if got, err := os.Readlink(filepath.Join(old, "dosdevices/z:")); err != nil || got != "/" {
		t.Errorf("old prefix changed: %q %v", got, err)
	}
	if _, ok := menuOf(t, f, "synthetic-books"); !ok {
		t.Error("no menu entry")
	}
	if !slices.Equal(f.switches, []string{"synthetic-books"}) {
		t.Errorf("switches cleared for %v", f.switches)
	}
	if f.runs(t) != 0 {
		t.Error("Wine's setup ran for an import")
	}
	if !strings.Contains(out.String(), old+" stays as it was") {
		t.Errorf("output %q", out.String())
	}

	if err := importCmd([]string{"Synthetic Books", old}, io.Discard, io.Discard); err == nil {
		t.Error("imported over an app")
	}
	if err := importCmd([]string{"Again", paths.Prefix}, io.Discard, io.Discard); err == nil {
		t.Error("imported an app's own prefix")
	}
	plain := t.TempDir()
	writeFile(t, filepath.Join(plain, "system.reg"), nil)
	for _, bad := range [][]string{{"X", plain}, {"X", filepath.Join(old, "system.reg")}, {".x", old}, {"X"}, {"X", "/nonexistent"}} {
		if err := importCmd(bad, io.Discard, io.Discard); err == nil {
			t.Errorf("import %q accepted", bad)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(paths.Prefix))
	if len(entries) != 1 {
		t.Errorf("prefixes %v", entries)
	}
}

package winesandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// basePrefix lays out the parts of a Wine prefix FixPrefix touches, as
// SYNTHETIC files.
func basePrefix(t *testing.T, home, user string) string {
	t.Helper()
	prefix := filepath.Join(t.TempDir(), "prefix")
	userDir := filepath.Join(prefix, "drive_c", "users", user)
	for _, dir := range []string{filepath.Join(prefix, "dosdevices"), userDir, filepath.Join(userDir, "Music")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(prefix, "dosdevices", "c:"): "../drive_c",
		filepath.Join(prefix, "dosdevices", "z:"): "/",
		filepath.Join(prefix, "dosdevices", "e:"): home,
		filepath.Join(userDir, "Documents"):       filepath.Join(home, "Documents"),
		filepath.Join(userDir, "Desktop"):         filepath.Join(home, "Desktop"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	return prefix
}

func TestFixPrefix(t *testing.T) {
	home, files := t.TempDir(), t.TempDir()
	prefix := basePrefix(t, home, "tester")
	if err := FixPrefix(prefix, files, "tester"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(prefix, "dosdevices"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("drives %v %v", entries, err)
	}
	for drive, want := range map[string]string{"c:": "../drive_c", "d:": files} {
		if got, err := os.Readlink(filepath.Join(prefix, "dosdevices", drive)); err != nil || got != want {
			t.Errorf("%s -> %q %v, want %q", drive, got, err, want)
		}
	}
	userDir := filepath.Join(prefix, "drive_c", "users", "tester")
	for _, name := range []string{"Documents", "Downloads", "Pictures", "Videos"} {
		if got, err := os.Readlink(filepath.Join(userDir, name)); err != nil || got != files {
			t.Errorf("%s -> %q %v", name, got, err)
		}
	}
	if info, err := os.Lstat(filepath.Join(userDir, "Music")); err != nil || !info.IsDir() {
		t.Errorf("Wine's own Music folder replaced: %v %v", info, err)
	}
	if info, err := os.Lstat(filepath.Join(userDir, "Desktop")); err != nil || !info.IsDir() {
		t.Errorf("Desktop is not a folder: %v %v", info, err)
	}
}

func TestWinPath(t *testing.T) {
	home := t.TempDir()
	paths := PathsFor(home, "synthetic", "Synthetic App")
	exe := filepath.Join(paths.Prefix, "drive_c", "Program Files", "Synthetic", "app.exe")
	setup := filepath.Join(paths.Files, "setup.exe")
	for _, f := range []string{exe, setup, filepath.Join(home, "elsewhere.exe")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("SYNTHETIC"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]string{
		exe:   `C:\Program Files\Synthetic\app.exe`,
		setup: `D:\setup.exe`,
	} {
		got, err := WinPath(paths, path)
		if err != nil || got != want {
			t.Errorf("WinPath(%s) = %q %v, want %q", path, got, err, want)
		}
		back, err := HostPath(paths, got)
		if err != nil || back != path {
			t.Errorf("HostPath(%q) = %s %v", got, back, err)
		}
	}
	if _, err := WinPath(paths, filepath.Join(home, "elsewhere.exe")); err == nil {
		t.Error("file outside the app accepted")
	}
	link := filepath.Join(paths.Files, "link.exe")
	if err := os.Symlink(filepath.Join(home, "elsewhere.exe"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := WinPath(paths, link); err == nil {
		t.Error("link out of the files folder accepted")
	}
	for _, bad := range []string{`Z:\nix\store\x.exe`, `C:\..\..\x.exe`, `C:\a\\b.exe`, `C:x.exe`, `D:\a/../b.exe`, ""} {
		if _, err := HostPath(paths, bad); err == nil {
			t.Errorf("HostPath(%q) accepted", bad)
		}
	}
}

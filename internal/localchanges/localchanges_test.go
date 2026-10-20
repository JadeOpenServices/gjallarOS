package localchanges

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func ownedByMe(t *testing.T) {
	t.Helper()
	uid, parents := rootUID, checkParents
	rootUID, checkParents = uint32(os.Getuid()), false
	t.Cleanup(func() { rootUID, checkParents = uid, parents })
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		name := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckName(t *testing.T) {
	for _, good := range []string{"README.md", "system/default.nix", "home/zsh.nix", "system/_off.nix", "home/wall/paper.png", "system/a/b/c.nix"} {
		if err := CheckName(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, bad := range []string{"", "/system/a.nix", "system/../a.nix", "./system/a.nix", "system//a.nix", "flake.nix",
		"notes.txt", "other/a.nix", "system/.hidden.nix", "system/.d/a.nix", "system/run.sh", "system/a b.nix",
		"system/a/b/c/d.nix", "system/", "system"} {
		if err := CheckName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckCaps(t *testing.T) {
	if _, err := Check(map[string][]byte{"system/big.nix": make([]byte, MaxFileBytes+1)}); err == nil {
		t.Error("accepted a file over the size cap")
	}
	files := map[string][]byte{}
	for i := 0; i < 5; i++ {
		files["system/f"+string(rune('a'+i))+".nix"] = make([]byte, MaxFileBytes)
	}
	if _, err := Check(files); err == nil {
		t.Error("accepted a folder over the total cap")
	}
	files = map[string][]byte{}
	for i := 0; i <= MaxFiles; i++ {
		files[filepath.Join("system", strings.Repeat("a", 1+i%40)+string(rune('a'+i/40))+".nix")] = nil
	}
	if _, err := Check(files); err == nil {
		t.Error("accepted more files than the cap")
	}
}

func TestHashMatchesSha256sumManifest(t *testing.T) {
	tree, err := Check(map[string][]byte{"system/b.nix": []byte("{ }\n"), "README.md": []byte("hi\n")})
	if err != nil {
		t.Fatal(err)
	}
	line := func(path, text string) string {
		sum := sha256.Sum256([]byte(text))
		return hex.EncodeToString(sum[:]) + "  " + path + "\n"
	}
	sum := sha256.Sum256([]byte(line("README.md", "hi\n") + line("system/b.nix", "{ }\n")))
	if tree.Hash() != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash %s does not match the manifest", tree.Hash())
	}
	if got := strings.Join(tree.Paths(), ","); got != "README.md,system/b.nix" {
		t.Fatalf("paths %s", got)
	}
	if tree.Empty() {
		t.Fatal("a tree with a .nix file is empty")
	}
}

func TestReadRootAndWrite(t *testing.T) {
	ownedByMe(t)
	dir := filepath.Join(t.TempDir(), "local")
	if tree, err := ReadRoot(dir); err != nil || len(tree.Files) != 0 {
		t.Fatalf("missing folder: %v %v", tree, err)
	}
	writeTree(t, dir, map[string]string{"README.md": "x\n", "system/a.nix": "{ }\n", "home/b.nix": "{ }\n"})
	tree, err := ReadRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(t.TempDir(), ".gjallar-local")
	if err := Write(staged, tree); err != nil {
		t.Fatal(err)
	}
	again, err := ReadRoot(staged)
	if err != nil || again.Hash() != tree.Hash() {
		t.Fatalf("staged copy differs: %v", err)
	}
}

func TestReadRootRefuses(t *testing.T) {
	ownedByMe(t)
	cases := map[string]func(dir string){
		"symlink": func(dir string) {
			os.Symlink("/etc/passwd", filepath.Join(dir, "system", "x.nix"))
		},
		"group-writable": func(dir string) {
			os.Chmod(filepath.Join(dir, "system", "a.nix"), 0o664)
		},
		"hidden": func(dir string) {
			os.WriteFile(filepath.Join(dir, "system", ".x.nix"), nil, 0o644)
		},
		"script": func(dir string) {
			os.WriteFile(filepath.Join(dir, "system", "x.sh"), nil, 0o644)
		},
	}
	for name, break_ := range cases {
		dir := filepath.Join(t.TempDir(), "local")
		writeTree(t, dir, map[string]string{"system/a.nix": "{ }\n"})
		break_(dir)
		if _, err := ReadRoot(dir); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	rootUID = uint32(os.Getuid()) + 1
	dir := filepath.Join(t.TempDir(), "local")
	writeTree(t, dir, map[string]string{"system/a.nix": "{ }\n"})
	if _, err := ReadRoot(dir); err == nil {
		t.Error("a folder not owned by root was accepted")
	}
}

func TestReadDir(t *testing.T) {
	rootUID = uint32(os.Getuid()) + 1
	t.Cleanup(func() { rootUID = 0 })
	dir := filepath.Join(t.TempDir(), "GjallarOS-local")
	writeTree(t, dir, map[string]string{"system/a.nix": "{ }\n"})
	os.Chmod(filepath.Join(dir, "system", "a.nix"), 0o664)
	if tree, err := ReadDir(dir); err != nil || strings.Join(tree.Paths(), ",") != "system/a.nix" {
		t.Fatalf("own folder: %v %v", tree, err)
	}
	os.Symlink("/etc/passwd", filepath.Join(dir, "system", "x.nix"))
	if _, err := ReadDir(dir); err == nil {
		t.Error("a symlink was accepted")
	}
	if _, err := ReadDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing folder read as empty")
	}
}

func TestReplace(t *testing.T) {
	ownedByMe(t)
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
	dir := filepath.Join(t.TempDir(), "local")
	first, _ := Check(map[string][]byte{"system/a.nix": []byte("{ }\n"), "home/x/b.nix": []byte("{ }\n")})
	if err := Replace(dir, first); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"": 0o755, "home": 0o755, "home/x": 0o755, "home/x/b.nix": 0o644} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%q has mode %v, want %v", name, info.Mode().Perm(), want)
		}
	}
	second, _ := Check(map[string][]byte{"system/c.nix": []byte("{ }\n")})
	if err := Replace(dir, second); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRoot(dir)
	if err != nil || got.Hash() != second.Hash() {
		t.Fatalf("after the swap: %v %v", got.Paths(), err)
	}
	if _, err := os.Lstat(dir + ".new"); err == nil {
		t.Error("the old folder was left next to the new one")
	}
	if err := Replace(dir, Tree{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Error("an empty tree left the folder")
	}
}

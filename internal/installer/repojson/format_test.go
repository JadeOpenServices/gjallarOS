package repojson

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "gjallarOS Test")
	return root
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCanonicalizeModifiedTrackedJSON(t *testing.T) {
	root := repo(t)
	path := filepath.Join(root, "device.json")

	write(t, path, "{\"schema\":1,\"name\":\"test\"}\n")
	git(t, root, "add", "device.json")
	git(t, root, "commit", "-qm", "base")

	write(t, path, "{\"schema\":1,\"name\":\"changed\"}")

	paths, err := CanonicalizeChangedTracked(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "device.json" {
		t.Fatalf("paths=%v", paths)
	}

	want := "{\n  \"schema\": 1,\n  \"name\": \"changed\"\n}\n"
	if got := read(t, path); got != want {
		t.Fatalf("formatted JSON:\n%q\nwant:\n%q", got, want)
	}
}

func TestCanonicalizeNewlyTrackedJSON(t *testing.T) {
	root := repo(t)
	path := filepath.Join(root, "new.json")

	write(t, path, "{\"b\":2,\"a\":1}")
	git(t, root, "add", "new.json")

	paths, err := CanonicalizeChangedTracked(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "new.json" {
		t.Fatalf("paths=%v", paths)
	}

	want := "{\n  \"b\": 2,\n  \"a\": 1\n}\n"
	if got := read(t, path); got != want {
		t.Fatalf("formatted JSON:\n%q\nwant:\n%q", got, want)
	}
}

func TestCanonicalizeLeavesUntrackedJSONAlone(t *testing.T) {
	root := repo(t)
	path := filepath.Join(root, "untracked.json")
	original := "{\"oneLine\":true}"

	write(t, path, original)

	paths, err := CanonicalizeChangedTracked(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths=%v", paths)
	}
	if got := read(t, path); got != original {
		t.Fatalf("untracked JSON changed: %q", got)
	}
}

func TestCanonicalizeRejectsInvalidTrackedJSON(t *testing.T) {
	root := repo(t)
	path := filepath.Join(root, "broken.json")

	write(t, path, "{\"ok\":true}\n")
	git(t, root, "add", "broken.json")
	git(t, root, "commit", "-qm", "base")

	original := "{\"broken\":"
	write(t, path, original)

	_, err := CanonicalizeChangedTracked(context.Background(), root)
	if err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if !strings.Contains(err.Error(), "parse JSON") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := read(t, path); got != original {
		t.Fatalf("invalid JSON was modified: %q", got)
	}
}

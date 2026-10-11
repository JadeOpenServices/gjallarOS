package binarycache

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeCache(t *testing.T, body string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "deployment"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "deployment", "binary-cache.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestArgs(t *testing.T) {
	repo := writeCache(t, `{"url": "https://example.cachix.org", "publicKey": "example.cachix.org-1:abc="}`)
	want := []string{
		"--option", "extra-substituters", "https://example.cachix.org",
		"--option", "extra-trusted-public-keys", "example.cachix.org-1:abc=",
	}
	if got := Args(repo); !slices.Equal(got, want) {
		t.Fatalf("Args = %q, want %q", got, want)
	}
}

func TestArgsNone(t *testing.T) {
	for name, repo := range map[string]string{
		"missing file": t.TempDir(),
		"empty":        writeCache(t, `{"url": "", "publicKey": ""}`),
		"plain http":   writeCache(t, `{"url": "http://example.org", "publicKey": "k"}`),
		"no key":       writeCache(t, `{"url": "https://example.org", "publicKey": ""}`),
		"broken json":  writeCache(t, `{`),
	} {
		if got := Args(repo); got != nil {
			t.Errorf("%s: Args = %q, want nil", name, got)
		}
	}
}

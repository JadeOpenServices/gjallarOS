package app

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func swapInstalledAnswers(t *testing.T, data []byte, err error) *string {
	t.Helper()
	old := readInstalledAnswers
	var seen string
	readInstalledAnswers = func(root string) ([]byte, error) {
		seen = root
		return data, err
	}
	t.Cleanup(func() { readInstalledAnswers = old })
	return &seen
}

func TestImportInstalledAnswersCopiesIntoTheLiveRepository(t *testing.T) {
	// The checked-in neutral preset stands for a saved answers file.
	want, err := os.ReadFile("../../../scripts/installation/user_PresetJSON/default.user.config.json")
	if err != nil {
		t.Fatal(err)
	}
	seen := swapInstalledAnswers(t, want, nil)
	preset := filepath.Join(t.TempDir(), "user.config.json")
	var out bytes.Buffer
	if err := importInstalledAnswers("/mnt", preset, &out); err != nil {
		t.Fatal(err)
	}
	if *seen != "/mnt" {
		t.Fatalf("read from %q, want /mnt", *seen)
	}
	got, err := os.ReadFile(preset)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("copied %q, want %q", got, want)
	}
	info, err := os.Stat(preset)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), "installed system's /etc/gjallar/system/user.config.json") {
		t.Fatalf("output %q does not name the source", out.String())
	}
}

func TestImportInstalledAnswersKeepsTheRuntimeConfiguration(t *testing.T) {
	swapInstalledAnswers(t, nil, errors.New("must not be read"))
	preset := filepath.Join(t.TempDir(), "user.config.json")
	runtime := []byte(`{"username":"runtime"}`)
	if err := os.WriteFile(preset, runtime, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := importInstalledAnswers("/mnt", preset, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(preset)
	if !bytes.Equal(got, runtime) {
		t.Fatalf("runtime configuration replaced: %q", got)
	}
}

func TestImportInstalledAnswersRefusesALinkInTheLiveRepository(t *testing.T) {
	swapInstalledAnswers(t, nil, errors.New("must not be read"))
	dir := t.TempDir()
	preset := filepath.Join(dir, "user.config.json")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), preset); err != nil {
		t.Fatal(err)
	}
	if err := importInstalledAnswers("/mnt", preset, &bytes.Buffer{}); err == nil {
		t.Fatal("a link at the preset path was accepted")
	}
}

func TestImportInstalledAnswersWithoutSavedAnswersAsks(t *testing.T) {
	swapInstalledAnswers(t, nil, fs.ErrNotExist)
	preset := filepath.Join(t.TempDir(), "user.config.json")
	var out bytes.Buffer
	if err := importInstalledAnswers("/mnt", preset, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(preset); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preset written without answers: %v", err)
	}
	if !strings.Contains(out.String(), "the installer asks") {
		t.Fatalf("output %q", out.String())
	}
}

func TestImportInstalledAnswersStopsOnARefusedFile(t *testing.T) {
	swapInstalledAnswers(t, nil, errors.New("refuse etc/gjallar/system/user.config.json below /mnt: not a regular file"))
	preset := filepath.Join(t.TempDir(), "user.config.json")
	err := importInstalledAnswers("/mnt", preset, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(preset); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("preset written after a refused read")
	}
}

func TestImportInstalledAnswersRejectsBrokenAnswers(t *testing.T) {
	swapInstalledAnswers(t, []byte("{not json"), nil)
	preset := filepath.Join(t.TempDir(), "user.config.json")
	if err := importInstalledAnswers("/mnt", preset, &bytes.Buffer{}); err == nil {
		t.Fatal("broken answers accepted")
	}
	if _, err := os.Lstat(preset); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("broken answers copied")
	}
}

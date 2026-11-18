package oldroot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantPATH puts a cryptsetup and an lsblk that leave a mark in a folder in
// front of PATH, as a caller who controls PATH could (L114).
func plantPATH(t *testing.T) (mark string) {
	t.Helper()
	dir := t.TempDir()
	mark = filepath.Join(dir, "ran")
	for _, name := range []string{"cryptsetup", "lsblk"} {
		script := "#!/bin/sh\necho " + name + " >>\"" + mark + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return mark
}

func notRun(t *testing.T, mark string) {
	t.Helper()
	if data, err := os.ReadFile(mark); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planted program ran: %q, %v", data, err)
	}
}

func TestToolDefaultsAbsolute(t *testing.T) {
	for _, p := range []string{Cryptsetup, Lsblk} {
		if !filepath.IsAbs(p) {
			t.Errorf("%q is not absolute", p)
		}
	}
	for _, kv := range toolEnv {
		if strings.HasPrefix(kv, "PATH=") && strings.Contains(kv, "/home") {
			t.Errorf("tool PATH reaches a home: %s", kv)
		}
	}
}

// The open runs the cryptsetup it was built with, never the planted one.
func TestOpenIgnoresPlantedPATH(t *testing.T) {
	fakeCryptsetup(t, "ok")
	mark := plantPATH(t)
	if err := prepared.Open(context.Background(), strings.NewReader("key")); err != nil {
		t.Fatal(err)
	}
	if err := closeMapping(context.Background()); err != nil {
		t.Fatal(err)
	}
	notRun(t, mark)
}

// A bare name would be looked up through PATH: it is refused before
// anything runs.
func TestToolBareNameRefused(t *testing.T) {
	mark := plantPATH(t)
	for _, name := range []string{"cryptsetup", "./lsblk", "bin/lsblk"} {
		if err := Tool(context.Background(), name, "--version").Run(); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("%s: %v", name, err)
		}
	}
	notRun(t, mark)
}

// The caller's environment does not reach the tools: a DM_DEV_DIR or a
// planted PATH folder set by the caller is gone.
func TestToolEnvFixed(t *testing.T) {
	mark := plantPATH(t)
	t.Setenv("DM_DEV_DIR", "/run/example/fake-dev")
	t.Setenv("LD_PRELOAD", "/run/example/evil.so")
	out, err := Tool(context.Background(), "/bin/sh", "-c", "env").Output()
	if err != nil {
		if _, statErr := os.Stat("/bin/sh"); statErr != nil {
			t.Skip("no /bin/sh")
		}
		t.Fatal(err)
	}
	env := string(out)
	for _, bad := range []string{"DM_DEV_DIR", "LD_PRELOAD", filepath.Dir(mark)} {
		if strings.Contains(env, bad) {
			t.Errorf("tool env carries %s:\n%s", bad, env)
		}
	}
	if !strings.Contains(env, "LC_ALL=C") {
		t.Errorf("tool env:\n%s", env)
	}
}

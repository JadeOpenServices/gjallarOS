package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testReleaseImage = "/nix/store/example-gjallar-recovery-iso/iso/gjallar-recovery.iso"

// releaseRig is a release folder as sign-image.sh leaves it, with
// SYNTHETIC contents, and records what reaches root.
type releaseRig struct {
	dir     string
	written map[string]string
	calls   *[][]string
}

func newReleaseRig(t *testing.T) *releaseRig {
	t.Helper()
	rig := &releaseRig{dir: t.TempDir(), written: map[string]string{}}
	for name, data := range map[string]string{
		"gjallar-recovery.iso.manifest":     "schema=1\nimage=gjallar-recovery.iso\n",
		"gjallar-recovery.iso.manifest.sig": "example signature",
		"recovery-signing-public.pem":       "example public key",
	} {
		if err := os.WriteFile(filepath.Join(rig.dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rootDirs := t.TempDir()
	previousPrefix, previousWrite := recoveryReleasePrefix, writeRootFile
	t.Cleanup(func() { recoveryReleasePrefix, writeRootFile = previousPrefix, previousWrite })
	recoveryReleasePrefix = filepath.Join(rootDirs, "gjallar-recovery-release.")
	writeRootFile = func(_ context.Context, data []byte, dst string) error {
		rig.written[dst] = string(data)
		return nil
	}
	rig.calls = withPrivilegedCommand(t, func(args []string) ([]byte, error) {
		if args[0] == "mktemp" {
			dir, err := os.MkdirTemp(rootDirs, "gjallar-recovery-release.")
			return []byte(dir + "\n"), err
		}
		return nil, nil
	})
	return rig
}

func TestStageRecoveryReleaseHandsRootTheDataNotThePath(t *testing.T) {
	rig := newReleaseRig(t)
	dir, paths, err := stageRecoveryRelease(context.Background(), rig.dir, testReleaseImage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, recoveryReleasePrefix) || len(paths) != 3 {
		t.Fatalf("root folder %q, paths %v", dir, paths)
	}
	want := []string{"gjallar-recovery.iso.manifest", "gjallar-recovery.iso.manifest.sig", "recovery-signing-public.pem"}
	for i, path := range paths {
		if path != filepath.Join(dir, want[i]) {
			t.Errorf("path %d = %q, want %s in %s", i, path, want[i], dir)
		}
		data, _ := os.ReadFile(filepath.Join(rig.dir, want[i]))
		if rig.written[path] != string(data) {
			t.Errorf("%s got %q, want %q", path, rig.written[path], data)
		}
	}
	for _, call := range *rig.calls {
		if strings.Contains(strings.Join(call, " "), rig.dir) {
			t.Errorf("a root step names the user's folder: %v", call)
		}
	}
}

// L56: a link, a pipe or a huge file in the release folder never reaches
// root.
func TestStageRecoveryReleaseRefusesPlantedFiles(t *testing.T) {
	plants := map[string]func(path string) error{
		"link": func(path string) error { return os.Symlink("/etc/hostname", path) },
		"pipe": func(path string) error { return exec.Command("mkfifo", path).Run() },
		"oversize": func(path string) error {
			return os.WriteFile(path, make([]byte, recoveryReleaseCap+1), 0o600)
		},
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			rig := newReleaseRig(t)
			pem := filepath.Join(rig.dir, "recovery-signing-public.pem")
			if err := os.Remove(pem); err != nil {
				t.Fatal(err)
			}
			if err := plant(pem); err != nil {
				t.Fatal(err)
			}
			if _, _, err := stageRecoveryRelease(context.Background(), rig.dir, testReleaseImage); err == nil {
				t.Fatalf("planted %s accepted", name)
			}
			if len(*rig.calls) != 0 || len(rig.written) != 0 {
				t.Fatalf("root steps after a planted %s: %v %v", name, *rig.calls, rig.written)
			}
		})
	}
}

func TestStageRecoveryReleaseRefusesOddRootFolder(t *testing.T) {
	rig := newReleaseRig(t)
	withPrivilegedCommand(t, func(args []string) ([]byte, error) {
		return []byte("/etc\n"), nil
	})
	if _, _, err := stageRecoveryRelease(context.Background(), rig.dir, testReleaseImage); err == nil || !strings.Contains(err.Error(), "is not below") {
		t.Fatalf("odd root folder accepted: %v", err)
	}
	if len(rig.written) != 0 {
		t.Fatalf("wrote into an odd folder: %v", rig.written)
	}
}

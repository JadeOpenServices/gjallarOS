package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/deviceprofilecache"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

// withPrivilegedCommand records every privileged command and runs it through
// run (nil: succeed without executing).
func withPrivilegedCommand(t *testing.T, run func(args []string) ([]byte, error)) *[][]string {
	t.Helper()
	var calls [][]string
	previous := privilegedCommand
	privilegedCommand = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if run == nil {
			return nil, nil
		}
		return run(args)
	}
	t.Cleanup(func() { privilegedCommand = previous })
	return &calls
}

// runUnprivileged executes a privileged command without sudo, skipping the
// root-only chown, so the real file operations can be checked in a temp dir.
func runUnprivileged(args []string) ([]byte, error) {
	if args[0] == "chown" {
		return nil, nil
	}
	return exec.Command(args[0], args[1:]...).CombinedOutput()
}

func TestInstallTreePrivilegedCopiesBeforeReplacing(t *testing.T) {
	calls := withPrivilegedCommand(t, nil)
	if err := installTreePrivileged(context.Background(), "/tmp/stage/device-profile", "/mnt/var/lib/gjallarOS/device-profile"); err != nil {
		t.Fatal(err)
	}
	joined := make([]string, len(*calls))
	for i, c := range *calls {
		joined[i] = strings.Join(c, " ")
	}
	copyAt, replaceAt := -1, -1
	for i, c := range joined {
		if strings.HasPrefix(c, "cp -r -- /tmp/stage/device-profile ") {
			copyAt = i
		}
		if c == "mv -T -- /mnt/var/lib/gjallarOS/.device-profile.incoming /mnt/var/lib/gjallarOS/device-profile" {
			replaceAt = i
		}
	}
	if copyAt < 0 || replaceAt < 0 || copyAt > replaceAt {
		t.Fatalf("copy must complete before the destination is replaced:\n%s", strings.Join(joined, "\n"))
	}
}

func TestInstallTreePrivilegedCleansUpAfterFailedCopy(t *testing.T) {
	calls := withPrivilegedCommand(t, func(args []string) ([]byte, error) {
		if args[0] == "cp" {
			return nil, errors.New("disk full")
		}
		return nil, nil
	})
	err := installTreePrivileged(context.Background(), "/tmp/s/p", "/mnt/var/lib/p")
	if err == nil {
		t.Fatal("copy failure was ignored")
	}
	last := strings.Join((*calls)[len(*calls)-1], " ")
	if last != "rm -rf -- /mnt/var/lib/.p.incoming" {
		t.Fatalf("incoming tree not removed after failure, last command %q", last)
	}
	for _, c := range *calls {
		if c[0] == "mv" {
			t.Fatalf("destination touched after failed copy: %v", c)
		}
	}
}

func TestInstallTreePrivilegedRealFilesystem(t *testing.T) {
	withPrivilegedCommand(t, runUnprivileged)
	dir := t.TempDir()
	source := filepath.Join(dir, "stage", "profile")
	if err := os.MkdirAll(filepath.Join(source, "oddc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "oddc", "new"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "target", "var", "lib", "gjallarOS", "profile")
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "old"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installTreePrivileged(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "oddc", "new")); err != nil || string(data) != "new" {
		t.Fatalf("new tree not installed: %q %v", data, err)
	}
	for _, gone := range []string{filepath.Join(destination, "old"), destination + ".previous", filepath.Join(filepath.Dir(destination), ".profile.incoming")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", gone, err)
		}
	}
}

// L55: the user can plant a link or a pipe in the staged tree, or swap the
// whole tree for a link, before root copies it. The copy is refused and the
// installed tree stays as it was.
func TestInstallTreePrivilegedRefusesPlantedLinksAndPipes(t *testing.T) {
	withPrivilegedCommand(t, func(args []string) ([]byte, error) {
		output, err := runUnprivileged(args)
		if err != nil {
			return output, fmt.Errorf("%w: %s", err, output)
		}
		return output, nil
	})
	plants := map[string]func(source string) error{
		"link": func(source string) error {
			return os.Symlink("/etc/hostname", filepath.Join(source, "oddc", "link"))
		},
		"pipe": func(source string) error {
			return exec.Command("mkfifo", filepath.Join(source, "oddc", "pipe")).Run()
		},
		"swapped tree": func(source string) error {
			if err := os.Rename(source, source+".real"); err != nil {
				return err
			}
			return os.Symlink(source+".real", source)
		},
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "stage", "profile")
			destination := filepath.Join(dir, "target", "profile")
			for _, err := range []error{
				os.MkdirAll(filepath.Join(source, "oddc"), 0o700),
				os.WriteFile(filepath.Join(source, "oddc", "new"), []byte("new"), 0o600),
				os.MkdirAll(destination, 0o700),
				os.WriteFile(filepath.Join(destination, "old"), []byte("old"), 0o600),
				plant(source),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}

			err := installTreePrivileged(context.Background(), source, destination)
			if err == nil || !strings.Contains(err.Error(), "plain") {
				t.Fatalf("planted %s accepted: %v", name, err)
			}
			if data, err := os.ReadFile(filepath.Join(destination, "old")); err != nil || string(data) != "old" {
				t.Fatalf("installed tree changed: %q %v", data, err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "target", ".profile.incoming")); !os.IsNotExist(err) {
				t.Fatalf("refused copy left behind: %v", err)
			}
		})
	}
}

func TestStageFreshPasswordFilesCopiesRootToRootViaSudo(t *testing.T) {
	const source = "/var/lib/gjallarOS/passwords/root.hash"
	calls := withPrivilegedCommand(t, func(args []string) ([]byte, error) {
		if args[0] == "find" {
			return []byte(source + "\n"), nil
		}
		return nil, nil
	})
	if err := stageFreshPasswordFiles(context.Background(), []string{source, source}); err != nil {
		t.Fatal(err)
	}
	var installs int
	for _, c := range *calls {
		line := strings.Join(c, " ")
		if strings.HasPrefix(line, "install ") {
			installs++
			if line != "install -m 0600 -o root -g root -- "+source+" /mnt/var/lib/gjallarOS/passwords/.root.hash.tmp" {
				t.Fatalf("unexpected install %q", line)
			}
		}
	}
	if installs != 1 {
		t.Fatalf("expected one deduplicated install, got %d: %v", installs, *calls)
	}
}

func TestStageFreshPasswordFilesRejectsUnsafeSources(t *testing.T) {
	calls := withPrivilegedCommand(t, func(args []string) ([]byte, error) {
		return nil, nil // find printed nothing: symlink, empty or group/other readable
	})
	if err := stageFreshPasswordFiles(context.Background(), []string{"/var/lib/gjallarOS/passwords/root.hash"}); err == nil {
		t.Fatal("accepted a hash that failed the privileged file check")
	}
	for _, bad := range []string{"/etc/shadow", "/var/lib/gjallarOS/passwords/../root.hash", "/var/lib/gjallarOS/passwords/sub/x.hash"} {
		*calls = nil
		if err := stageFreshPasswordFiles(context.Background(), []string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		for _, c := range *calls {
			if c[0] == "install" {
				t.Fatalf("installed rejected path %q", bad)
			}
		}
	}
}

func TestMaterializeODDCCapsuleRunsAsUnprivilegedUser(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "oddc")); err != nil {
		t.Skip("repository oddc tree unavailable")
	}
	calls := withPrivilegedCommand(t, runUnprivileged)
	destination := filepath.Join(t.TempDir(), "mnt", "var", "lib", "gjallarOS", "device-profile")
	resolved := oddc.Resolved{Source: oddc.SourceMetadata{Kind: "embedded", Revision: "test-revision"}}
	if err := materializeODDCCapsule(context.Background(), repo, destination, discovery.Hardware{}, resolved, false, "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := deviceprofilecache.Verify(destination); err != nil {
		t.Fatalf("installed capsule does not verify: %v", err)
	}
	if len(*calls) == 0 {
		t.Fatal("capsule was not installed through the privileged command")
	}
}

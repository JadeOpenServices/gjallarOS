package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func recordPrivileged(t *testing.T, fail string) *[]string {
	t.Helper()
	var calls []string
	previous := privilegedCommand
	privilegedCommand = func(ctx context.Context, args ...string) ([]byte, error) {
		// Like exec.CommandContext: a cancelled context never starts the command.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if call == fail {
			return nil, errors.New("target is busy")
		}
		return nil, nil
	}
	t.Cleanup(func() { privilegedCommand = previous })
	fakeProc(t, nil)
	return &calls
}

// fakeProc points mappingUsers at a synthetic /proc: each pid gets a mount
// namespace link, a mount table and a comm.
func fakeProc(t *testing.T, procs map[string][3]string) {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p[0], filepath.Join(dir, "ns", "mnt")); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{"mountinfo": p[1], "comm": p[2]} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	prevProc, prevMapper := procRoot, mapperDir
	procRoot, mapperDir = root, filepath.Join(root, "no-mapper")
	t.Cleanup(func() { procRoot, mapperDir = prevProc, prevMapper })
}

const (
	plainMounts = "22 1 0:21 / / rw,relatime shared:1 - tmpfs tmpfs rw\n" +
		"40 22 0:30 / /dev/mapper/cryptroot rw shared:9 - tmpfs /dev/mapper/other rw\n"
	newDiskMounts = plainMounts +
		"90 22 254:1 / /run/x\\040y rw,relatime master:4 shared:12 - ext4 /dev/mapper/cryptroot rw\n"
)

// L151: a namespace that still mounts the new disk keeps cryptsetup close
// from running and names who holds it.
func TestCloseFreshTargetKeepsMappingHeldElsewhere(t *testing.T) {
	calls := recordPrivileged(t, "")
	fakeProc(t, map[string][3]string{
		"1":    {"mnt:[4026531841]", plainMounts, "systemd\n"},
		"55":   {"mnt:[4026531841]", newDiskMounts, "same-ns\n"},
		"712":  {"mnt:[4026532300]", newDiskMounts, "timesync\x1b[2J\n"},
		"self": {"mnt:[4026532301]", newDiskMounts, "x\n"},
	})
	var out bytes.Buffer
	closeFreshTarget(context.Background(), "/mnt", "cryptroot", &out)()
	if want := []string{"umount -R -- /mnt"}; !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
	if got := out.String(); !strings.Contains(got, "in use by timesync?[2J (pid 712)") {
		t.Fatalf("output = %q", got)
	}
}

// A mount point or another device named like the mapping is no hold; one
// namespace is read once; an oversized table is skipped; a tmpfs or FUSE
// mount a user namespace made with the mapping as its source is no hold.
func TestMappingUsers(t *testing.T) {
	big := strings.Repeat("x", maxProcMountinfo+1)
	fakeProc(t, map[string][3]string{
		"1": {"mnt:[1]", plainMounts, "init\n"},
		"2": {"mnt:[1]", newDiskMounts, "dup\n"},
		"3": {"mnt:[3]", big, "big\n"},
		"4": {"mnt:[4]", plainMounts + "91 22 254:2 / /a rw - ext4 /dev/mapper/cryptroot2 rw\n", "near\n"},
		"5": {"mnt:[5]", plainMounts +
			"92 22 0:77 / /home/live/a rw,nosuid,nodev - tmpfs /dev/mapper/cryptroot rw,uid=60600\n" +
			"93 22 0:78 / /home/live/b rw,nosuid,nodev - fuse.sshfs /dev/mapper/cryptroot rw,user_id=60600\n" +
			"94 22 0:79 / /home/live/c rw - overlay /dev/mapper/cryptroot rw,lowerdir=/x\n", "spoof\n"},
		"10": {"mnt:[10]", strings.ReplaceAll(newDiskMounts, "/dev/mapper/cryptroot rw\n", "/dev/mapper/crypt\\040root rw\n"), "esc\n"},
	})
	if got := mappingUsers("cryptroot"); len(got) != 0 {
		t.Errorf("users = %v", got)
	}
	if got := mappingUsers("crypt root"); len(got) != 1 || got[0].pid != 10 {
		t.Errorf("escaped source: users = %v", got)
	}
}

func TestCloseFreshTargetAfterCancel(t *testing.T) {
	calls := recordPrivileged(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	closeTarget := closeFreshTarget(ctx, "/mnt", "cryptroot", &out)
	cancel()
	closeTarget()
	want := []string{"umount -R -- /mnt", "cryptsetup close cryptroot"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
	if !strings.Contains(out.String(), "closed again") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCloseFreshTargetKeepsMappingWhileMounted(t *testing.T) {
	calls := recordPrivileged(t, "umount -R -- /mnt")
	var out bytes.Buffer
	closeFreshTarget(context.Background(), "/mnt", "cryptroot", &out)()
	if want := []string{"umount -R -- /mnt"}; !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
	if !strings.Contains(out.String(), "Restart before you install again") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCloseFreshTargetRefusesOptionLikeMapping(t *testing.T) {
	calls := recordPrivileged(t, "")
	closeFreshTarget(context.Background(), "/mnt", "--help", &bytes.Buffer{})()
	if want := []string{"umount -R -- /mnt"}; !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

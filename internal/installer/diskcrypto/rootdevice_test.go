package diskcrypto

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// withRootLookup answers findmnt with source and lsblk -s with the
// ancestors listed for that source only.
func withRootLookup(t *testing.T, source string, ancestors map[string]string) *[]string {
	t.Helper()
	var calls []string
	previous := rootLookup
	rootLookup = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "findmnt":
			return []byte(source + "\n"), nil
		case "lsblk":
			out, ok := ancestors[args[len(args)-1]]
			if !ok {
				return nil, fmt.Errorf("unexpected lsblk device %s", args[len(args)-1])
			}
			return []byte(out), nil
		}
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	t.Cleanup(func() { rootLookup = previous })
	return &calls
}

// Stock NixOS layout on the locked laptop (2026-11-15): root LUKS on p2,
// swap LUKS on p3, plus a LUKS USB stick. Only the root's device counts.
func TestRootDeviceIgnoresSwapAndStickLUKS(t *testing.T) {
	calls := withRootLookup(t, "/dev/mapper/luks-root", map[string]string{
		"/dev/mapper/luks-root": "/dev/mapper/luks-root ext4\n/dev/nvme0n1p2 crypto_LUKS\n/dev/nvme0n1 \n",
	})
	device, err := RootDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device != "/dev/nvme0n1p2" {
		t.Fatalf("root device = %q", device)
	}
	want := "findmnt -nvo SOURCE /\nlsblk -spnro NAME,FSTYPE /dev/mapper/luks-root"
	if got := strings.Join(*calls, "\n"); got != want {
		t.Fatalf("lookups must cover the root's devices only:\n%s", got)
	}
}

func TestRootDeviceThroughLVM(t *testing.T) {
	withRootLookup(t, "/dev/mapper/vg-root", map[string]string{
		"/dev/mapper/vg-root": "/dev/mapper/vg-root btrfs\n/dev/mapper/cryptlvm LVM2_member\n/dev/sda2 crypto_LUKS\n/dev/sda \n",
	})
	device, err := RootDevice(context.Background())
	if err != nil || device != "/dev/sda2" {
		t.Fatalf("root device = %q, %v", device, err)
	}
}

func TestRootDeviceWithoutLUKS(t *testing.T) {
	withRootLookup(t, "/dev/nvme0n1p2", map[string]string{
		"/dev/nvme0n1p2": "/dev/nvme0n1p2 ext4\n/dev/nvme0n1 \n",
	})
	device, err := RootDevice(context.Background())
	if err != nil || device != "" {
		t.Fatalf("root device = %q, %v", device, err)
	}
}

// A root spread over two LUKS devices (btrfs RAID, LVM over two disks) has
// no single device to enroll; refuse instead of picking one.
func TestRootDeviceRefusesSeveralLUKS(t *testing.T) {
	withRootLookup(t, "/dev/mapper/vg-root", map[string]string{
		"/dev/mapper/vg-root": "/dev/mapper/vg-root ext4\n/dev/mapper/a LVM2_member\n/dev/sda2 crypto_LUKS\n/dev/mapper/b LVM2_member\n/dev/sdb2 crypto_LUKS\n",
	})
	if device, err := RootDevice(context.Background()); err == nil {
		t.Fatalf("picked %q from a root on two LUKS devices", device)
	}
}

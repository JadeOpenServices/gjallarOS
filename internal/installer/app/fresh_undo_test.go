package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

const undoESP = "4b1e9c2a-6f3d-4e8a-9b7c-2d5f8a1e6c03"

// undoFirmware fakes efibootmgr, lsblk and findmnt for freshUndo.
type undoFirmware struct {
	entries map[string]string
	lsblk   string
	mounted bool
	calls   []string
}

func (f *undoFirmware) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	switch {
	case key == "efibootmgr -v":
		var b strings.Builder
		b.WriteString("BootCurrent: 0001\nBootOrder: 0003,0001\n")
		for _, n := range []string{"0001", "0003"} {
			if e, ok := f.entries[n]; ok {
				b.WriteString("Boot" + n + e + "\n")
			}
		}
		return []byte(b.String()), nil
	case strings.HasPrefix(key, "efibootmgr --quiet --bootnum ") && strings.HasSuffix(key, " --delete-bootnum"):
		delete(f.entries, args[2])
		return nil, nil
	case strings.HasPrefix(key, "lsblk "):
		return []byte(f.lsblk), nil
	case strings.HasPrefix(key, "findmnt "):
		if f.mounted {
			return []byte("/mnt/boot\n"), nil
		}
		return nil, errors.New("exit status 1")
	}
	return nil, os.ErrInvalid
}

func undoLsblk(path string) string {
	return `{"blockdevices":[
 {"path":"/dev/nvme0n1","type":"disk","partuuid":null,"partlabel":null,"parttype":null,"pkname":null},
 {"path":"` + path + `","type":"part","partuuid":"` + undoESP + `","partlabel":"EFI","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/nvme0n1"}
]}`
}

func useUndo(t *testing.T, f *undoFirmware, failOn string) *[]string {
	t.Helper()
	var priv []string
	prevRunner, prevPriv := bootNextRunner, privilegedCommand
	bootNextRunner = f
	privilegedCommand = func(_ context.Context, args ...string) ([]byte, error) {
		priv = append(priv, strings.Join(args, " "))
		if failOn != "" && args[0] == failOn {
			return nil, errors.New("refused")
		}
		return nil, nil
	}
	t.Cleanup(func() { bootNextRunner, privilegedCommand = prevRunner, prevPriv })
	return &priv
}

func newUndoFirmware(path string) *undoFirmware {
	return &undoFirmware{
		entries: map[string]string{
			"0001": "* UEFI USB\tPciRoot(0x0)/Pci(0x14,0x0)/USB(1,0)",
			"0003": "* Linux Boot Manager (shim)\tHD(1,GPT," + undoESP + ",0x800,0x100000)/File(\\EFI\\nixos\\shimx64.efi)",
		},
		lsblk: undoLsblk(path),
	}
}

func TestFreshUndoRemovesEntryAndClearsESP(t *testing.T) {
	f := newUndoFirmware("/dev/nvme0n1p1")
	priv := useUndo(t, f, "")
	u := &freshUndo{}
	u.arm("/dev/nvme0n1", undoESP)
	var out bytes.Buffer
	u.run(context.Background(), &out)
	if _, ok := f.entries["0003"]; ok {
		t.Fatalf("shim entry left: %v", f.entries)
	}
	if _, ok := f.entries["0001"]; !ok {
		t.Fatalf("USB entry removed: %v", f.entries)
	}
	if want := []string{"blkdiscard --zeroout -- /dev/nvme0n1p1"}; !reflect.DeepEqual(*priv, want) {
		t.Fatalf("root calls %q, want %q", *priv, want)
	}
	if !strings.Contains(out.String(), "Boot0003") || !strings.Contains(out.String(), "Cleared the boot partition") {
		t.Fatalf("output: %s", out.String())
	}
	f.calls, *priv = nil, nil
	u.run(context.Background(), &out)
	if len(f.calls) != 0 || len(*priv) != 0 {
		t.Fatalf("second run acted: %q %q", f.calls, *priv)
	}
}

func TestFreshUndoUnmountsFirst(t *testing.T) {
	f := newUndoFirmware("/dev/nvme0n1p1")
	f.mounted = true
	priv := useUndo(t, f, "")
	u := &freshUndo{}
	u.arm("/dev/nvme0n1", undoESP)
	u.run(context.Background(), &bytes.Buffer{})
	want := []string{"umount --all-targets -- /dev/nvme0n1p1", "blkdiscard --zeroout -- /dev/nvme0n1p1"}
	if !reflect.DeepEqual(*priv, want) {
		t.Fatalf("root calls %q, want %q", *priv, want)
	}
}

func TestFreshUndoKeepsMountedESPWhenUnmountFails(t *testing.T) {
	f := newUndoFirmware("/dev/nvme0n1p1")
	f.mounted = true
	priv := useUndo(t, f, "umount")
	u := &freshUndo{}
	u.arm("/dev/nvme0n1", undoESP)
	var out bytes.Buffer
	u.run(context.Background(), &out)
	if len(*priv) != 1 || !strings.Contains(out.String(), "still mounted") {
		t.Fatalf("root calls %q, output %s", *priv, out.String())
	}
}

func TestFreshUndoNotArmedDoesNothing(t *testing.T) {
	f := newUndoFirmware("/dev/nvme0n1p1")
	priv := useUndo(t, f, "")
	var none *freshUndo
	none.arm("/dev/nvme0n1", undoESP)
	none.run(context.Background(), &bytes.Buffer{})
	(&freshUndo{}).run(context.Background(), &bytes.Buffer{})
	u := &freshUndo{}
	u.arm("/dev/nvme0n1", undoESP)
	u.disarm()
	u.run(context.Background(), &bytes.Buffer{})
	if len(f.calls) != 0 || len(*priv) != 0 {
		t.Fatalf("acted: %q %q", f.calls, *priv)
	}
}

// A device path lsblk did not put under /dev (planted name, mount point,
// FIFO somewhere) or a partition on another disk is never wiped.
func TestFreshUndoRefusesUnexpectedDevice(t *testing.T) {
	for _, c := range []struct{ name, path, disk string }{
		{"outside /dev", "/run/user/1000/esp", "/dev/nvme0n1"},
		{"dot dot", "/dev/../tmp/esp", "/dev/nvme0n1"},
		{"other disk", "/dev/nvme0n1p1", "/dev/sda"},
	} {
		f := newUndoFirmware(c.path)
		priv := useUndo(t, f, "")
		u := &freshUndo{}
		u.arm(c.disk, undoESP)
		var out bytes.Buffer
		u.run(context.Background(), &out)
		if len(*priv) != 0 {
			t.Fatalf("%s: root calls %q", c.name, *priv)
		}
		if !strings.Contains(out.String(), "left as it is") {
			t.Fatalf("%s: output %s", c.name, out.String())
		}
	}
}

func TestFreshUndoWiring(t *testing.T) {
	bare, err := os.ReadFile("fresh_baremetal.go")
	if err != nil {
		t.Fatal(err)
	}
	arm := strings.Index(string(bare), "undo.arm(plan.TargetDisk.Path, plan.ESP.PARTUUID)")
	prov := strings.Index(string(bare), "freshgpt.Provision(")
	if arm < 0 || prov < 0 || arm > prov {
		t.Fatalf("undo must be armed before the partition table is written (arm %d, provision %d)", arm, prov)
	}
	app, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(app)
	run := strings.Index(src, "s.freshUndo.run(ctx, out)")
	save := strings.Index(src, "saveFailureToBootStick(")
	if run < 0 || save < 0 || run > save {
		t.Fatalf("undo must run in the failure path before the stick save (run %d, save %d)", run, save)
	}
	if strings.Count(src, "s.freshUndo.disarm()") != 2 {
		t.Fatal("undo must be disarmed when the shim path is not taken and after FinishFreshShim")
	}
}

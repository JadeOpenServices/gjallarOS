package bootnext

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fake struct {
	out   map[string]string
	err   map[string]error
	calls []string
}

func (f *fake) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	if err := f.err[key]; err != nil {
		return nil, err
	}
	out, ok := f.out[key]
	if !ok {
		return nil, errors.New("unexpected call " + key)
	}
	return []byte(out), nil
}

const lsblkKey = "lsblk -J -l -p -o PATH,TYPE,PARTUUID,PARTLABEL,PARTTYPE,PKNAME"

const (
	espA   = "9f6bc3da-7244-4fce-8759-6a3033d20cea"
	espB   = "1b2c3d4e-0000-4000-8000-00000000000b"
	espUSB = "aaaaaaaa-0000-4000-8000-0000000000cc"
	espWin = "bbbbbbbb-0000-4000-8000-0000000000dd"
)

// Two installed disks, the stick (ESP but no GJALLAROS partition) and a
// Windows disk.
var disks = `{"blockdevices":[
 {"path":"/dev/vda","type":"disk","partuuid":null,"partlabel":null,"parttype":null,"pkname":null},
 {"path":"/dev/vda1","type":"part","partuuid":"9F6BC3DA-7244-4FCE-8759-6A3033D20CEA","partlabel":"EFI","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/vda"},
 {"path":"/dev/vda2","type":"part","partuuid":"11111111-0000-4000-8000-000000000001","partlabel":"GJALLAROS","parttype":"ca7d7ccb-63ed-4c53-861c-1742536059cc","pkname":"/dev/vda"},
 {"path":"/dev/vdb1","type":"part","partuuid":"` + espB + `","partlabel":"EFI","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/vdb"},
 {"path":"/dev/vdb2","type":"part","partuuid":"11111111-0000-4000-8000-000000000002","partlabel":"GJALLAROS","parttype":"ca7d7ccb-63ed-4c53-861c-1742536059cc","pkname":"/dev/vdb"},
 {"path":"/dev/sda1","type":"part","partuuid":"` + espUSB + `","partlabel":"GJALLAR_STICK","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/sda"},
 {"path":"/dev/nvme0n1p1","type":"part","partuuid":"` + espWin + `","partlabel":"EFI system partition","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/nvme0n1"},
 {"path":"/dev/nvme0n1p3","type":"part","partuuid":"11111111-0000-4000-8000-000000000003","partlabel":"Basic data partition","parttype":"ebd0a0a2-b9e5-4433-87c0-68b6b72699c7","pkname":"/dev/nvme0n1"},
 {"path":"/dev/mapper/root","type":"crypt","partuuid":null,"partlabel":null,"parttype":null,"pkname":"/dev/vda2"}
]}`

func hd(uuid string) string { return "HD(1,GPT," + uuid + ",0x800,0x100000)" }

func find(t *testing.T, lsblk, boot string) []Install {
	t.Helper()
	f := &fake{out: map[string]string{lsblkKey: lsblk, "efibootmgr -v": boot}}
	got, err := Find(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFindOrdersByBootOrder(t *testing.T) {
	boot := strings.Join([]string{
		"BootCurrent: 0005",
		"BootOrder: 0005,0003,0001,0002",
		"Boot0001* UEFI QEMU HARDDISK\tPciRoot(0x0)/Pci(0x4,0x0)",
		"Boot0002* Linux Boot Manager (shim)\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`,
		"Boot0003* Linux Boot Manager\t" + hd(espB) + `/\EFI\systemd\systemd-bootx64.efi`,
		"Boot0004* Linux Boot Manager\t" + hd(espA) + `/File(\EFI\systemd\systemd-bootx64.efi)`,
		"Boot0005* UEFI USB\t" + hd(espUSB) + `/File(\EFI\systemd\systemd-bootx64.efi)`,
	}, "\n") + "\n"
	got := find(t, disks, boot)
	want := []Install{{"0003", "/dev/vdb"}, {"0002", "/dev/vda"}, {"0004", "/dev/vda"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// L90: only an exact device path on a gjallarOS disk counts.
func TestFindRefusesLookalikes(t *testing.T) {
	for name, line := range map[string]string{
		"inactive":      "Boot0002  Linux Boot Manager\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`,
		"recovery":      "Boot0002* gjallarOS recovery\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`,
		"load options":  "Boot0002* Linux Boot Manager\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)\EFI\other\grubx64.efi`,
		"options hex":   "Boot0002* Linux Boot Manager\t" + hd(espA) + `/\EFI\nixos\shimx64.efi0000424f`,
		"path in label": "Boot0002* " + hd(espA) + `/File(\EFI\nixos\shimx64.efi)` + "\tPciRoot(0x0)/Pci(0x4,0x0)",
		"other loader":  "Boot0002* Linux Boot Manager\t" + hd(espA) + `/File(\EFI\BOOT\BOOTX64.EFI)`,
		"stick esp":     "Boot0002* Linux Boot Manager\t" + hd(espUSB) + `/File(\EFI\nixos\shimx64.efi)`,
		"windows esp":   "Boot0002* Windows Boot Manager\t" + hd(espWin) + `/File(\EFI\systemd\systemd-bootx64.efi)`,
		"prefix path":   "Boot0002* Linux Boot Manager\tPciRoot(0x0)/" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`,
		"mbr":           "Boot0002* Linux Boot Manager\tHD(1,MBR,0x1234,0x800,0x100000)/File(\\EFI\\nixos\\shimx64.efi)",
		"unknown uuid":  "Boot0002* Linux Boot Manager\t" + hd("cccccccc-0000-4000-8000-0000000000ee") + `/File(\EFI\nixos\shimx64.efi)`,
		"bad entry hex": "BootZZZZ* Linux Boot Manager\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`,
		"tab in label":  "Boot0002* x\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)` + "\tPciRoot(0x0)/Pci(0x4,0x0)",
		"no tab":        "Boot0002* Linux Boot Manager " + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := find(t, disks, "BootOrder: 0002\n"+line+"\n"); len(got) != 0 {
				t.Fatalf("matched %v", got)
			}
		})
	}
}

func TestFindNeedsAnESPType(t *testing.T) {
	notESP := strings.Replace(disks, `"partlabel":"EFI","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/vda"`,
		`"partlabel":"EFI","parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4","pkname":"/dev/vda"`, 1)
	line := "Boot0002* Linux Boot Manager\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`
	if got := find(t, notESP, line+"\n"); len(got) != 0 {
		t.Fatalf("matched %v", got)
	}
}

// A cloned disk: the same ESP PARTUUID twice, so the entry could start
// either disk.
func TestFindSkipsDuplicatePartUUID(t *testing.T) {
	clone := strings.Replace(disks, `"`+espB+`"`, `"`+espA+`"`, 1)
	line := "Boot0002* Linux Boot Manager\t" + hd(espA) + `/File(\EFI\nixos\shimx64.efi)`
	if got := find(t, clone, line+"\n"); len(got) != 0 {
		t.Fatalf("matched %v", got)
	}
}

func TestFindNoInstalledDiskSkipsEfibootmgr(t *testing.T) {
	f := &fake{out: map[string]string{lsblkKey: `{"blockdevices":[]}`}}
	got, err := Find(context.Background(), f)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestFindErrors(t *testing.T) {
	f := &fake{err: map[string]error{lsblkKey: errors.New("boom")}}
	if _, err := Find(context.Background(), f); err == nil {
		t.Fatal("lsblk error ignored")
	}
	f = &fake{out: map[string]string{lsblkKey: "not json"}}
	if _, err := Find(context.Background(), f); err == nil {
		t.Fatal("bad json taken")
	}
	f = &fake{out: map[string]string{lsblkKey: disks}, err: map[string]error{"efibootmgr -v": errors.New("no efivars")}}
	if _, err := Find(context.Background(), f); err == nil {
		t.Fatal("efibootmgr error ignored")
	}
	f = &fake{out: map[string]string{lsblkKey: strings.Repeat(" ", maxOutput+1)}}
	if _, err := Find(context.Background(), f); err == nil {
		t.Fatal("oversized lsblk taken")
	}
}

func TestSet(t *testing.T) {
	f := &fake{out: map[string]string{
		"efibootmgr --bootnext 000A": "",
		"efibootmgr":                 "BootNext: 000A\nBootCurrent: 0001\n",
	}}
	if err := Set(context.Background(), f, "000a"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"efibootmgr --bootnext 000A", "efibootmgr"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestSetChecksReadBack(t *testing.T) {
	f := &fake{out: map[string]string{
		"efibootmgr --bootnext 0002": "",
		"efibootmgr":                 "BootNext: 0003\nBootCurrent: 0001\n",
	}}
	if err := Set(context.Background(), f, "0002"); err == nil {
		t.Fatal("wrong BootNext taken")
	}
	f = &fake{out: map[string]string{"efibootmgr": "BootCurrent: 0001\n"}, err: map[string]error{"efibootmgr --bootnext 0002": errors.New("ro")}}
	if err := Set(context.Background(), f, "0002"); err == nil {
		t.Fatal("set error ignored")
	}
}

func TestSetRefusesBadEntry(t *testing.T) {
	f := &fake{}
	for _, e := range []string{"", "2", "00002", "-n 1", "000G"} {
		if err := Set(context.Background(), f, e); err == nil {
			t.Fatalf("took %q", e)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran %v", f.calls)
	}
}

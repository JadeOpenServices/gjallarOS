package bootnext

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// firmware is a fake efibootmgr that keeps its entries, so the read back
// after a delete sees the change.
type firmware struct {
	entries map[string]string // number -> "label\tpath"
	order   []string
	next    string
	calls   []string
	failDel string
}

func (f *firmware) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	if name != "efibootmgr" {
		return nil, errors.New("unexpected call " + key)
	}
	switch {
	case key == "efibootmgr -v":
		var b strings.Builder
		if f.next != "" {
			fmt.Fprintf(&b, "BootNext: %s\n", f.next)
		}
		fmt.Fprintf(&b, "BootCurrent: 0001\nBootOrder: %s\n", strings.Join(f.order, ","))
		for _, n := range []string{"0000", "0001", "0002", "0003", "0004", "0005"} {
			if e, ok := f.entries[n]; ok {
				fmt.Fprintf(&b, "Boot%s%s\n", n, e)
			}
		}
		return []byte(b.String()), nil
	case len(args) == 4 && args[0] == "--quiet" && args[1] == "--bootnum" && args[3] == "--delete-bootnum":
		n := args[2]
		if n == f.failDel {
			return nil, errors.New("efivarfs is read-only")
		}
		if _, ok := f.entries[n]; !ok {
			return nil, errors.New("no entry " + n)
		}
		delete(f.entries, n)
		var order []string
		for _, o := range f.order {
			if o != n {
				order = append(order, o)
			}
		}
		f.order = order
		return nil, nil
	case key == "efibootmgr --quiet --delete-bootnext":
		f.next = ""
		return nil, nil
	}
	return nil, errors.New("unexpected call " + key)
}

const freshESP = "4c1e2b3a-5d6f-4a7b-8c9d-0e1f2a3b4c5d"

// The failed run's ESP has the shim entry (active, first in BootOrder) and
// an inactive systemd-boot entry; the stick, Windows and an older install
// on another disk must stay.
func t480sFirmware() *firmware {
	return &firmware{
		entries: map[string]string{
			"0000": "* Windows Boot Manager\t" + hd(espWin) + `/File(\EFI\Microsoft\Boot\bootmgfw.efi)`,
			"0001": "* USB HDD\t" + hd(espUSB) + `/File(\EFI\BOOT\BOOTX64.EFI)`,
			"0002": "* Linux Boot Manager (shim)\t" + strings.ToUpper(hd(freshESP)) + `/File(\EFI\nixos\shimx64.efi)`,
			"0003": " Linux Boot Manager\t" + hd(freshESP) + `/File(\EFI\systemd\systemd-bootx64.efi)`,
			"0004": "* Linux Boot Manager\t" + hd(espB) + `/File(\EFI\systemd\systemd-bootx64.efi)`,
		},
		order: []string{"0002", "0001", "0000", "0004"},
		next:  "0002",
	}
}

func TestRemoveForESPDeletesOnlyTheFreshESPEntries(t *testing.T) {
	f := t480sFirmware()
	removed, err := RemoveForESP(context.Background(), f, strings.ToUpper(freshESP))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"0002", "0003"}) {
		t.Fatalf("removed %v", removed)
	}
	for _, keep := range []string{"0000", "0001", "0004"} {
		if _, ok := f.entries[keep]; !ok {
			t.Fatalf("entry %s of another system was deleted", keep)
		}
	}
	if !reflect.DeepEqual(f.order, []string{"0001", "0000", "0004"}) {
		t.Fatalf("BootOrder %v", f.order)
	}
	if f.next != "" {
		t.Fatalf("BootNext still %s", f.next)
	}
}

func TestRemoveForESPKeepsForeignBootNext(t *testing.T) {
	f := t480sFirmware()
	f.next = "0004"
	if _, err := RemoveForESP(context.Background(), f, freshESP); err != nil {
		t.Fatal(err)
	}
	if f.next != "0004" {
		t.Fatalf("BootNext of another system was cleared")
	}
	for _, c := range f.calls {
		if strings.Contains(c, "delete-bootnext") {
			t.Fatal("cleared a BootNext that was not ours")
		}
	}
}

func TestRemoveForESPNothingThereWritesNothing(t *testing.T) {
	f := t480sFirmware()
	removed, err := RemoveForESP(context.Background(), f, "00000000-1111-4222-8333-444444444444")
	if err != nil || removed != nil {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"efibootmgr -v"}) {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestRemoveForESPRefusesBadUUID(t *testing.T) {
	for _, u := range []string{"", "x", espA + ",0x800", "../" + espA, espA + "\n"} {
		f := t480sFirmware()
		if _, err := RemoveForESP(context.Background(), f, u); err == nil {
			t.Fatalf("%q accepted", u)
		}
		if len(f.calls) != 0 {
			t.Fatalf("%q ran %v", u, f.calls)
		}
	}
}

func TestRemoveForESPReportsFailedDelete(t *testing.T) {
	f := t480sFirmware()
	f.failDel = "0003"
	removed, err := RemoveForESP(context.Background(), f, freshESP)
	if err == nil || !strings.Contains(err.Error(), "0003") {
		t.Fatalf("err %v", err)
	}
	if !reflect.DeepEqual(removed, []string{"0002"}) {
		t.Fatalf("removed %v", removed)
	}
}

// An entry that only names the PARTUUID in its load options or label, not
// as the start of its device path, is not ours.
func TestRemoveForESPIgnoresUUIDOutsideDevicePath(t *testing.T) {
	f := t480sFirmware()
	f.entries["0005"] = "* " + freshESP + "\t" + hd(espWin) + `/File(\EFI\x.efi)` + freshESP
	if _, err := RemoveForESP(context.Background(), f, freshESP); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.entries["0005"]; !ok {
		t.Fatal("foreign entry with the UUID in its label was deleted")
	}
}

func espLsblk(extra string) string {
	return `{"blockdevices":[
 {"path":"/dev/nvme0n1","type":"disk","partuuid":null,"partlabel":null,"parttype":null,"pkname":null},
 {"path":"/dev/nvme0n1p1","type":"part","partuuid":"` + strings.ToUpper(freshESP) + `","partlabel":"EFI","parttype":"C12A7328-F81F-11D2-BA4B-00A0C93EC93B","pkname":"/dev/nvme0n1"},
 {"path":"/dev/sda1","type":"part","partuuid":"` + espUSB + `","partlabel":"GJALLAR_STICK","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/sda"}` + extra + `
]}`
}

func TestFindESP(t *testing.T) {
	ok := espLsblk("")
	cases := []struct {
		name, lsblk, disk, want string
		fail                    bool
	}{
		{name: "found", lsblk: ok, disk: "/dev/nvme0n1", want: "/dev/nvme0n1p1"},
		{name: "not written yet", lsblk: espLsblk(""), disk: "/dev/nvme0n1", want: ""},
		{name: "other disk", lsblk: ok, disk: "/dev/sda", fail: true},
		{name: "clone", lsblk: espLsblk(`,
 {"path":"/dev/sdb1","type":"part","partuuid":"` + freshESP + `","partlabel":"EFI","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/sdb"}`), disk: "/dev/nvme0n1", fail: true},
		{name: "not an ESP", lsblk: strings.Replace(ok, "C12A7328-F81F-11D2-BA4B-00A0C93EC93B", "0fc63daf-8483-4772-8e79-3d69d8477de4", 1), disk: "/dev/nvme0n1", fail: true},
		{name: "not under /dev", lsblk: strings.Replace(ok, `"/dev/nvme0n1p1"`, `"/mnt/boot/esp"`, 1), disk: "/dev/nvme0n1", fail: true},
		{name: "dot dot", lsblk: strings.Replace(ok, `"/dev/nvme0n1p1"`, `"/dev/../mnt/x"`, 1), disk: "/dev/nvme0n1", fail: true},
		{name: "a mapping", lsblk: strings.Replace(ok, `"type":"part","partuuid":"`+strings.ToUpper(freshESP), `"type":"crypt","partuuid":"`+strings.ToUpper(freshESP), 1), disk: "/dev/nvme0n1", fail: true},
	}
	for _, c := range cases {
		lsblk := c.lsblk
		if c.name == "not written yet" {
			lsblk = strings.Replace(lsblk, strings.ToUpper(freshESP), "11111111-2222-4333-8444-555555555555", 1)
		}
		f := &fake{out: map[string]string{lsblkKey: lsblk}}
		got, err := FindESP(context.Background(), f, c.disk, freshESP)
		if c.fail {
			if err == nil {
				t.Fatalf("%s: accepted %q", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("%s: got %q, %v", c.name, got, err)
		}
	}
}

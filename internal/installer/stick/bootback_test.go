package stick

import (
	"strings"
	"testing"
)

// An installed gjallarOS on vda next to the stick on sda (Q228 item 4).
const (
	installedDisks = `{"blockdevices":[
 {"path":"/dev/vda1","type":"part","partuuid":"9f6bc3da-7244-4fce-8759-6a3033d20cea","partlabel":"EFI","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/vda"},
 {"path":"/dev/vda2","type":"part","partuuid":"11111111-0000-4000-8000-000000000001","partlabel":"GJALLAROS","parttype":"ca7d7ccb-63ed-4c53-861c-1742536059cc","pkname":"/dev/vda"},
 {"path":"/dev/sda1","type":"part","partuuid":"aaaaaaaa-0000-4000-8000-0000000000cc","partlabel":"GJALLAR_STICK","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b","pkname":"/dev/sda"}
]}`
	installedBoot = "BootCurrent: 0001\nBootOrder: 0001,0002\n" +
		"Boot0001* UEFI USB\tHD(1,GPT,aaaaaaaa-0000-4000-8000-0000000000cc,0x800,0x100000)/File(\\EFI\\BOOT\\BOOTX64.EFI)\n" +
		"Boot0002* Linux Boot Manager\tHD(1,GPT,9f6bc3da-7244-4fce-8759-6a3033d20cea,0x800,0x100000)/File(\\EFI\\systemd\\systemd-bootx64.efi)\n"
)

func installedNet() *fakeNet {
	return &fakeNet{devices: always(cable), connectivity: "full", disks: installedDisks, boot: installedBoot}
}

func TestMenuWithInstalledSystem(t *testing.T) {
	h := newHarness(t, ModeWindow, "", installedNet())
	h.run(t)
	out := plain(h.screen.String())
	last := -1
	for _, item := range []string{" 1  \nStart installed gjallarOS", "Leave the installer and start it", " 2  \nInstall gjallarOS",
		" 3  \nRecovery", " 4  \nShell", " 5  \nNetwork", " 6  \nKeyboard layout", " 7  \nAdvanced", " 8  \nRestart", " 9  \nPower off"} {
		i := strings.Index(out, item)
		if i <= last {
			t.Fatalf("%q missing or out of order:\n%s", item, out)
		}
		last = i
	}
}

func TestMenuWithoutInstalledSystem(t *testing.T) {
	net := &fakeNet{devices: always(cable), connectivity: "full", disks: installedDisks,
		boot: "BootOrder: 0001\nBoot0001* UEFI USB\tPciRoot(0x0)/Pci(0x4,0x0)\n"}
	h := newHarness(t, ModeWindow, "", net)
	h.run(t)
	if out := plain(h.screen.String()); strings.Contains(out, "Start installed") || !strings.Contains(out, " 1  \nInstall gjallarOS") {
		t.Errorf("menu:\n%s", out)
	}
}

func TestStartInstalledSetsBootNextAndRestarts(t *testing.T) {
	net := installedNet()
	h := newHarness(t, ModeWindow, "1\n", net)
	h.run(t)
	if net.bootNext != "0002" {
		t.Fatalf("BootNext %q, calls %q", net.bootNext, net.calls)
	}
	if r := net.ran(""); len(r) != 1 || r[0] != "systemctl reboot" {
		t.Errorf("ran %q", r)
	}
	if log := h.logText(t); !strings.Contains(log, "GJA-2050 ") || !strings.Contains(log, "result=set") {
		t.Errorf("no anchor:\n%s", log)
	}
}

// The install is looked up again on Start: a disk pulled after the menu was
// drawn sets nothing.
func TestStartInstalledLooksAgain(t *testing.T) {
	net := installedNet()
	net.disksGone = 2
	h := newHarness(t, ModeWindow, "1\n", net)
	h.run(t)
	if net.bootNext != "" || len(net.ran("")) != 0 {
		t.Fatalf("BootNext %q, ran %q", net.bootNext, net.ran(""))
	}
	if !strings.Contains(h.screen.String(), "No installed gjallarOS was found anymore.") {
		t.Errorf("no note:\n%s", plain(h.screen.String()))
	}
	if !strings.Contains(h.logText(t), "result=none") {
		t.Errorf("log:\n%s", h.logText(t))
	}
}

func TestStartInstalledSetFails(t *testing.T) {
	net := installedNet()
	net.setFails = true
	h := newHarness(t, ModeWindow, "1\n", net)
	h.run(t)
	if len(net.ran("")) != 0 {
		t.Fatalf("restarted although BootNext failed: %q", net.ran(""))
	}
	if !strings.Contains(h.screen.String(), "The next start could not be set. Use the firmware's boot menu.") {
		t.Errorf("no note:\n%s", plain(h.screen.String()))
	}
	log := h.logText(t)
	if !strings.Contains(log, "result=failed") || !strings.Contains(log, "ERROR: could not set the next start to the installed gjallarOS") {
		t.Errorf("log:\n%s", log)
	}
}

func TestUnknownChoiceNoteCountsStart(t *testing.T) {
	h := newHarness(t, ModeWindow, "10\n", installedNet())
	h.run(t)
	if !strings.Contains(h.screen.String(), "Type one of the numbers 1 to 9.") {
		t.Errorf("no note:\n%s", plain(h.screen.String()))
	}
}

// A failed look shows the menu without Start installed gjallarOS and logs
// one fixed line, however often the menu is drawn.
func TestLookFailureLogsOnce(t *testing.T) {
	net := installedNet()
	net.disks = "not json"
	h := newHarness(t, ModeWindow, "x\ny\n", net)
	h.run(t)
	if strings.Contains(h.screen.String(), "Start installed") {
		t.Error("Start shown after a failed look")
	}
	if n := strings.Count(h.logText(t), "could not look for an installed gjallarOS"); n != 1 {
		t.Errorf("logged %d times:\n%s", n, h.logText(t))
	}
}

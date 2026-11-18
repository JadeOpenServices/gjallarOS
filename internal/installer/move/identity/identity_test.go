package identity

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// Bluetooth addresses from the documentation range of RFC 7042
// (00-00-5E-00-53-00 to 00-00-5E-00-53-FF).
const (
	hereAdapter  = "00:00:5E:00:53:01"
	otherAdapter = "00:00:5E:00:53:02"
	device1      = "00:00:5E:00:53:A1"
	device2      = "00:00:5E:00:53:A2"
	device3      = "00:00:5E:00:53:A3"
)

// EXAMPLE machine-id in the machine-id(5) format.
const exampleMachineID = "0123456789abcdef0123456789abcdef\n"

func asRoot(t *testing.T) {
	t.Helper()
	old := rootUID
	rootUID = uint32(os.Geteuid())
	t.Cleanup(func() { rootUID = old })
}

func put(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// A read-only file from oldDisk is replaced, not opened.
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// oldDisk lays out a top level with one of every item.
func oldDisk(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	nm := filepath.Join(top, nmDir)
	for _, f := range []string{"companywifi", "personal-wifi", "redhat-openvpn", "wireguard", "main-eth0"} {
		put(t, filepath.Join(nm, f+".nmconnection"), fixture(t, f+".nmconnection"), 0o600)
	}
	bt := filepath.Join(top, bluezDir)
	put(t, filepath.Join(bt, hereAdapter, "settings"), "[General]\n", 0o600)
	put(t, filepath.Join(bt, hereAdapter, device1, "info"), "[General]\n", 0o600)
	put(t, filepath.Join(bt, hereAdapter, device2, "info"), "[General]\n", 0o600)
	if err := os.MkdirAll(filepath.Join(bt, hereAdapter, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(bt, otherAdapter, device3, "info"), "[General]\n", 0o600)
	fp := filepath.Join(top, fprintDir, "example", "goodixmoc", "0")
	put(t, filepath.Join(fp, "7"), "template", 0o600)
	put(t, filepath.Join(fp, "a"), "template", 0o600)
	put(t, filepath.Join(top, tailscaleRel), "{}", 0o600)
	put(t, filepath.Join(top, sshHostKeys[2]), "key", 0o600)
	put(t, filepath.Join(top, sshHostKeys[0]), "key", 0o600)
	put(t, filepath.Join(top, machineIDRel), exampleMachineID, 0o444)
	put(t, filepath.Join(top, homeDir, "example", nextcloudRel), "[General]\n", 0o600)
	return top
}

func scan(t *testing.T, top, user string) Inventory {
	t.Helper()
	d, err := safefs.OpenDir(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var inv Inventory
	done := make(chan struct{})
	go func() { inv = Scan(d, user, map[string]bool{hereAdapter: true}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scan hung")
	}
	return inv
}

func TestProfileKind(t *testing.T) {
	for name, want := range map[string]kind{
		"companywifi":    kindWiFiEnterprise,
		"personal-wifi":  kindWiFi,
		"redhat-openvpn": kindVPN,
		"wireguard":      kindVPN,
		"main-eth0":      kindOther,
	} {
		if got := profileKind([]byte(fixture(t, name+".nmconnection"))); got != want {
			t.Errorf("%s: kind %d, want %d", name, got, want)
		}
	}
	for name, raw := range map[string]string{
		"a line that is no key": "[connection]\ntype=wifi\nnot a key\n",
		"key before a group":    "type=wifi\n[connection]\n",
		"two types":             "[connection]\ntype=wifi\n[connection]\ntype=vpn\n",
		"no type":               "[connection]\nid=x\n[wifi]\n",
		"broken group":          "[connection\ntype=wifi\n",
		"NUL byte":              "[connection]\ntype=wifi\x00\n",
		"not UTF-8":             "[connection]\ntype=wifi\n# \xff\n",
	} {
		if got := profileKind([]byte(raw)); got != kindBad {
			t.Errorf("%s: kind %d", name, got)
		}
	}
}

func TestScan(t *testing.T) {
	asRoot(t)
	inv := scan(t, oldDisk(t), "example")
	want := Inventory{WiFi: 1, WiFiEnterprise: 1, VPN: 2, Bluetooth: 2, BluetoothOther: 1, Fingerprints: 2, Tailscale: true, SSHHostKeys: 2, MachineID: true, Nextcloud: true}
	if inv != want {
		t.Fatalf("inventory\n %+v\nwant\n %+v", inv, want)
	}
}

func TestScanEmptyDisk(t *testing.T) {
	if inv := scan(t, t.TempDir(), "example"); inv != (Inventory{}) {
		t.Fatalf("inventory %+v", inv)
	}
}

func TestScanBadUsername(t *testing.T) {
	asRoot(t)
	inv := scan(t, oldDisk(t), "../example")
	if inv.Fingerprints != 0 || inv.Nextcloud || inv.Skipped != 1 {
		t.Fatalf("inventory %+v", inv)
	}
}

// Links, pipes and odd files at every place: nothing is followed or opened
// for reading, nothing hangs, and each is counted as skipped.
func TestScanRefusesPlanted(t *testing.T) {
	asRoot(t)
	victim := t.TempDir()
	put(t, filepath.Join(victim, "a.nmconnection"), fixture(t, "personal-wifi.nmconnection"), 0o600)
	put(t, filepath.Join(victim, "7"), "template", 0o600)
	link := func(t *testing.T, target, at string) {
		t.Helper()
		os.RemoveAll(at)
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	fifo := func(t *testing.T, at string) {
		t.Helper()
		os.RemoveAll(at)
		if err := unix.Mkfifo(at, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]struct {
		plant func(t *testing.T, top string)
		check func(Inventory) bool
	}{
		"profile folder is a link": {
			plant: func(t *testing.T, top string) { link(t, victim, filepath.Join(top, nmDir)) },
			check: func(i Inventory) bool { return i.WiFi+i.WiFiEnterprise+i.VPN == 0 },
		},
		"etc is a link": {
			plant: func(t *testing.T, top string) {
				os.Rename(filepath.Join(top, "@/etc"), filepath.Join(top, "@/real-etc"))
				link(t, "real-etc", filepath.Join(top, "@/etc"))
			},
			check: func(i Inventory) bool { return i.WiFi+i.VPN+i.SSHHostKeys == 0 && !i.MachineID },
		},
		"profile is a link": {
			plant: func(t *testing.T, top string) {
				link(t, filepath.Join(victim, "a.nmconnection"), filepath.Join(top, nmDir, "personal-wifi.nmconnection"))
			},
			check: func(i Inventory) bool { return i.WiFi == 0 },
		},
		"profile is a pipe": {
			plant: func(t *testing.T, top string) { fifo(t, filepath.Join(top, nmDir, "personal-wifi.nmconnection")) },
			check: func(i Inventory) bool { return i.WiFi == 0 },
		},
		"profile others can read": {
			plant: func(t *testing.T, top string) {
				os.Chmod(filepath.Join(top, nmDir, "personal-wifi.nmconnection"), 0o644)
			},
			check: func(i Inventory) bool { return i.WiFi == 0 },
		},
		"profile over the cap": {
			plant: func(t *testing.T, top string) {
				put(t, filepath.Join(top, nmDir, "personal-wifi.nmconnection"), fixture(t, "personal-wifi.nmconnection")+string(make([]byte, maxProfile)), 0o600)
			},
			check: func(i Inventory) bool { return i.WiFi == 0 },
		},
		"profile without suffix": {
			plant: func(t *testing.T, top string) {
				os.Rename(filepath.Join(top, nmDir, "personal-wifi.nmconnection"), filepath.Join(top, nmDir, "personal-wifi"))
			},
			check: func(i Inventory) bool { return i.WiFi == 0 },
		},
		"adapter is a link": {
			plant: func(t *testing.T, top string) {
				os.Rename(filepath.Join(top, bluezDir, hereAdapter), filepath.Join(top, bluezDir, "real"))
				link(t, "real", filepath.Join(top, bluezDir, hereAdapter))
			},
			check: func(i Inventory) bool { return i.Bluetooth == 0 && i.BluetoothOther == 1 },
		},
		"pairing is a pipe": {
			plant: func(t *testing.T, top string) { fifo(t, filepath.Join(top, bluezDir, hereAdapter, device1, "info")) },
			check: func(i Inventory) bool { return i.Bluetooth == 1 },
		},
		"fingerprint account is a link": {
			plant: func(t *testing.T, top string) { link(t, victim, filepath.Join(top, fprintDir, "example")) },
			check: func(i Inventory) bool { return i.Fingerprints == 0 },
		},
		"finger is a pipe": {
			plant: func(t *testing.T, top string) { fifo(t, filepath.Join(top, fprintDir, "example/goodixmoc/0/7")) },
			check: func(i Inventory) bool { return i.Fingerprints == 1 },
		},
		"finger file with another name": {
			plant: func(t *testing.T, top string) {
				os.Rename(filepath.Join(top, fprintDir, "example/goodixmoc/0/7"), filepath.Join(top, fprintDir, "example/goodixmoc/0/b"))
			},
			check: func(i Inventory) bool { return i.Fingerprints == 1 },
		},
		"tailscale state is a link": {
			plant: func(t *testing.T, top string) { link(t, filepath.Join(victim, "7"), filepath.Join(top, tailscaleRel)) },
			check: func(i Inventory) bool { return !i.Tailscale },
		},
		"host key is a pipe": {
			plant: func(t *testing.T, top string) { fifo(t, filepath.Join(top, sshHostKeys[2])) },
			check: func(i Inventory) bool { return i.SSHHostKeys == 1 },
		},
		"machine-id is a pipe": {
			plant: func(t *testing.T, top string) { fifo(t, filepath.Join(top, machineIDRel)) },
			check: func(i Inventory) bool { return !i.MachineID },
		},
		"machine-id is all zero": {
			plant: func(t *testing.T, top string) {
				put(t, filepath.Join(top, machineIDRel), "00000000000000000000000000000000\n", 0o444)
			},
			check: func(i Inventory) bool { return !i.MachineID },
		},
		"machine-id in capitals": {
			plant: func(t *testing.T, top string) {
				put(t, filepath.Join(top, machineIDRel), "0123456789ABCDEF0123456789ABCDEF\n", 0o444)
			},
			check: func(i Inventory) bool { return !i.MachineID },
		},
		"home is a link": {
			plant: func(t *testing.T, top string) {
				os.Rename(filepath.Join(top, homeDir, "example"), filepath.Join(top, homeDir, "real"))
				link(t, "real", filepath.Join(top, homeDir, "example"))
			},
			check: func(i Inventory) bool { return !i.Nextcloud },
		},
		"nextcloud config is a pipe": {
			plant: func(t *testing.T, top string) { fifo(t, filepath.Join(top, homeDir, "example", nextcloudRel)) },
			check: func(i Inventory) bool { return !i.Nextcloud },
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			top := oldDisk(t)
			c.plant(t, top)
			inv := scan(t, top, "example")
			if !c.check(inv) || inv.Skipped == 0 {
				t.Fatalf("inventory %+v", inv)
			}
		})
	}
}

func TestScanFolderCap(t *testing.T) {
	asRoot(t)
	top := oldDisk(t)
	dir := filepath.Join(top, fprintDir, "example", "goodixmoc", "0")
	for i := range maxFingers + 1 {
		put(t, filepath.Join(dir, "x"+string(rune('A'+i%26))+string(rune('a'+i/26))), "", 0o600)
	}
	if inv := scan(t, top, "example"); inv.Fingerprints != 0 || inv.Skipped != 1 {
		t.Fatalf("inventory %+v", inv)
	}
}

func TestOffer(t *testing.T) {
	inv := Inventory{WiFi: 3, VPN: 1, Bluetooth: 2, BluetoothOther: 4, Fingerprints: 2, Tailscale: true, SSHHostKeys: 3, MachineID: true, Skipped: 5}
	keep := []Choice{
		{Item: WiFi, Count: 3, Keep: true},
		{Item: VPN, Count: 1, Keep: true, Risk: true},
		{Item: Bluetooth, Count: 2, Keep: true},
		{Item: Tailscale, Count: 1, Keep: true, Risk: true},
		{Item: SSHHostKeys, Count: 3, Keep: true, Risk: true},
		{Item: MachineID, Count: 1, Keep: true, Risk: true},
	}
	if got := Offer(inv, Options{}); !reflect.DeepEqual(got, keep) {
		t.Errorf("new computer:\n %+v", got)
	}
	got := Offer(inv, Options{SameComputer: true})
	if len(got) != 7 || got[3] != (Choice{Item: Fingerprints, Count: 2, Keep: true}) {
		t.Errorf("same computer:\n %+v", got)
	}
	for _, c := range Offer(inv, Options{PINUnchecked: true}) {
		if c.Keep == c.Risk {
			t.Errorf("PIN not checked: %+v", c)
		}
	}
	if got := Offer(Inventory{BluetoothOther: 2, Skipped: 1}, Options{SameComputer: true}); len(got) != 0 {
		t.Errorf("nothing to offer: %+v", got)
	}
}

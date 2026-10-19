package mediagate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeProbe map[string]Medium

func (p fakeProbe) Medium(id string) (Medium, error) { return p[id], nil }
func (p fakeProbe) Capacity(string) (uint64, error)  { return 32 << 30, nil }

type fakeKernel struct {
	t         *testing.T
	root      string
	autoprobe string
	writes    []string
}

// add creates a SCSI LUN. usb=true puts it behind USB device 1-1.
func (k *fakeKernel) add(id string, usb, removable bool, typ int, devnum string) string {
	k.t.Helper()
	parent := filepath.Join(k.root, "devices/pci0000:00/ata1")
	if usb {
		dev := filepath.Join(k.root, "devices/pci0000:00/usb1/1-1")
		parent = filepath.Join(dev, "1-1:1.0")
		mustWrite(k.t, filepath.Join(dev, "idVendor"), "090c")
		mustWrite(k.t, filepath.Join(dev, "idProduct"), "1000")
		mustWrite(k.t, filepath.Join(dev, "product"), "USB DISK")
		mustWrite(k.t, filepath.Join(dev, "devnum"), devnum)
		mustWrite(k.t, filepath.Join(parent, "bInterfaceClass"), "08")
	}
	h := strings.SplitN(id, ":", 2)[0]
	real := filepath.Join(parent, "host"+h, "target"+strings.Join(strings.Split(id, ":")[:3], ":"), id)
	mustWrite(k.t, filepath.Join(real, "type"), string(rune('0'+typ)))
	inquiry := []byte{byte(typ), 0}
	if removable {
		inquiry[1] = 0x80
	}
	if err := os.WriteFile(filepath.Join(real, "inquiry"), inquiry, 0o644); err != nil {
		k.t.Fatal(err)
	}
	link := filepath.Join(k.root, "bus/scsi/devices", id)
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Remove(link)
	if err := os.Symlink(real, link); err != nil {
		k.t.Fatal(err)
	}
	return real
}

func (k *fakeKernel) bound(id string) bool {
	_, err := os.Lstat(filepath.Join(k.root, "bus/scsi/devices", id, "driver"))
	return err == nil
}

func (k *fakeKernel) setBound(id string, on bool) {
	link := filepath.Join(k.root, "bus/scsi/devices", id, "driver")
	os.Remove(link)
	if on {
		if err := os.Symlink(filepath.Join(k.root, "bus/scsi/drivers/sd"), link); err != nil {
			k.t.Fatal(err)
		}
	}
}

func (k *fakeKernel) write(rel, value string) error {
	k.writes = append(k.writes, rel+"="+value)
	switch {
	case rel == "bus/scsi/drivers_autoprobe":
		k.autoprobe = value
	case rel == "bus/scsi/drivers_probe":
		k.setBound(value, true)
	case strings.HasSuffix(rel, "/unbind"):
		k.setBound(value, false)
	}
	return nil
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newGate(t *testing.T) (*Gate, *fakeKernel, fakeProbe) {
	root := t.TempDir()
	k := &fakeKernel{t: t, root: root}
	probe := fakeProbe{}
	mountinfo := filepath.Join(root, "mountinfo")
	mustWrite(t, mountinfo, "")
	g := &Gate{Sysfs: root, MountInfo: mountinfo, Probe: probe, writeFile: k.write}
	return g, k, probe
}

func only(t *testing.T, g *Gate) (id, token string, allowed bool) {
	t.Helper()
	media, listed := g.MediaPolicy()
	if !listed || len(media) != 1 {
		t.Fatalf("media = %+v, want one", media)
	}
	return media[0].ID, media[0].Connection, media[0].Allowed
}

func TestCardStaysClosedUntilAllowed(t *testing.T) {
	g, k, probe := newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	probe["6:0:0:0"] = MediumPresent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if k.autoprobe != "0" {
		t.Fatalf("autoprobe = %q, want 0 while enforcing", k.autoprobe)
	}
	if k.bound("6:0:0:0") {
		t.Fatal("card was bound before the owner allowed it")
	}
	id, token, allowed := only(t, g)
	if allowed || token == "" {
		t.Fatalf("allowed=%v token=%q", allowed, token)
	}
	if err := g.AllowMedia(id, "wrong"); err == nil {
		t.Fatal("allowed with a stale token")
	}
	if err := g.AllowMedia(id, token); err != nil {
		t.Fatal(err)
	}
	if !k.bound("6:0:0:0") {
		t.Fatal("allowed card is not bound")
	}
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if !k.bound("6:0:0:0") {
		t.Fatal("next poll closed an allowed card")
	}

	// Pull the card and put one back: the new card asks again.
	probe["6:0:0:0"] = MediumAbsent
	g.Sync(true)
	if k.bound("6:0:0:0") {
		t.Fatal("empty reader stayed bound")
	}
	if media, _ := g.MediaPolicy(); len(media) != 0 {
		t.Fatal("removed card still listed")
	}
	probe["6:0:0:0"] = MediumPresent
	g.Sync(true)
	_, newToken, allowed := only(t, g)
	if allowed || k.bound("6:0:0:0") {
		t.Fatal("reinserted card opened without approval")
	}
	if newToken == token {
		t.Fatal("reinserted card reused the old token")
	}
	if err := g.AllowMedia(id, token); err == nil {
		t.Fatal("old approval opened the new card")
	}
}

// A broker that has not scanned yet must not report "no media": the review
// would forget which cards the owner kept closed.
func TestMediaListedOnlyAfterScan(t *testing.T) {
	g, k, probe := newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	probe["6:0:0:0"] = MediumPresent
	if _, listed := g.MediaPolicy(); listed {
		t.Fatal("media listed before the first scan")
	}
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	only(t, g)
	if err := g.Sync(false); err != nil {
		t.Fatal(err)
	}
	if _, listed := g.MediaPolicy(); !listed {
		t.Fatal("audit-mode scan left media unlisted")
	}
}

func TestSwapRevokes(t *testing.T) {
	g, k, probe := newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	probe["6:0:0:0"] = MediumPresent
	g.Sync(true)
	id, token, _ := only(t, g)
	if err := g.AllowMedia(id, token); err != nil {
		t.Fatal(err)
	}
	probe["6:0:0:0"] = MediumChanged
	g.Sync(true)
	if k.bound("6:0:0:0") {
		t.Fatal("swapped card kept the old approval")
	}
	probe["6:0:0:0"] = MediumPresent
	g.Sync(true)
	if _, _, allowed := only(t, g); allowed || k.bound("6:0:0:0") {
		t.Fatal("swapped card opened")
	}
}

// sd can take the unit attention before the gate polls; the kernel then
// raises diskseq and the gate must still close the new card.
func TestSwapSeenOnlyBySD(t *testing.T) {
	g, k, probe := newGate(t)
	real := k.add("6:0:0:0", true, true, 0, "5")
	probe["6:0:0:0"] = MediumPresent
	g.Sync(true)
	id, token, _ := only(t, g)
	if err := g.AllowMedia(id, token); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(real, "block/sdb/diskseq"), "14")
	g.Sync(true)
	if !k.bound("6:0:0:0") {
		t.Fatal("allowed card closed without a change")
	}
	mustWrite(t, filepath.Join(real, "block/sdb/diskseq"), "15")
	g.Sync(true)
	if k.bound("6:0:0:0") {
		t.Fatal("card swapped behind sd stayed open")
	}
	g.Sync(true)
	if _, _, allowed := only(t, g); allowed || k.bound("6:0:0:0") {
		t.Fatal("swapped card opened")
	}
}

func TestReplugResetsApproval(t *testing.T) {
	g, k, probe := newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	probe["6:0:0:0"] = MediumPresent
	g.Sync(true)
	id, token, _ := only(t, g)
	g.AllowMedia(id, token)
	// Same LUN name, new USB connection.
	k.setBound("6:0:0:0", false)
	k.add("6:0:0:0", true, true, 0, "9")
	g.Sync(true)
	if _, _, allowed := only(t, g); allowed || k.bound("6:0:0:0") {
		t.Fatal("approval survived a replug")
	}
}

func TestArmingKeepsMountedMediaAndClosesTheRest(t *testing.T) {
	g, k, probe := newGate(t)
	mounted := k.add("6:0:0:0", true, true, 0, "5")
	mustWrite(t, filepath.Join(mounted, "block/sdb/dev"), "8:16")
	mustWrite(t, filepath.Join(mounted, "block/sdb/sdb1/dev"), "8:17")
	mustWrite(t, g.MountInfo, "40 1 8:17 / /run/media/user/card rw - vfat /dev/sdb1 rw")
	k.setBound("6:0:0:0", true)
	k.add("7:0:0:0", true, true, 0, "5")
	k.setBound("7:0:0:0", true)
	probe["6:0:0:0"] = MediumPresent
	probe["7:0:0:0"] = MediumPresent
	g.Kept = func() map[string]bool { return map[string]bool{"1-1": true} }

	g.Sync(true)
	if !k.bound("6:0:0:0") {
		t.Fatal("arming pulled a mounted card")
	}
	if k.bound("7:0:0:0") {
		t.Fatal("arming left an unmounted, unapproved card open")
	}
	// Later polls do not grandfather anything new.
	k.setBound("7:0:0:0", true)
	g.Sync(true)
	if k.bound("7:0:0:0") {
		t.Fatal("card bound behind the gate's back stayed open")
	}
}

func TestRestartKeepsNothingOpen(t *testing.T) {
	g, k, probe := newGate(t)
	mounted := k.add("6:0:0:0", true, true, 0, "5")
	mustWrite(t, filepath.Join(mounted, "block/sdb/dev"), "8:16")
	mustWrite(t, filepath.Join(mounted, "block/sdb/sdb1/dev"), "8:17")
	mustWrite(t, g.MountInfo, "40 1 8:17 / /run/media/user/card rw - vfat /dev/sdb1 rw")
	k.setBound("6:0:0:0", true)
	probe["6:0:0:0"] = MediumPresent
	if ports := g.MountedPorts(); len(ports) != 1 || !ports["1-1"] {
		t.Fatalf("mounted ports: %v", ports)
	}
	var events []string
	g.Event = func(e map[string]any) { events = append(events, e["change"].(string)) }
	// No arming in this process: Kept is nil, so the mounted card closes.
	g.Kept = func() map[string]bool { return nil }
	g.Sync(true)
	if k.bound("6:0:0:0") {
		t.Fatal("restart left a mounted, unapproved card open")
	}
	for _, e := range events {
		if e == "kept-open" {
			t.Fatal("restart logged kept-open")
		}
	}
}

func TestUnknownProbeFailsClosed(t *testing.T) {
	g, k, probe := newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	k.setBound("6:0:0:0", true)
	probe["6:0:0:0"] = MediumUnknown
	g.Sync(true)
	if k.bound("6:0:0:0") {
		t.Fatal("unanswered probe left the LUN open")
	}
}

func TestFixedAndNonUSBDisksBind(t *testing.T) {
	g, k, _ := newGate(t)
	k.add("0:0:0:0", false, false, 0, "")
	k.add("6:0:0:0", true, false, 0, "5") // USB SSD: no medium to swap
	g.Sync(true)
	if k.bound("0:0:0:0") {
		t.Fatal("gate touched a non-USB disk")
	}
	if !k.bound("6:0:0:0") {
		t.Fatal("fixed USB disk was not bound")
	}
	if media, _ := g.MediaPolicy(); len(media) != 0 {
		t.Fatal("fixed disk listed as a medium")
	}
}

func TestAuditModeOpensEverything(t *testing.T) {
	g, k, probe := newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	probe["6:0:0:0"] = MediumPresent
	g.Sync(true)
	if k.bound("6:0:0:0") {
		t.Fatal("bound while enforcing")
	}
	g.Sync(false)
	if k.autoprobe != "1" || !k.bound("6:0:0:0") {
		t.Fatalf("audit mode: autoprobe=%q bound=%v", k.autoprobe, k.bound("6:0:0:0"))
	}
	if err := g.AllowMedia("6:0:0:0", "x"); err == nil {
		t.Fatal("allow-media succeeded with the gate off")
	}
}

func TestParseSense(t *testing.T) {
	fixed := make([]byte, 18)
	fixed[0], fixed[2], fixed[12], fixed[13] = 0x70, 0x02, 0x3a, 0x00
	if got := classify(scsiResult{status: statusCheck, key: 2, asc: 0x3a}); got != MediumAbsent {
		t.Fatalf("classify absent = %v", got)
	}
	key, asc, ascq := parseSense(fixed)
	if key != 2 || asc != 0x3a || ascq != 0 {
		t.Fatalf("fixed sense = %x %x %x", key, asc, ascq)
	}
	desc := []byte{0x72, 0x06, 0x28, 0x00}
	key, asc, _ = parseSense(desc)
	if classify(scsiResult{status: statusCheck, key: key, asc: asc}) != MediumChanged {
		t.Fatal("descriptor unit attention not seen as a change")
	}
	if classify(scsiResult{status: statusGood}) != MediumPresent {
		t.Fatal("GOOD is not present")
	}
	if classify(scsiResult{status: statusCheck, key: 2, asc: 0x04}) != MediumUnknown {
		t.Fatal("becoming ready must wait")
	}
}

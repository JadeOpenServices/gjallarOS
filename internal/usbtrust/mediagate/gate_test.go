package mediagate

import (
	"os"
	"path/filepath"
	"strconv"
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
	mustWrite(k.t, filepath.Join(real, "type"), strconv.Itoa(typ))
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

// Plane 289: a USB disk that reports a fixed medium (an SSD, a disk in a
// dock, or a stick that clears its removable bit) waits for the owner like a
// card. Disks that are not behind USB stay the kernel's.
func TestFixedUSBDiskStaysClosedUntilAllowed(t *testing.T) {
	g, k, probe := newGate(t)
	k.add("0:0:0:0", false, false, 0, "")
	real := k.add("6:0:0:0", true, false, 0, "5")
	mustWrite(t, filepath.Join(real, "vendor"), "SYNTHETIC ")
	mustWrite(t, filepath.Join(real, "model"), "EXAMPLE SSD 1TB  ")
	k.setBound("6:0:0:0", true)
	probe["6:0:0:0"] = MediumPresent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if k.bound("0:0:0:0") {
		t.Fatal("gate touched a non-USB disk")
	}
	if k.bound("6:0:0:0") {
		t.Fatal("fixed USB disk opened without the owner")
	}
	media, _ := g.MediaPolicy()
	if len(media) != 1 || media[0].Kind != "disk" || media[0].Disk != "SYNTHETIC EXAMPLE SSD 1TB" || media[0].Reader != "USB DISK" {
		t.Fatalf("media = %+v", media)
	}
	if err := g.AllowMedia("6:0:0:0", media[0].Connection); err != nil {
		t.Fatal(err)
	}
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if !k.bound("6:0:0:0") {
		t.Fatal("allowed fixed disk not opened")
	}
}

// Every type sd or sr would bind is gated; others (an enclosure here) are
// not storage and bind as before.
func TestGatedTypes(t *testing.T) {
	for typ, want := range map[int]bool{0: true, 4: true, 5: true, 7: true, 14: true, 20: true, 1: false, 13: false} {
		if got := (LUN{Type: typ}).Gated(); got != want {
			t.Errorf("type %d gated = %v, want %v", typ, got, want)
		}
	}
	g, k, _ := newGate(t)
	k.add("6:0:0:0", true, false, 13, "5")
	g.Sync(true)
	if !k.bound("6:0:0:0") {
		t.Fatal("enclosure LUN not bound")
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
	if classify(scsiResult{status: statusCheck, key: 2, asc: 0x04, asq: 0x01}) != MediumUnknown {
		t.Fatal("becoming ready must wait")
	}
	if classify(scsiResult{status: statusCheck, key: 2, asc: 0x04, asq: 0x02}) != MediumPresent {
		t.Fatal("a stopped disk must be offered, sd starts it on open")
	}
}

func TestCapacity16(t *testing.T) {
	data := make([]byte, 32)
	// SYNTHETIC: 4 TB in 512-byte blocks, last LBA 7814037167.
	data[3], data[4], data[5], data[6], data[7] = 0x01, 0xd1, 0xc0, 0xbe, 0xaf
	data[10] = 0x02
	if got := capacity16(data); got != 7814037168*512 {
		t.Fatalf("capacity16 = %d", got)
	}
	for i := range 8 {
		data[i] = 0xff
	}
	if got := capacity16(data); got != 0 {
		t.Fatalf("overflowing capacity = %d, want unknown", got)
	}
}

func kindOf(t *testing.T, g *Gate, id string) string {
	t.Helper()
	media, _ := g.MediaPolicy()
	for _, m := range media {
		if m.ID == id {
			return m.Kind
		}
	}
	t.Fatalf("no medium in %s: %+v", id, media)
	return ""
}

func TestMediaKind(t *testing.T) {
	g, k, probe := newGate(t)
	real := k.add("6:0:0:0", true, true, 0, "5")
	mustWrite(t, filepath.Join(real, "model"), "SYNTHETIC SD/MMC")
	probe["6:0:0:0"] = MediumPresent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if got := kindOf(t, g, "6:0:0:0"); got != "sd-card" {
		t.Fatalf("SD/MMC slot kind = %q", got)
	}

	// A stick names nothing and never reports an empty slot.
	g, k, probe = newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	probe["6:0:0:0"] = MediumPresent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if got := kindOf(t, g, "6:0:0:0"); got != "" {
		t.Fatalf("stick kind = %q, want plain USB storage", got)
	}

	// A slot that was empty since the plug is a card reader.
	probe["6:0:0:0"] = MediumAbsent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	probe["6:0:0:0"] = MediumPresent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if got := kindOf(t, g, "6:0:0:0"); got != "card" {
		t.Fatalf("slot seen empty: kind = %q, want card", got)
	}

	// Each slot of a multi-slot reader goes by its own model; the shared
	// product name says nothing about one slot.
	g, k, probe = newGate(t)
	for id, model := range map[string]string{"6:0:0:0": "SD", "6:0:0:1": "MS", "6:0:0:2": "SYNTHETIC"} {
		mustWrite(t, filepath.Join(k.add(id, true, true, 0, "5"), "model"), model)
		probe[id] = MediumPresent
	}
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"6:0:0:0": "sd-card", "6:0:0:1": "memory-stick", "6:0:0:2": "card"} {
		if got := kindOf(t, g, id); got != want {
			t.Errorf("%s kind = %q, want %q", id, got, want)
		}
	}

	g, k, probe = newGate(t)
	k.add("6:0:0:0", true, true, 5, "5")
	probe["6:0:0:0"] = MediumPresent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	if got := kindOf(t, g, "6:0:0:0"); got != "disc" {
		t.Fatalf("optical kind = %q", got)
	}
}

func TestDiskKind(t *testing.T) {
	_, k, _ := newGate(t)
	for id, model := range map[string]string{"6:0:0:0": "SD/MMC", "6:0:0:1": "SYNTHETIC"} {
		mustWrite(t, filepath.Join(k.add(id, true, true, 0, "5"), "model"), model)
	}
	k.add("7:0:0:0", false, false, 0, "")
	k.add("6:0:0:2", true, false, 0, "5")
	for id, want := range map[string]string{"6:0:0:0": "sd-card", "6:0:0:1": "card", "6:0:0:2": "disk", "7:0:0:0": "", "9:0:0:0": ""} {
		got, err := DiskKind(k.root, id)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("DiskKind(%s) = %q, want %q", id, got, want)
		}
	}

	// A lone stick names nothing, the same as in its notice.
	_, k, _ = newGate(t)
	k.add("6:0:0:0", true, true, 0, "5")
	if got, _ := DiskKind(k.root, "6:0:0:0"); got != "" {
		t.Errorf("stick kind = %q", got)
	}
}

func TestCardKind(t *testing.T) {
	for name, want := range map[string]string{
		"USB Flash Disk":      "",
		"USB DISK 3.0":        "",
		"Mass Storage":        "",
		"SD/MMC":              "sd-card",
		"microSD Card Reader": "sd-card",
		"SDXC":                "sd-card",
		"MS/MS-Pro":           "memory-stick",
		"Memory Stick Reader": "memory-stick",
		"CF Card":             "card",
		"SD/MS Combo":         "card",
		"xD-Picture":          "card",
		"Multi-Card Reader":   "card",
		"Flash Reader":        "card",
	} {
		if got := cardKind(name); got != want {
			t.Errorf("cardKind(%q) = %q, want %q", name, got, want)
		}
	}
}

// openedCard makes a mounted card in USB device 1-1 that the owner allowed,
// on a gate that keeps a record. It returns the LUN's sysfs directory.
func openedCard(t *testing.T) (*Gate, *fakeKernel, fakeProbe, string) {
	t.Helper()
	g, k, probe := newGate(t)
	g.Record = filepath.Join(t.TempDir(), "media-kept.json")
	real := k.add("6:0:0:0", true, true, 0, "5")
	mustWrite(t, filepath.Join(real, "../../../../serial"), "EXAMPLE-SERIAL-1")
	mustWrite(t, filepath.Join(real, "block/sdb/dev"), "8:16")
	mustWrite(t, filepath.Join(real, "block/sdb/diskseq"), "9")
	mustWrite(t, filepath.Join(real, "block/sdb/sdb1/dev"), "8:17")
	probe["6:0:0:0"] = MediumPresent
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	id, token, _ := only(t, g)
	if err := g.AllowMedia(id, token); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, g.MountInfo, "40 1 8:17 / /run/media/user/card rw - vfat /dev/sdb1 rw")
	if err := g.Sync(true); err != nil {
		t.Fatal(err)
	}
	return g, k, probe, real
}

// restartedGate is the gate of a new broker process on the same machine.
// keep stands in for the controller: it keeps whatever port it is told.
func restartedGate(old *Gate, keep map[string]bool) *Gate {
	return &Gate{Sysfs: old.Sysfs, MountInfo: old.MountInfo, Probe: old.Probe, Record: old.Record,
		writeFile: old.writeFile, Kept: func() map[string]bool { return keep }}
}

// Plane 299: a broker restart keeps a medium the owner opened when it is
// still mounted and unchanged.
func TestRestartKeepsRecordedMedium(t *testing.T) {
	g, k, _, _ := openedCard(t)
	info, err := os.Stat(g.Record)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode %v", info.Mode().Perm())
	}
	media, err := readRecord(g.Record)
	if err != nil || len(media) != 1 || media["6:0:0:0"].DiskSeq != "9" || media["6:0:0:0"].Serial != "EXAMPLE-SERIAL-1" {
		t.Fatalf("record = %+v, %v", media, err)
	}

	next := restartedGate(g, nil)
	ports := next.Reclaim()
	if len(ports) != 1 || !ports["1-1"] {
		t.Fatalf("reclaimed ports: %v", ports)
	}
	next.Kept = func() map[string]bool { return ports }
	if err := next.Sync(true); err != nil {
		t.Fatal(err)
	}
	if !k.bound("6:0:0:0") {
		t.Fatal("restart pulled a recorded, mounted card")
	}
	if _, _, allowed := only(t, next); !allowed {
		t.Fatal("kept card is not listed as open")
	}
}

func TestRestartClosesChangedMedium(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, g *Gate, k *fakeKernel, real string){
		"other medium": func(t *testing.T, g *Gate, k *fakeKernel, real string) {
			mustWrite(t, filepath.Join(real, "block/sdb/diskseq"), "10")
		},
		"other serial": func(t *testing.T, g *Gate, k *fakeKernel, real string) {
			mustWrite(t, filepath.Join(real, "../../../../serial"), "EXAMPLE-SERIAL-2")
		},
		"replugged": func(t *testing.T, g *Gate, k *fakeKernel, real string) {
			mustWrite(t, filepath.Join(real, "../../../../devnum"), "6")
		},
		"unmounted": func(t *testing.T, g *Gate, k *fakeKernel, real string) {
			mustWrite(t, g.MountInfo, "")
		},
		"record readable by others": func(t *testing.T, g *Gate, k *fakeKernel, real string) {
			if err := os.Chmod(g.Record, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"record is a link": func(t *testing.T, g *Gate, k *fakeKernel, real string) {
			moved := g.Record + ".real"
			if err := os.Rename(g.Record, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, g.Record); err != nil {
				t.Fatal(err)
			}
		},
		"record has extra fields": func(t *testing.T, g *Gate, k *fakeKernel, real string) {
			data, _ := os.ReadFile(g.Record)
			data = []byte(strings.Replace(string(data), `"version":1`, `"version":1,"keepAll":true`, 1))
			if err := os.WriteFile(g.Record, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			g, k, _, real := openedCard(t)
			change(t, g, k, real)
			// The controller keeps the port anyway: the gate must still
			// close a medium the record does not match.
			next := restartedGate(g, map[string]bool{"1-1": true})
			if ports := next.Reclaim(); len(ports) != 0 {
				t.Fatalf("reclaimed ports: %v", ports)
			}
			next.Sync(true)
			if k.bound("6:0:0:0") {
				t.Fatal("restart kept a changed medium open")
			}
		})
	}
}

func TestRecordEmptiedWhenClosed(t *testing.T) {
	g, _, probe, _ := openedCard(t)
	probe["6:0:0:0"] = MediumAbsent
	g.Sync(true)
	if media, err := readRecord(g.Record); err != nil || len(media) != 0 {
		t.Fatalf("record after removal = %+v, %v", media, err)
	}
	g, _, _, _ = openedCard(t)
	g.Sync(false)
	if media, err := readRecord(g.Record); err != nil || len(media) != 0 {
		t.Fatalf("record without enforcement = %+v, %v", media, err)
	}
}

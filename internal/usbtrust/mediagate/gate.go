// Package mediagate keeps the medium inside an allowed USB storage device
// closed until the owner allows that insertion.
//
// USBGuard decides about USB devices and interfaces only. A card reader that
// was allowed would otherwise expose every card put into it: the sd driver
// reads the partition table as soon as the medium appears and udisks mounts
// it. While enforcing, the gate turns off automatic SCSI driver binding
// (drivers_autoprobe=0) and binds the upper driver (sd, sr) for a removable
// USB LUN only after the owner allows the medium that is in it right now.
// Medium presence comes from TEST UNIT READY through the LUN's bsg node,
// which never touches the medium's contents.
package mediagate

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

type Medium int

const (
	MediumUnknown Medium = iota
	MediumAbsent
	MediumPresent
	// MediumChanged is a unit attention: the medium may have been swapped.
	MediumChanged
)

// Probe asks a LUN about its medium without reading the medium's data.
type Probe interface {
	Medium(id string) (Medium, error)
	Capacity(id string) (uint64, error)
}

// LUN is one SCSI logical unit behind a USB device.
type LUN struct {
	ID        string // H:C:T:L
	Port      string // USB device name, e.g. 1-1
	DevNum    string // changes on every replug
	VIDPID    string
	Product   string
	Type      int
	Removable bool
	Driver    string // bound upper driver, empty when unbound
	InUse     bool   // mounted or held (dm, md) through the disk or a partition
	// DiskSeq is the bound disk's diskseq. The kernel raises it whenever sd
	// sees a media change, so a swap is caught even when sd, not the gate,
	// received the unit attention.
	DiskSeq string
}

// Gated reports whether the LUN's medium needs the owner's approval: a
// removable disk (card slot, stick) or optical drive.
func (l LUN) Gated() bool {
	return l.Removable && (l.Type == 0 || l.Type == 5)
}

type state struct {
	lun     LUN
	present bool
	token   string
	allowed bool
	bytes   uint64
	diskSeq string
}

type Gate struct {
	// Sysfs is the sysfs mount point, normally /sys.
	Sysfs string
	// MountInfo lists mounts, normally /proc/self/mountinfo.
	MountInfo string
	Probe     Probe
	// Event receives audit events; it may be nil.
	Event func(map[string]any)

	// writeFile replaces sysfs writes in tests.
	writeFile func(path, value string) error

	mu        sync.Mutex
	started   bool
	enforcing bool
	luns      map[string]*state
}

// Sync applies one poll. While enforcing, every gated LUN stays unbound unless
// its current medium was allowed. Turning enforcement on keeps media that are
// already mounted or held open, so arming never pulls a filesystem away.
func (g *Gate) Sync(enforcing bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.luns == nil {
		g.luns = map[string]*state{}
	}
	arming := enforcing && (!g.started || !g.enforcing)
	if !g.started || enforcing != g.enforcing {
		if err := g.write("bus/scsi/drivers_autoprobe", boolText(!enforcing)); err != nil {
			return err
		}
		g.started, g.enforcing = true, enforcing
		clear(g.luns)
	}
	luns, err := g.scan()
	if err != nil {
		return err
	}
	if !enforcing {
		var errs []error
		for _, lun := range luns {
			if lun.Driver == "" {
				errs = append(errs, g.bind(lun.ID))
			}
		}
		return errors.Join(errs...)
	}
	seen := map[string]bool{}
	var errs []error
	for _, lun := range luns {
		seen[lun.ID] = true
		if !lun.Gated() {
			if lun.Driver == "" {
				errs = append(errs, g.bind(lun.ID))
			}
			continue
		}
		st := g.luns[lun.ID]
		if st == nil || st.lun.Port != lun.Port || st.lun.DevNum != lun.DevNum {
			st = &state{}
			g.luns[lun.ID] = st
		}
		st.lun = lun
		errs = append(errs, g.step(st, arming))
	}
	for id := range g.luns {
		if !seen[id] {
			delete(g.luns, id)
		}
	}
	return errors.Join(errs...)
}

func (g *Gate) step(st *state, arming bool) error {
	lun := st.lun
	medium, err := g.Probe.Medium(lun.ID)
	if st.allowed && lun.DiskSeq != "" {
		if st.diskSeq != "" && st.diskSeq != lun.DiskSeq {
			medium, err = MediumChanged, nil
		}
		st.diskSeq = lun.DiskSeq
	}
	if err != nil || medium == MediumUnknown {
		// Fail closed: an unanswered probe never opens a medium.
		if !st.allowed && lun.Driver != "" && !(arming && lun.InUse) {
			return errors.Join(err, g.unbind(lun))
		}
		if arming && lun.Driver != "" && lun.InUse {
			g.keep(st)
		}
		return err
	}
	switch medium {
	case MediumAbsent:
		if st.present {
			g.event("removed", st)
		}
		*st = state{lun: lun}
		if lun.Driver != "" {
			return g.unbind(lun)
		}
	case MediumChanged:
		if st.allowed {
			g.event("revoked", st)
		}
		*st = state{lun: lun}
		if lun.Driver != "" {
			return g.unbind(lun)
		}
	case MediumPresent:
		if !st.present {
			st.present = true
			st.token = newToken()
			st.bytes, _ = g.Probe.Capacity(lun.ID)
			if arming && lun.Driver != "" && lun.InUse {
				g.keep(st)
				return nil
			}
			g.event("closed", st)
		}
		switch {
		case st.allowed && lun.Driver == "":
			return g.bind(lun.ID)
		case !st.allowed && lun.Driver != "":
			return g.unbind(lun)
		}
	}
	return nil
}

func (g *Gate) keep(st *state) {
	st.present, st.allowed = true, true
	if st.token == "" {
		st.token = newToken()
	}
	g.event("kept-open", st)
}

// MediaPolicy lists the media that are in gated LUNs now.
func (g *Gate) MediaPolicy() []usbtrust.MediaDecision {
	g.mu.Lock()
	defer g.mu.Unlock()
	var result []usbtrust.MediaDecision
	for _, st := range g.luns {
		if st.present {
			result = append(result, decision(st))
		}
	}
	return result
}

// AllowMedia opens the medium that is in LUN id now. connection must match
// the token issued for this insertion, so a swap voids an older approval.
func (g *Gate) AllowMedia(id, connection string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.enforcing {
		return fmt.Errorf("storage media are not gated while USB protection only audits")
	}
	st := g.luns[id]
	if st == nil || !st.present || st.token == "" ||
		subtle.ConstantTimeCompare([]byte(st.token), []byte(connection)) != 1 {
		return fmt.Errorf("medium %s is no longer in the reader; review it again", id)
	}
	if st.allowed {
		return nil
	}
	if err := g.bind(id); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(g.devicePath(id), "driver")); err != nil {
		return fmt.Errorf("medium %s: no driver took it: %w", id, err)
	}
	st.allowed = true
	g.event("allowed", st)
	return nil
}

func decision(st *state) usbtrust.MediaDecision {
	return usbtrust.MediaDecision{
		ID:         st.lun.ID,
		Connection: st.token,
		Reader:     st.lun.Product,
		VIDPID:     st.lun.VIDPID,
		Port:       st.lun.Port,
		Bytes:      st.bytes,
		Allowed:    st.allowed,
	}
}

func (g *Gate) event(name string, st *state) {
	if g.Event == nil {
		return
	}
	d := decision(st)
	g.Event(map[string]any{"event": "usb-media", "change": name, "id": d.ID, "reader": d.Reader, "vidPid": d.VIDPID, "port": d.Port, "bytes": d.Bytes})
}

func (g *Gate) devicePath(id string) string {
	return filepath.Join(g.Sysfs, "bus/scsi/devices", id)
}

func (g *Gate) bind(id string) error {
	return g.write("bus/scsi/drivers_probe", id)
}

func (g *Gate) unbind(lun LUN) error {
	if strings.ContainsAny(lun.Driver, "/.") {
		return fmt.Errorf("unexpected driver name %q", lun.Driver)
	}
	return g.write(filepath.Join("bus/scsi/drivers", lun.Driver, "unbind"), lun.ID)
}

func (g *Gate) write(rel, value string) error {
	if g.writeFile != nil {
		return g.writeFile(rel, value)
	}
	f, err := os.OpenFile(filepath.Join(g.Sysfs, rel), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	return errors.Join(err, f.Close())
}

func boolText(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// scan lists SCSI LUNs that sit behind a USB device.
func (g *Gate) scan() ([]LUN, error) {
	dir := filepath.Join(g.Sysfs, "bus/scsi/devices")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	mounts := g.mountedDevices()
	var result []LUN
	for _, entry := range entries {
		id := entry.Name()
		if strings.Count(id, ":") != 3 {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(dir, id))
		if err != nil {
			continue
		}
		usbDev := usbDevice(real, g.Sysfs)
		if usbDev == "" {
			continue
		}
		lun := LUN{
			ID:      id,
			Port:    filepath.Base(usbDev),
			DevNum:  readTrim(filepath.Join(usbDev, "devnum")),
			VIDPID:  readTrim(filepath.Join(usbDev, "idVendor")) + ":" + readTrim(filepath.Join(usbDev, "idProduct")),
			Product: readTrim(filepath.Join(usbDev, "product")),
			Type:    -1,
		}
		fmt.Sscanf(readTrim(filepath.Join(real, "type")), "%d", &lun.Type)
		if inquiry, err := os.ReadFile(filepath.Join(real, "inquiry")); err == nil && len(inquiry) > 1 {
			lun.Removable = inquiry[1]&0x80 != 0
		}
		if target, err := os.Readlink(filepath.Join(real, "driver")); err == nil {
			lun.Driver = filepath.Base(target)
		}
		lun.InUse = inUse(filepath.Join(real, "block"), mounts)
		if disks, err := os.ReadDir(filepath.Join(real, "block")); err == nil && len(disks) > 0 {
			lun.DiskSeq = readTrim(filepath.Join(real, "block", disks[0].Name(), "diskseq"))
		}
		result = append(result, lun)
	}
	return result, nil
}

// usbDevice walks up from a SCSI device to the USB device that carries it.
// The USB interface is the first ancestor with bInterfaceClass; its parent is
// the device. Non-USB SCSI devices return "".
func usbDevice(path, sysfs string) string {
	stop := filepath.Clean(sysfs)
	for p := filepath.Dir(path); p != stop && p != "/" && p != "."; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, "bInterfaceClass")); err == nil {
			parent := filepath.Dir(p)
			if _, err := os.Stat(filepath.Join(parent, "idVendor")); err == nil {
				return parent
			}
			return ""
		}
	}
	return ""
}

// inUse reports whether any disk under blockDir or its partitions is mounted
// or held by another block device.
func inUse(blockDir string, mounts map[string]bool) bool {
	disks, err := os.ReadDir(blockDir)
	if err != nil {
		return false
	}
	for _, disk := range disks {
		diskDir := filepath.Join(blockDir, disk.Name())
		nodes := []string{diskDir}
		if parts, err := os.ReadDir(diskDir); err == nil {
			for _, part := range parts {
				if strings.HasPrefix(part.Name(), disk.Name()) {
					nodes = append(nodes, filepath.Join(diskDir, part.Name()))
				}
			}
		}
		for _, node := range nodes {
			if mounts[readTrim(filepath.Join(node, "dev"))] {
				return true
			}
			if holders, err := os.ReadDir(filepath.Join(node, "holders")); err == nil && len(holders) > 0 {
				return true
			}
		}
	}
	return false
}

// mountedDevices returns the major:minor numbers of mounted block devices.
func (g *Gate) mountedDevices() map[string]bool {
	result := map[string]bool{}
	f, err := os.Open(g.MountInfo)
	if err != nil {
		return result
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 2 {
			result[fields[2]] = true
		}
	}
	return result
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

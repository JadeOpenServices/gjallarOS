package mediagate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// The record lists the media the owner opened, so a broker restart (a
// rebuild that changes the broker) does not pull a mounted filesystem away.
// It keeps no secret, but it lives with the broker's other runtime files,
// root only, and goes away at reboot.

const recordVersion = 1

// maxRecordSize bounds the record read back: a few hundred bytes per medium.
const maxRecordSize = 64 << 10

// openMedium is one opened medium as the record keeps it. Every field must
// match again after the restart.
type openMedium struct {
	ID     string `json:"id"`
	Port   string `json:"port"`
	DevNum string `json:"devNum"`
	VIDPID string `json:"vidPid"`
	Serial string `json:"serial"`
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
	// DiskSeq is the kernel's number for this medium. It only grows during a
	// boot, so a medium put in later, or the same one put in again, never
	// gets it back.
	DiskSeq string `json:"diskSeq"`
	Bytes   uint64 `json:"bytes"`
}

type record struct {
	Version int          `json:"version"`
	Media   []openMedium `json:"media"`
}

func (m openMedium) matches(lun LUN) bool {
	return m.ID == lun.ID && m.Port == lun.Port && m.DevNum == lun.DevNum &&
		m.VIDPID == lun.VIDPID && m.Serial == lun.Serial &&
		m.Vendor == lun.Vendor && m.Model == lun.Model &&
		m.DiskSeq != "" && m.DiskSeq == lun.DiskSeq
}

// save writes the media that are open now: allowed by the owner (or kept),
// bound and with a disk number. A medium allowed during this poll is written
// at the next one, once sd has bound it.
func (g *Gate) save() error {
	if g.Record == "" {
		return nil
	}
	doc := record{Version: recordVersion, Media: []openMedium{}}
	if g.enforcing {
		for _, st := range g.luns {
			lun := st.lun
			if !st.allowed || !st.present || lun.Driver == "" || lun.DiskSeq == "" || st.bytes == 0 {
				continue
			}
			doc.Media = append(doc.Media, openMedium{
				ID: lun.ID, Port: lun.Port, DevNum: lun.DevNum, VIDPID: lun.VIDPID,
				Serial: lun.Serial, Vendor: lun.Vendor, Model: lun.Model,
				DiskSeq: lun.DiskSeq, Bytes: st.bytes,
			})
		}
	}
	sort.Slice(doc.Media, func(i, j int) bool { return doc.Media[i].ID < doc.Media[j].ID })
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if g.saved != nil && bytes.Equal(data, g.saved) {
		return nil
	}
	if err := writeRecord(g.Record, data); err != nil {
		return fmt.Errorf("record open media: %w", err)
	}
	g.saved = data
	return nil
}

func writeRecord(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".media-record-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	err = errors.Join(err, f.Chmod(0o600), f.Close())
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func readRecord(path string) (map[string]openMedium, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is not a private file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRecordSize {
		return nil, fmt.Errorf("%s is too large", path)
	}
	var doc record
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Version != recordVersion {
		return nil, fmt.Errorf("%s: unknown version %d", path, doc.Version)
	}
	media := map[string]openMedium{}
	for _, m := range doc.Media {
		media[m.ID] = m
	}
	return media, nil
}

// Reclaim runs once, before the first poll after a broker restart, and lists
// the USB ports with a medium that is still open as the owner left it: bound,
// mounted or held, and with the same port, device number, USB IDs and serial,
// SCSI vendor and model, disk number and size as recorded. The first poll
// keeps only those media open; every other medium closes and is asked about
// again. A missing or unreadable record keeps nothing.
func (g *Gate) Reclaim() map[string]bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reclaimed = map[string]openMedium{}
	if g.Record == "" {
		return nil
	}
	media, err := readRecord(g.Record)
	if err != nil {
		if g.Event != nil {
			g.Event(map[string]any{"event": "usb-media", "change": "record-unreadable", "error": err.Error()})
		}
		return nil
	}
	if len(media) == 0 {
		return nil
	}
	luns, err := g.scan()
	if err != nil {
		return nil
	}
	ports := map[string]bool{}
	for _, lun := range luns {
		medium, ok := media[lun.ID]
		if !ok || !lun.Gated() || lun.Driver == "" || !lun.InUse || !medium.matches(lun) {
			continue
		}
		size, err := g.Probe.Capacity(lun.ID)
		if err != nil || size == 0 || size != medium.Bytes {
			continue
		}
		g.reclaimed[lun.ID] = medium
		ports[lun.Port] = true
	}
	return ports
}

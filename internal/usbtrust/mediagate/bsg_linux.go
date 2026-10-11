package mediagate

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// sgIOv4 is struct sg_io_v4 from <linux/bsg.h> (160 bytes).
type sgIOv4 struct {
	Guard           int32
	Protocol        uint32
	Subprotocol     uint32
	RequestLen      uint32
	Request         uint64
	RequestTag      uint64
	RequestAttr     uint32
	RequestPriority uint32
	RequestExtra    uint32
	MaxResponseLen  uint32
	Response        uint64
	DoutIovecCount  uint32
	DoutXferLen     uint32
	DinIovecCount   uint32
	DinXferLen      uint32
	DoutXferp       uint64
	DinXferp        uint64
	Timeout         uint32
	Flags           uint32
	UsrPtr          uint64
	SpareIn         uint32
	DriverStatus    uint32
	TransportStatus uint32
	DeviceStatus    uint32
	RetryDelay      uint32
	Info            uint32
	Duration        uint32
	ResponseLen     uint32
	DinResid        int32
	DoutResid       int32
	GeneratedTag    uint64
	SpareOut        uint32
	Padding         uint32
}

const (
	sgIO              = 0x2285
	driverSense       = 0x08 // driver byte: sense data returned, not an error
	statusGood        = 0x00
	statusCheck       = 0x02
	senseNotReady     = 0x02
	senseUnitAttn     = 0x06
	ascMediumAbsent   = 0x3a
	ascBecomingReady  = 0x04
	ascMediumChanged  = 0x28
	ascPowerOnReset   = 0x29
	commandTimeoutMs  = 3000
	opTestUnitReady   = 0x00
	opReadCapacity10  = 0x25
	readCapacityBytes = 8
)

var _ = [1]struct{}{}[unsafe.Sizeof(sgIOv4{})-160]

// BSG probes LUNs through /dev/bsg/H:C:T:L. It works while no upper driver is
// bound and sends only TEST UNIT READY and READ CAPACITY.
type BSG struct {
	Dir string // normally /dev/bsg
}

type scsiResult struct {
	status        uint32
	key, asc, asq byte
}

func (b BSG) command(id string, cdb []byte, din []byte) (scsiResult, error) {
	if filepath.Base(id) != id {
		return scsiResult{}, fmt.Errorf("invalid LUN %q", id)
	}
	f, err := os.OpenFile(filepath.Join(b.Dir, id), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return scsiResult{}, err
	}
	defer f.Close()
	sense := make([]byte, 32)
	var pin runtime.Pinner
	defer pin.Unpin()
	pin.Pin(&cdb[0])
	pin.Pin(&sense[0])
	hdr := sgIOv4{
		Guard:          'Q',
		RequestLen:     uint32(len(cdb)),
		Request:        uint64(uintptr(unsafe.Pointer(&cdb[0]))),
		MaxResponseLen: uint32(len(sense)),
		Response:       uint64(uintptr(unsafe.Pointer(&sense[0]))),
		Timeout:        commandTimeoutMs,
	}
	if len(din) > 0 {
		pin.Pin(&din[0])
		hdr.DinXferLen = uint32(len(din))
		hdr.DinXferp = uint64(uintptr(unsafe.Pointer(&din[0])))
	}
	pin.Pin(&hdr)
	raw, err := f.SyscallConn()
	if err != nil {
		return scsiResult{}, err
	}
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, sgIO, uintptr(unsafe.Pointer(&hdr)))
	}); err != nil {
		return scsiResult{}, err
	}
	if errno != 0 {
		return scsiResult{}, fmt.Errorf("SG_IO %s: %w", id, errno)
	}
	if hdr.DriverStatus&^driverSense != 0 || hdr.TransportStatus != 0 {
		return scsiResult{}, fmt.Errorf("SG_IO %s: driver status %#x, transport status %#x", id, hdr.DriverStatus, hdr.TransportStatus)
	}
	result := scsiResult{status: hdr.DeviceStatus}
	result.key, result.asc, result.asq = parseSense(sense[:min(int(hdr.ResponseLen), len(sense))])
	return result, nil
}

// parseSense reads key, ASC and ASCQ from fixed or descriptor format sense.
func parseSense(sense []byte) (key, asc, ascq byte) {
	if len(sense) < 4 {
		return 0, 0, 0
	}
	switch sense[0] & 0x7f {
	case 0x70, 0x71:
		if len(sense) < 14 {
			return sense[2] & 0x0f, 0, 0
		}
		return sense[2] & 0x0f, sense[12], sense[13]
	case 0x72, 0x73:
		return sense[1] & 0x0f, sense[2], sense[3]
	}
	return 0, 0, 0
}

func classify(r scsiResult) Medium {
	switch r.status {
	case statusGood:
		return MediumPresent
	case statusCheck:
	default:
		return MediumUnknown
	}
	switch {
	case r.key == senseNotReady && r.asc == ascMediumAbsent:
		return MediumAbsent
	case r.key == senseNotReady && r.asc == ascBecomingReady:
		// The medium is spinning up or initializing; the next poll decides.
		return MediumUnknown
	case r.key == senseUnitAttn && (r.asc == ascMediumChanged || r.asc == ascPowerOnReset):
		return MediumChanged
	}
	return MediumUnknown
}

func (b BSG) Medium(id string) (Medium, error) {
	r, err := b.command(id, []byte{opTestUnitReady, 0, 0, 0, 0, 0}, nil)
	if err != nil {
		return MediumUnknown, err
	}
	return classify(r), nil
}

// Capacity is for display only; READ CAPACITY returns what the reader reports
// about the medium and does not read its blocks.
func (b BSG) Capacity(id string) (uint64, error) {
	cdb := make([]byte, 10)
	cdb[0] = opReadCapacity10
	data := make([]byte, readCapacityBytes)
	r, err := b.command(id, cdb, data)
	if err != nil {
		return 0, err
	}
	if r.status != statusGood {
		return 0, fmt.Errorf("READ CAPACITY %s: status %#x", id, r.status)
	}
	last := binary.BigEndian.Uint32(data[0:4])
	block := binary.BigEndian.Uint32(data[4:8])
	if last == 0xffffffff {
		return 0, nil
	}
	return (uint64(last) + 1) * uint64(block), nil
}

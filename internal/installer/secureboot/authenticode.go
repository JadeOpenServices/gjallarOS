package secureboot

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// AuthenticodeSHA256 is the hash shim compares with a SHA-256 entry in its
// MOK list or in db/dbx: the headers without the checksum and without the
// certificate table entry, then every section in file order, then the bytes
// after the last section up to the certificate table (shim 16.1 pe.c,
// generate_hash). Anything shim would refuse to hash is an error here too.
func AuthenticodeSHA256(data []byte) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	u16 := func(at int) uint16 { return binary.LittleEndian.Uint16(data[at:]) }
	u32 := func(at int) uint32 { return binary.LittleEndian.Uint32(data[at:]) }

	if len(data) < 0x40 || string(data[:2]) != "MZ" {
		return zero, errors.New("not a PE image: no MZ header")
	}
	pe := int(u32(0x3c))
	if pe < 0x40 || pe > len(data)-24 || string(data[pe:pe+4]) != "PE\x00\x00" {
		return zero, errors.New("not a PE image: no PE header")
	}
	sections := int(u16(pe + 6))
	optSize := int(u16(pe + 20))
	opt := pe + 24
	if optSize < 96 || opt+optSize > len(data) {
		return zero, errors.New("PE optional header runs past the file")
	}
	var dirs, dirCount int
	switch u16(opt) {
	case 0x10b:
		dirCount, dirs = int(u32(opt+92)), opt+96
	case 0x20b:
		dirCount, dirs = int(u32(opt+108)), opt+112
	default:
		return zero, errors.New("PE optional header has an unknown magic")
	}
	checksum := opt + 64
	secdir := dirs + 4*8
	if dirCount < 5 || secdir+8 > opt+optSize {
		return zero, errors.New("PE image has no certificate table entry")
	}
	headers := int(u32(opt + 60))
	table := opt + optSize
	if headers < secdir+8 || headers > len(data) || table+40*sections > len(data) {
		return zero, errors.New("PE headers run past the file")
	}
	certOff, certSize := int(u32(secdir)), int(u32(secdir+4))
	end := len(data)
	if certSize != 0 {
		// shim hashes up to file size minus the table, so the table must
		// close the file.
		if certOff < headers || certOff > len(data) || certSize != len(data)-certOff {
			return zero, errors.New("PE certificate table is not at the end of the file")
		}
		end = certOff
	}

	type span struct{ off, size int }
	var spans []span
	for i := range sections {
		at := table + 40*i
		size, off := int(u32(at+16)), int(u32(at+20))
		if size == 0 {
			continue
		}
		if off < headers || size > end-off {
			return zero, fmt.Errorf("PE section %d lies outside the image", i)
		}
		spans = append(spans, span{off, size})
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].off < spans[b].off })

	h := sha256.New()
	h.Write(data[:checksum])
	h.Write(data[checksum+4 : secdir])
	h.Write(data[secdir+8 : headers])
	hashed := headers
	last := headers
	for _, s := range spans {
		if s.off < last {
			return zero, errors.New("PE sections overlap")
		}
		h.Write(data[s.off : s.off+s.size])
		hashed += s.size
		last = s.off + s.size
	}
	if hashed < end {
		h.Write(data[hashed:end])
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

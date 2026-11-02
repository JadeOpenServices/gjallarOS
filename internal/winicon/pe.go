package winicon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Resource types (winuser.h).
const (
	rtIcon      = 3
	rtGroupIcon = 14
)

// maxSections and maxDirEntries bound the tables walked in a program file.
const (
	maxSections   = 96
	maxDirEntries = 4096
)

// peFile is the part of a program file needed to find its resources.
type peFile struct {
	r        io.ReaderAt
	size     int64
	sections []section
	// rsrcOff and rsrcSize place the resource directory in the file.
	rsrcOff, rsrcSize int64
}

type section struct {
	va, vsize, off, rawSize uint32
}

// ProgramIcon lists the icons of the first icon group in a program's
// resources, the one file managers show for it. Named groups come before
// numbered ones, in the order the file lists them.
func ProgramIcon(r io.ReaderAt, size int64) ([]Entry, error) {
	f, err := openPE(r, size)
	if err != nil {
		return nil, err
	}
	group, err := f.firstData(rtGroupIcon, -1)
	if err != nil {
		return nil, err
	}
	if len(group) < 6 || binary.LittleEndian.Uint16(group[0:]) != 0 || binary.LittleEndian.Uint16(group[2:]) != 1 {
		return nil, ErrNoIcon
	}
	count := int(binary.LittleEndian.Uint16(group[4:]))
	if count > MaxEntries {
		return nil, fmt.Errorf("icon group lists %d icons, more than %d", count, MaxEntries)
	}
	if len(group) < 6+14*count {
		return nil, errors.New("icon group is cut short")
	}
	var entries []Entry
	for i := range count {
		e := group[6+14*i:]
		data, err := f.firstData(rtIcon, int(binary.LittleEndian.Uint16(e[12:])))
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Width:    side(e[0]),
			Height:   side(e[1]),
			BitCount: int(binary.LittleEndian.Uint16(e[6:])),
			Data:     data,
		})
	}
	if len(entries) == 0 {
		return nil, ErrNoIcon
	}
	return entries, nil
}

func readAt(r io.ReaderAt, size, off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off+int64(n) > size {
		return nil, ErrNoIcon
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return buf, nil
}

// openPE reads the headers: the DOS stub's pointer to the PE header, the
// file header, the optional header's resource directory, and the section
// table to map addresses to file offsets.
func openPE(r io.ReaderAt, size int64) (*peFile, error) {
	dos, err := readAt(r, size, 0, 64)
	if err != nil || dos[0] != 'M' || dos[1] != 'Z' {
		return nil, ErrNoIcon
	}
	peOff := int64(binary.LittleEndian.Uint32(dos[0x3c:]))
	hdr, err := readAt(r, size, peOff, 24)
	if err != nil || string(hdr[:4]) != "PE\x00\x00" {
		return nil, ErrNoIcon
	}
	nsec := int(binary.LittleEndian.Uint16(hdr[6:]))
	optSize := int(binary.LittleEndian.Uint16(hdr[20:]))
	if nsec == 0 || nsec > maxSections || optSize < 2 {
		return nil, ErrNoIcon
	}
	opt, err := readAt(r, size, peOff+24, optSize)
	if err != nil {
		return nil, ErrNoIcon
	}
	// The data directories start at 96 in PE32 and 112 in PE32+, after
	// their count; the resource directory is the third.
	var dirs int
	switch binary.LittleEndian.Uint16(opt[0:]) {
	case 0x10b:
		dirs = 96
	case 0x20b:
		dirs = 112
	default:
		return nil, ErrNoIcon
	}
	if optSize < dirs+3*8 || binary.LittleEndian.Uint32(opt[dirs-4:]) < 3 {
		return nil, ErrNoIcon
	}
	rsrcRVA := binary.LittleEndian.Uint32(opt[dirs+16:])
	rsrcSize := binary.LittleEndian.Uint32(opt[dirs+20:])
	if rsrcRVA == 0 || rsrcSize == 0 {
		return nil, ErrNoIcon
	}
	table, err := readAt(r, size, peOff+24+int64(optSize), 40*nsec)
	if err != nil {
		return nil, ErrNoIcon
	}
	f := &peFile{r: r, size: size}
	for i := range nsec {
		s := table[40*i:]
		f.sections = append(f.sections, section{
			vsize:   binary.LittleEndian.Uint32(s[8:]),
			va:      binary.LittleEndian.Uint32(s[12:]),
			rawSize: binary.LittleEndian.Uint32(s[16:]),
			off:     binary.LittleEndian.Uint32(s[20:]),
		})
	}
	off, avail, ok := f.fileOffset(rsrcRVA)
	if !ok {
		return nil, ErrNoIcon
	}
	f.rsrcOff, f.rsrcSize = off, min(int64(rsrcSize), avail)
	return f, nil
}

// fileOffset maps an address to its place in the file, and says how many
// bytes of its section follow it there.
func (f *peFile) fileOffset(rva uint32) (int64, int64, bool) {
	for _, s := range f.sections {
		if rva >= s.va && uint64(rva) < uint64(s.va)+uint64(s.rawSize) {
			delta := int64(rva - s.va)
			return int64(s.off) + delta, int64(s.rawSize) - delta, true
		}
	}
	return 0, 0, false
}

// rsrc reads n bytes at off in the resource directory.
func (f *peFile) rsrc(off int64, n int) ([]byte, error) {
	if off < 0 || off+int64(n) > f.rsrcSize {
		return nil, ErrNoIcon
	}
	return readAt(f.r, f.size, f.rsrcOff+off, n)
}

// child finds the entry at one level of the resource tree: the one with
// number id, or the first one if id is -1. It gives the entry's offset and
// whether that is a further directory.
func (f *peFile) child(dir int64, id int) (int64, bool, error) {
	head, err := f.rsrc(dir, 16)
	if err != nil {
		return 0, false, err
	}
	named := int(binary.LittleEndian.Uint16(head[12:]))
	numbered := int(binary.LittleEndian.Uint16(head[14:]))
	n := named + numbered
	if n == 0 {
		return 0, false, ErrNoIcon
	}
	if n > maxDirEntries {
		return 0, false, errors.New("resource directory is too large")
	}
	list, err := f.rsrc(dir+16, 8*n)
	if err != nil {
		return 0, false, err
	}
	for i := range n {
		e := list[8*i:]
		name := binary.LittleEndian.Uint32(e[0:])
		if id >= 0 && (name&0x80000000 != 0 || int(name) != id) {
			continue
		}
		to := binary.LittleEndian.Uint32(e[4:])
		return int64(to & 0x7fffffff), to&0x80000000 != 0, nil
	}
	return 0, false, ErrNoIcon
}

// firstData gives the data of resource type typ, number id (or the first
// one if -1), in its first language. The tree has three levels.
func (f *peFile) firstData(typ, id int) ([]byte, error) {
	dir, isDir, err := f.child(0, typ)
	if err != nil || !isDir {
		return nil, ErrNoIcon
	}
	if dir, isDir, err = f.child(dir, id); err != nil || !isDir {
		return nil, ErrNoIcon
	}
	leaf, isDir, err := f.child(dir, -1)
	if err != nil || isDir {
		return nil, ErrNoIcon
	}
	entry, err := f.rsrc(leaf, 16)
	if err != nil {
		return nil, err
	}
	rva := binary.LittleEndian.Uint32(entry[0:])
	n := binary.LittleEndian.Uint32(entry[4:])
	if n == 0 || n > MaxEntryBytes {
		return nil, ErrNoIcon
	}
	off, avail, ok := f.fileOffset(rva)
	if !ok || int64(n) > avail {
		return nil, ErrNoIcon
	}
	return readAt(f.r, f.size, off, int(n))
}

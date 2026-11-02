// Package winpe reads the headers of a program file for the Wine app
// tools (Plane 277, wiki 931). Every read is bounded: the file comes from
// an app or a download and may be built to break a parser. Callers run it
// in a sandbox all the same (cmd/gjallar-wine).
package winpe

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

var (
	// ErrNotPE is a file that is not a PE program.
	ErrNotPE = errors.New("not a PE program")
	// ErrNotFound is a table or resource the file does not have, or one
	// that points outside the file.
	ErrNotFound = errors.New("not in the file")
	// ErrTooLarge is a resource directory with more entries than any real
	// program has.
	ErrTooLarge = errors.New("resource directory is too large")
)

// Limits on the tables walked in a file.
const (
	MaxSections   = 96
	maxDirEntries = 4096
)

// Data directories (winnt.h).
const (
	DirImport      = 1
	DirResource    = 2
	DirDelayImport = 13
	DirCLR         = 14
)

// Machine types (winnt.h).
const (
	MachineI386  = 0x14c
	MachineAMD64 = 0x8664
	MachineARM   = 0x1c0
	MachineARMNT = 0x1c4
	MachineARM64 = 0xaa64
)

// File is a program file's headers.
type File struct {
	r    io.ReaderAt
	size int64

	Machine         uint16
	Characteristics uint16
	// PE32Plus is a 64-bit optional header.
	PE32Plus  bool
	Subsystem uint16
	ImageBase uint64
	Sections  []Section
	dirs      []Dir
}

// Section is one entry of the section table.
type Section struct {
	// Name is the section's name, printable ASCII only.
	Name                    string
	VA, VSize, Off, RawSize uint32
}

// Dir is a data directory: where a table is, as an address in memory.
type Dir struct {
	RVA, Size uint32
}

// HasPEHeader says whether r starts with a DOS header that points at a PE
// header. A file with the first and not the second is an old 16-bit or
// DOS program.
func HasPEHeader(r io.ReaderAt, size int64) (mz, pe bool) {
	dos, err := readAt(r, size, 0, 64)
	if err != nil || dos[0] != 'M' || dos[1] != 'Z' {
		return false, false
	}
	sig, err := readAt(r, size, int64(binary.LittleEndian.Uint32(dos[0x3c:])), 4)
	return true, err == nil && string(sig) == "PE\x00\x00"
}

// Open reads the headers: the DOS stub's pointer to the PE header, the
// file header, the optional header's data directories, and the section
// table to map addresses to file offsets.
func Open(r io.ReaderAt, size int64) (*File, error) {
	dos, err := readAt(r, size, 0, 64)
	if err != nil || dos[0] != 'M' || dos[1] != 'Z' {
		return nil, ErrNotPE
	}
	peOff := int64(binary.LittleEndian.Uint32(dos[0x3c:]))
	hdr, err := readAt(r, size, peOff, 24)
	if err != nil || string(hdr[:4]) != "PE\x00\x00" {
		return nil, ErrNotPE
	}
	f := &File{
		r:               r,
		size:            size,
		Machine:         binary.LittleEndian.Uint16(hdr[4:]),
		Characteristics: binary.LittleEndian.Uint16(hdr[22:]),
	}
	nsec := int(binary.LittleEndian.Uint16(hdr[6:]))
	optSize := int(binary.LittleEndian.Uint16(hdr[20:]))
	if nsec == 0 || nsec > MaxSections || optSize < 2 {
		return nil, ErrNotPE
	}
	opt, err := readAt(r, size, peOff+24, optSize)
	if err != nil {
		return nil, ErrNotPE
	}
	// The data directories start at 96 in PE32 and 112 in PE32+, after
	// their count.
	var dirs int
	switch binary.LittleEndian.Uint16(opt[0:]) {
	case 0x10b:
		dirs = 96
		if optSize >= 32 {
			f.ImageBase = uint64(binary.LittleEndian.Uint32(opt[28:]))
		}
	case 0x20b:
		dirs = 112
		f.PE32Plus = true
		if optSize >= 32 {
			f.ImageBase = binary.LittleEndian.Uint64(opt[24:])
		}
	default:
		return nil, ErrNotPE
	}
	if optSize >= 70 {
		f.Subsystem = binary.LittleEndian.Uint16(opt[68:])
	}
	if optSize >= dirs {
		n := min(int(binary.LittleEndian.Uint32(opt[dirs-4:])), 16, (optSize-dirs)/8)
		for i := range n {
			d := opt[dirs+8*i:]
			f.dirs = append(f.dirs, Dir{RVA: binary.LittleEndian.Uint32(d), Size: binary.LittleEndian.Uint32(d[4:])})
		}
	}
	table, err := readAt(r, size, peOff+24+int64(optSize), 40*nsec)
	if err != nil {
		return nil, ErrNotPE
	}
	for i := range nsec {
		s := table[40*i:]
		f.Sections = append(f.Sections, Section{
			Name:    printable(s[:8]),
			VSize:   binary.LittleEndian.Uint32(s[8:]),
			VA:      binary.LittleEndian.Uint32(s[12:]),
			RawSize: binary.LittleEndian.Uint32(s[16:]),
			Off:     binary.LittleEndian.Uint32(s[20:]),
		})
	}
	return f, nil
}

// printable is b up to its first NUL, with anything but printable ASCII
// left out.
func printable(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		if c > ' ' && c < 0x7f {
			s.WriteByte(c)
		}
	}
	return s.String()
}

// Dir gives data directory i, if the file has it and it is not empty.
func (f *File) Dir(i int) (Dir, bool) {
	if i < 0 || i >= len(f.dirs) || f.dirs[i].RVA == 0 || f.dirs[i].Size == 0 {
		return Dir{}, false
	}
	return f.dirs[i], true
}

// fileOffset maps an address to its place in the file, and says how many
// bytes of its section follow it there.
func (f *File) fileOffset(rva uint32) (int64, int64, bool) {
	for _, s := range f.Sections {
		if rva >= s.VA && uint64(rva) < uint64(s.VA)+uint64(s.RawSize) {
			delta := int64(rva - s.VA)
			return int64(s.Off) + delta, int64(s.RawSize) - delta, true
		}
	}
	return 0, 0, false
}

// ReadRVA reads n bytes at address rva. They must lie in one section.
func (f *File) ReadRVA(rva uint32, n int) ([]byte, error) {
	off, avail, ok := f.fileOffset(rva)
	if !ok || int64(n) > avail {
		return nil, ErrNotFound
	}
	return readAt(f.r, f.size, off, n)
}

// maxString bounds a name read from the file.
const maxString = 256

// ReadString reads the NUL-ended string at address rva, of at most
// maxString bytes.
func (f *File) ReadString(rva uint32) (string, error) {
	off, avail, ok := f.fileOffset(rva)
	if !ok {
		return "", ErrNotFound
	}
	b, err := readAt(f.r, f.size, off, int(min(avail, maxString, f.size-off)))
	if err != nil {
		return "", err
	}
	for i, c := range b {
		if c == 0 {
			return string(b[:i]), nil
		}
	}
	return "", ErrNotFound
}

func readAt(r io.ReaderAt, size, off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off+int64(n) > size {
		return nil, ErrNotFound
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return buf, nil
}

// resources places the resource directory in the file.
func (f *File) resources() (off, size int64, err error) {
	d, ok := f.Dir(DirResource)
	if !ok {
		return 0, 0, ErrNotFound
	}
	off, avail, ok := f.fileOffset(d.RVA)
	if !ok {
		return 0, 0, ErrNotFound
	}
	return off, min(int64(d.Size), avail), nil
}

// rsrc reads n bytes at off in the resource directory at base.
func (f *File) rsrc(base, size, off int64, n int) ([]byte, error) {
	if off < 0 || off+int64(n) > size {
		return nil, ErrNotFound
	}
	return readAt(f.r, f.size, base+off, n)
}

// child finds the entry at one level of the resource tree: the one with
// number id, or the first one if id is -1. It gives the entry's offset and
// whether that is a further directory.
func (f *File) child(base, size, dir int64, id int) (int64, bool, error) {
	head, err := f.rsrc(base, size, dir, 16)
	if err != nil {
		return 0, false, err
	}
	named := int(binary.LittleEndian.Uint16(head[12:]))
	numbered := int(binary.LittleEndian.Uint16(head[14:]))
	n := named + numbered
	if n == 0 {
		return 0, false, ErrNotFound
	}
	if n > maxDirEntries {
		return 0, false, ErrTooLarge
	}
	list, err := f.rsrc(base, size, dir+16, 8*n)
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
	return 0, false, ErrNotFound
}

// Resource gives the data of resource type typ, number id (or the first
// one if -1), in its first language, if it is at most max bytes. The tree
// has three levels. Named entries come before numbered ones, in the order
// the file lists them.
func (f *File) Resource(typ, id int, max uint32) ([]byte, error) {
	base, size, err := f.resources()
	if err != nil {
		return nil, err
	}
	dir, isDir, err := f.child(base, size, 0, typ)
	if err != nil || !isDir {
		return nil, ErrNotFound
	}
	if dir, isDir, err = f.child(base, size, dir, id); err != nil || !isDir {
		return nil, ErrNotFound
	}
	leaf, isDir, err := f.child(base, size, dir, -1)
	if err != nil || isDir {
		return nil, ErrNotFound
	}
	entry, err := f.rsrc(base, size, leaf, 16)
	if err != nil {
		return nil, err
	}
	rva := binary.LittleEndian.Uint32(entry[0:])
	n := binary.LittleEndian.Uint32(entry[4:])
	if n == 0 || n > max {
		return nil, ErrNotFound
	}
	return f.ReadRVA(rva, int(n))
}

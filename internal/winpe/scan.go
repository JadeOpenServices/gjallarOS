package winpe

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Report is what a scan reads from an installer or program, before Wine
// runs it (wiki 931 step 1). It is the fixed shape the sandboxed scan
// sends back; Check tells whether one is well formed.
type Report struct {
	// Kind is pe, ne (16-bit), dos, msi or other.
	Kind string `json:"kind"`
	// Machine is x86, x64, arm64, arm or other; empty unless Kind is pe.
	Machine string `json:"machine,omitempty"`
	// Subsystem is gui, console or other.
	Subsystem string `json:"subsystem,omitempty"`
	// LargeAddress is a 32-bit program that may use more than 2 GB.
	LargeAddress bool `json:"largeAddress,omitempty"`
	// DotNet is set for a .NET program.
	DotNet *DotNet `json:"dotnet,omitempty"`
	// Imports and DelayImports are the libraries the program names, in
	// lower case, in file order, each once. Names outside nameRE are left
	// out and counted in Skipped.
	Imports      []string `json:"imports,omitempty"`
	DelayImports []string `json:"delayImports,omitempty"`
	Skipped      int      `json:"skipped,omitempty"`
	// Elevation is the manifest's requestedExecutionLevel: asInvoker,
	// highestAvailable or requireAdministrator.
	Elevation string `json:"elevation,omitempty"`
	// DPI is what the manifest says about screen scaling: unaware, system,
	// permonitor or permonitorv2. Empty is no manifest entry.
	DPI string `json:"dpi,omitempty"`
	// Packer names the packer or installer maker that a section name
	// points to.
	Packer string `json:"packer,omitempty"`
	// WineBuiltin is a library Wine made: its DOS stub carries Wine's
	// mark instead of a program of its own.
	WineBuiltin bool `json:"wineBuiltin,omitempty"`
}

// DotNet is a .NET program's runtime header.
type DotNet struct {
	// Version is the runtime the metadata names, like v4.0.30319.
	Version string `json:"version,omitempty"`
	ILOnly  bool   `json:"ilOnly,omitempty"`
	// Needs32 is a program that must run as 32-bit.
	Needs32 bool `json:"needs32,omitempty"`
}

// Limits of a scan.
const (
	// MaxRead caps the bytes a scan reads from the file in all.
	MaxRead = 32 << 20
	// MaxNames caps the libraries listed per table.
	MaxNames = 512
	// maxManifest is the largest manifest read.
	maxManifest = 64 << 10
	// maxTokens caps the XML tokens read from a manifest.
	maxTokens = 10000
)

var (
	nameRE    = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
	versionRE = regexp.MustCompile(`^v[0-9.]{1,20}$`)
	packers   = map[string]string{
		"UPX0": "UPX", "UPX1": "UPX", ".MPRESS1": "MPRESS", ".aspack": "ASPack",
		".themida": "Themida", ".vmp0": "VMProtect", ".enigma1": "Enigma",
		".ndata": "NSIS",
	}
)

var errTooMuch = errors.New("scan read too much of the file")

// wineMarks are what Wine writes at offset 0x40 of the libraries it makes
// in a prefix: its own builtin ones, and placeholders from older setups.
// Both have the same length.
var wineMarks = [][]byte{[]byte("Wine builtin DLL"), []byte("Wine placeholder")}

// WineMarkAt is where a Wine library carries its mark, and WineMarkLen how
// long the mark is.
const (
	WineMarkAt  = 0x40
	WineMarkLen = 16
)

// IsWineMark tells whether b, read at WineMarkAt, is Wine's mark.
func IsWineMark(b []byte) bool {
	for _, m := range wineMarks {
		if bytes.HasPrefix(b, m) {
			return true
		}
	}
	return false
}

// counted fails once more than MaxRead bytes were read through it.
type counted struct {
	r    io.ReaderAt
	read int64
}

func (c *counted) ReadAt(p []byte, off int64) (int, error) {
	c.read += int64(len(p))
	if c.read > MaxRead {
		return 0, errTooMuch
	}
	return c.r.ReadAt(p, off)
}

// Scan reads what it can from the file. A file that is not a program
// gives a Report with only its Kind.
func Scan(r io.ReaderAt, size int64) (Report, error) {
	c := &counted{r: r}
	head, err := readAt(c, size, 0, min(int(size), 8))
	if err != nil {
		return Report{}, err
	}
	switch {
	case bytes.HasPrefix(head, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}):
		return Report{Kind: "msi"}, nil
	case !bytes.HasPrefix(head, []byte("MZ")):
		return Report{Kind: "other"}, nil
	}
	f, err := Open(c, size)
	if errors.Is(err, ErrNotPE) {
		return Report{Kind: oldKind(c, size)}, nil
	}
	if err != nil {
		return Report{}, err
	}
	rep := Report{Kind: "pe", Machine: machine(f.Machine), Subsystem: subsystem(f.Subsystem)}
	switch stub, err := readAt(c, size, WineMarkAt, WineMarkLen); {
	case err == nil:
		rep.WineBuiltin = IsWineMark(stub)
	case !errors.Is(err, ErrNotFound):
		return Report{}, err
	}
	rep.LargeAddress = !f.PE32Plus && f.Characteristics&0x20 != 0
	for _, s := range f.Sections {
		if p, ok := packers[s.Name]; ok {
			rep.Packer = p
			break
		}
	}
	if rep.Imports, err = f.imports(&rep.Skipped); err != nil {
		return Report{}, err
	}
	if rep.DelayImports, err = f.delayImports(&rep.Skipped); err != nil {
		return Report{}, err
	}
	if rep.DotNet, err = f.dotNet(); err != nil {
		return Report{}, err
	}
	m, err := f.Resource(24, -1, maxManifest)
	switch {
	case err == nil:
		rep.Elevation, rep.DPI = manifest(m)
	case !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrTooLarge):
		return Report{}, err
	}
	return rep, nil
}

// oldKind tells a 16-bit program from a DOS one.
func oldKind(r io.ReaderAt, size int64) string {
	dos, err := readAt(r, size, 0, 64)
	if err != nil {
		return "dos"
	}
	if sig, err := readAt(r, size, int64(binary.LittleEndian.Uint32(dos[0x3c:])), 2); err == nil && string(sig) == "NE" {
		return "ne"
	}
	return "dos"
}

func machine(m uint16) string {
	switch m {
	case MachineI386:
		return "x86"
	case MachineAMD64:
		return "x64"
	case MachineARM64:
		return "arm64"
	case MachineARM, MachineARMNT:
		return "arm"
	}
	return "other"
}

func subsystem(s uint16) string {
	switch s {
	case 2:
		return "gui"
	case 3:
		return "console"
	}
	return "other"
}

// names adds a library name to list if it is new and well formed.
func names(list []string, name string, skipped *int) []string {
	name = strings.ToLower(name)
	switch {
	case !nameRE.MatchString(name):
		*skipped++
	case !slices.Contains(list, name):
		list = append(list, name)
	}
	return list
}

// imports reads the import table: 20-byte entries that end with an empty
// one, each naming a library at offset 12.
func (f *File) imports(skipped *int) ([]string, error) {
	d, ok := f.Dir(DirImport)
	if !ok {
		return nil, nil
	}
	var list []string
	for i := range MaxNames {
		e, err := f.ReadRVA(d.RVA+uint32(20*i), 20)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		if bytes.Equal(e, make([]byte, 20)) {
			break
		}
		name, err := f.ReadString(binary.LittleEndian.Uint32(e[12:]))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		list = names(list, name, skipped)
	}
	return list, nil
}

// delayImports reads the delay-load table: 32-byte entries that end with
// an empty one, each naming a library at offset 4. Old linkers wrote that
// as an address in memory rather than one relative to the image.
func (f *File) delayImports(skipped *int) ([]string, error) {
	d, ok := f.Dir(DirDelayImport)
	if !ok {
		return nil, nil
	}
	var list []string
	for i := range MaxNames {
		e, err := f.ReadRVA(d.RVA+uint32(32*i), 32)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		if bytes.Equal(e, make([]byte, 32)) {
			break
		}
		at := uint64(binary.LittleEndian.Uint32(e[4:]))
		if binary.LittleEndian.Uint32(e[0:])&1 == 0 {
			if at < f.ImageBase || at-f.ImageBase > 0xffffffff {
				*skipped++
				continue
			}
			at -= f.ImageBase
		}
		name, err := f.ReadString(uint32(at))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		list = names(list, name, skipped)
	}
	return list, nil
}

// dotNet reads the runtime header of a .NET program and the version its
// metadata names.
func (f *File) dotNet() (*DotNet, error) {
	d, ok := f.Dir(DirCLR)
	if !ok {
		return nil, nil
	}
	h, err := f.ReadRVA(d.RVA, 24)
	if errors.Is(err, ErrNotFound) {
		return &DotNet{}, nil
	}
	if err != nil {
		return nil, err
	}
	flags := binary.LittleEndian.Uint32(h[16:])
	n := &DotNet{ILOnly: flags&1 != 0, Needs32: flags&2 != 0}
	meta := binary.LittleEndian.Uint32(h[8:])
	root, err := f.ReadRVA(meta, 16)
	if errors.Is(err, ErrNotFound) {
		return n, nil
	}
	if err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(root) != 0x424a5342 {
		return n, nil
	}
	length := binary.LittleEndian.Uint32(root[12:])
	if length == 0 || length > 255 {
		return n, nil
	}
	v, err := f.ReadRVA(meta+16, int(length))
	if errors.Is(err, ErrNotFound) {
		return n, nil
	}
	if err != nil {
		return nil, err
	}
	if s := string(bytes.TrimRight(v, "\x00")); versionRE.MatchString(s) {
		n.Version = s
	}
	return n, nil
}

// manifest reads the rights and the screen scaling an application
// manifest asks for. It stops at the first XML error with what it has.
func manifest(data []byte) (elevation, dpi string) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(data))
	var aware, awareness string
	var in string
	var text strings.Builder
	for range maxTokens {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "requestedExecutionLevel":
				for _, a := range t.Attr {
					if a.Name.Local == "level" {
						elevation = level(a.Value)
					}
				}
			case "dpiAware", "dpiAwareness":
				in = t.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if in != "" && text.Len() < 256 {
				text.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == in {
				if in == "dpiAware" {
					aware = dpiAware(text.String())
				} else {
					awareness = dpiAwareness(text.String())
				}
				in = ""
			}
		}
	}
	if awareness != "" {
		return elevation, awareness
	}
	return elevation, aware
}

func level(s string) string {
	for _, l := range []string{"asInvoker", "highestAvailable", "requireAdministrator"} {
		if strings.EqualFold(strings.TrimSpace(s), l) {
			return l
		}
	}
	return ""
}

func dpiAware(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return "system"
	case "false":
		return "unaware"
	case "true/pm", "per monitor":
		return "permonitor"
	}
	return ""
}

// dpiAwareness takes the first value it knows from the list.
func dpiAwareness(s string) string {
	for v := range strings.SplitSeq(s, ",") {
		switch v := strings.ToLower(strings.TrimSpace(v)); v {
		case "unaware", "system", "permonitor", "permonitorv2":
			return v
		}
	}
	return ""
}

// Check says whether a Report is one Scan could have made. The process
// that reads it trusts nothing the scan sends.
func (r Report) Check() error {
	in := func(v string, ok ...string) bool { return slices.Contains(ok, v) }
	switch {
	case !in(r.Kind, "pe", "ne", "dos", "msi", "other"):
		return fmt.Errorf("kind %q", r.Kind)
	case r.Kind != "pe" && (r.Machine != "" || r.Subsystem != "" || r.LargeAddress || r.DotNet != nil ||
		r.Imports != nil || r.DelayImports != nil || r.Skipped != 0 || r.Elevation != "" || r.DPI != "" || r.Packer != "" || r.WineBuiltin):
		return fmt.Errorf("details for a %s file", r.Kind)
	case r.Kind == "pe" && !in(r.Machine, "x86", "x64", "arm64", "arm", "other"):
		return fmt.Errorf("machine %q", r.Machine)
	case r.Kind == "pe" && !in(r.Subsystem, "gui", "console", "other"):
		return fmt.Errorf("subsystem %q", r.Subsystem)
	case !in(r.Elevation, "", "asInvoker", "highestAvailable", "requireAdministrator"):
		return fmt.Errorf("elevation %q", r.Elevation)
	case !in(r.DPI, "", "unaware", "system", "permonitor", "permonitorv2"):
		return fmt.Errorf("dpi %q", r.DPI)
	case r.Packer != "" && !slices.Contains(slices.Collect(maps.Values(packers)), r.Packer):
		return fmt.Errorf("packer %q", r.Packer)
	case r.Skipped < 0 || r.Skipped > 2*MaxNames:
		return fmt.Errorf("skipped %d", r.Skipped)
	case r.DotNet != nil && r.DotNet.Version != "" && !versionRE.MatchString(r.DotNet.Version):
		return fmt.Errorf("runtime version %q", r.DotNet.Version)
	}
	for _, list := range [][]string{r.Imports, r.DelayImports} {
		if len(list) > MaxNames {
			return fmt.Errorf("%d library names", len(list))
		}
		for _, n := range list {
			if !nameRE.MatchString(n) {
				return fmt.Errorf("library name %q", n)
			}
		}
	}
	return nil
}

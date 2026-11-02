package winpe

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

// The files here are SYNTHETIC: built byte by byte for the test.

const (
	secOff    = 0x200
	secVA     = 0x1000
	imageBase = 0x400000
)

// synthetic builds a 32-bit GUI program with one section that holds an
// import table, a delay-load table, a .NET header and a manifest.
func synthetic(manifestXML, section string) []byte {
	s := make([]byte, 0x800)
	put32 := func(at, v int) { binary.LittleEndian.PutUint32(s[at:], uint32(v)) }
	str := func(at int, v string) { copy(s[at:], v+"\x00") }
	// Imports at 0: three entries and the empty one.
	put32(12, secVA+0x100)
	put32(20+12, secVA+0x110)
	put32(40+12, secVA+0x120)
	put32(60+12, secVA+0x100) // the same library again
	str(0x100, "KERNEL32.dll")
	str(0x110, "user32.dll")
	str(0x120, "bad name!")
	// Delay imports at 0x80: one by RVA, one by address, then the empty one.
	put32(0x80, 1)
	put32(0x80+4, secVA+0x130)
	put32(0xa0+4, imageBase+secVA+0x140)
	str(0x130, "Comdlg32.DLL")
	str(0x140, "shell32.dll")
	// .NET header at 0x200, metadata at 0x300.
	put32(0x200, 72)
	put32(0x200+8, secVA+0x300)
	put32(0x200+12, 0x40)
	put32(0x200+16, 3)
	put32(0x300, 0x424a5342)
	put32(0x300+12, 12)
	str(0x300+16, "v2.0.50727")
	// Resources at 0x400: type 24, id 1, language 0x409, data at 0x500.
	dir := func(at, id, to int) {
		binary.LittleEndian.PutUint16(s[at+14:], 1)
		put32(at+16, id)
		put32(at+20, to)
	}
	dir(0x400, 24, 0x80000000|24)
	dir(0x400+24, 1, 0x80000000|48)
	dir(0x400+48, 0x409, 72)
	put32(0x400+72, secVA+0x500)
	put32(0x400+76, len(manifestXML))
	copy(s[0x500:], manifestXML)

	f := make([]byte, 64)
	f[0], f[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(f[0x3c:], 64)
	f = append(f, "PE\x00\x00"...)
	f = binary.LittleEndian.AppendUint16(f, MachineI386)
	f = binary.LittleEndian.AppendUint16(f, 1)
	f = append(f, make([]byte, 12)...)
	f = binary.LittleEndian.AppendUint16(f, 224)
	f = binary.LittleEndian.AppendUint16(f, 0x122)
	opt := make([]byte, 224)
	binary.LittleEndian.PutUint16(opt, 0x10b)
	binary.LittleEndian.PutUint32(opt[28:], imageBase)
	binary.LittleEndian.PutUint16(opt[68:], 2)
	binary.LittleEndian.PutUint32(opt[92:], 16)
	d := func(i, rva, size int) {
		binary.LittleEndian.PutUint32(opt[96+8*i:], uint32(rva))
		binary.LittleEndian.PutUint32(opt[100+8*i:], uint32(size))
	}
	d(DirImport, secVA, 100)
	d(DirResource, secVA+0x400, 0x100+len(manifestXML))
	d(DirDelayImport, secVA+0x80, 96)
	d(DirCLR, secVA+0x200, 72)
	f = append(f, opt...)
	name := make([]byte, 8)
	copy(name, section)
	f = append(f, name...)
	f = binary.LittleEndian.AppendUint32(f, uint32(len(s)))
	f = binary.LittleEndian.AppendUint32(f, secVA)
	f = binary.LittleEndian.AppendUint32(f, uint32(len(s)))
	f = binary.LittleEndian.AppendUint32(f, secOff)
	f = append(f, make([]byte, 16)...)
	f = append(f, make([]byte, secOff-len(f))...)
	return append(f, s...)
}

const syntheticManifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
 <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3"><security><requestedPrivileges>
  <requestedExecutionLevel level="RequireAdministrator" uiAccess="false"/>
 </requestedPrivileges></security></trustInfo>
 <application xmlns="urn:schemas-microsoft-com:asm.v3"><windowsSettings>
  <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true</dpiAware>
  <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">bogus, PerMonitorV2, system</dpiAwareness>
 </windowsSettings></application>
</assembly>`

func scan(t *testing.T, data []byte) Report {
	t.Helper()
	r, err := Scan(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	return r
}

func TestScan(t *testing.T) {
	data := synthetic(syntheticManifest, "UPX0")
	want := Report{
		Kind: "pe", Machine: "x86", Subsystem: "gui", LargeAddress: true,
		DotNet:       &DotNet{Version: "v2.0.50727", ILOnly: true, Needs32: true},
		Imports:      []string{"kernel32.dll", "user32.dll"},
		DelayImports: []string{"comdlg32.dll", "shell32.dll"},
		Skipped:      1,
		Elevation:    "requireAdministrator", DPI: "permonitorv2", Packer: "UPX",
	}
	if got := scan(t, data); !reflect.DeepEqual(got, want) {
		t.Errorf("Scan:\n got %+v %+v\nwant %+v %+v", got, got.DotNet, want, want.DotNet)
	}
	// Every cut of the file scans or fails, and what it gives is well formed.
	for n := range data {
		if r, err := Scan(bytes.NewReader(data[:n]), int64(n)); err == nil {
			if err := r.Check(); err != nil {
				t.Fatalf("cut at %d: %v", n, err)
			}
		}
	}
	// A file that makes the scan read too much stops it.
	c := &counted{r: bytes.NewReader(data)}
	c.read = MaxRead
	if _, err := c.ReadAt(make([]byte, 1), 0); err != errTooMuch {
		t.Errorf("read past MaxRead: %v", err)
	}
}

func TestScanKinds(t *testing.T) {
	ne := make([]byte, 128)
	copy(ne, "MZ")
	ne[0x3c] = 64
	copy(ne[64:], "NE")
	dos := append([]byte("MZ"), make([]byte, 100)...)
	msi := append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, make([]byte, 504)...)
	for kind, data := range map[string][]byte{
		"ne": ne, "dos": dos, "msi": msi, "other": []byte("SYNTHETIC text"),
	} {
		if got := scan(t, data); got.Kind != kind || !reflect.DeepEqual(got, Report{Kind: kind}) {
			t.Errorf("%s: %+v", kind, got)
		}
	}
	// No manifest and a plain section name: nothing about rights or scaling.
	plain := synthetic("", ".text")
	if r := scan(t, plain); r.Elevation != "" || r.DPI != "" || r.Packer != "" {
		t.Errorf("plain: %+v", r)
	}
}

func TestManifest(t *testing.T) {
	for _, c := range []struct{ xml, level, dpi string }{
		{`<a><dpiAware>True/PM</dpiAware></a>`, "", "permonitor"},
		{`<a><dpiAware> false </dpiAware><requestedExecutionLevel level="asInvoker"/></a>`, "asInvoker", "unaware"},
		{`<a><dpiAwareness>junk</dpiAwareness><dpiAware>true</dpiAware></a>`, "", "system"},
		{`<a><requestedExecutionLevel level="root"/>`, "", ""},
		{"\xef\xbb\xbf<a><requestedExecutionLevel level='highestAvailable'/></a>", "highestAvailable", ""},
		{`<?xml version="1.0" encoding="UTF-16"?><a><dpiAware>true</dpiAware></a>`, "", ""},
		{strings.Repeat("<a>", 20000), "", ""},
	} {
		if l, d := manifest([]byte(c.xml)); l != c.level || d != c.dpi {
			t.Errorf("%.60q: %q %q", c.xml, l, d)
		}
	}
}

func TestCheck(t *testing.T) {
	good := Report{Kind: "pe", Machine: "x64", Subsystem: "console"}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Report{
		"kind":     {Kind: "elf"},
		"details":  {Kind: "msi", Imports: []string{"a.dll"}},
		"machine":  {Kind: "pe", Machine: "mips", Subsystem: "gui"},
		"name":     {Kind: "pe", Machine: "x64", Subsystem: "gui", Imports: []string{"../x"}},
		"many":     {Kind: "pe", Machine: "x64", Subsystem: "gui", Imports: make([]string, MaxNames+1)},
		"level":    {Kind: "pe", Machine: "x64", Subsystem: "gui", Elevation: "root"},
		"version":  {Kind: "pe", Machine: "x64", Subsystem: "gui", DotNet: &DotNet{Version: "v4\n"}},
		"packer":   {Kind: "pe", Machine: "x64", Subsystem: "gui", Packer: "Mine"},
		"skipped":  {Kind: "pe", Machine: "x64", Subsystem: "gui", Skipped: -1},
		"no subsy": {Kind: "pe", Machine: "x64"},
	} {
		if r.Check() == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

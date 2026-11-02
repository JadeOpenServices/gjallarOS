package winesandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMissingLatestCLR32(t *testing.T) {
	// SYNTHETIC system.reg parts in Wine's format.
	const set64 = "[Software\\\\Microsoft\\\\.NETFramework] 1793326395\n#time=1dc4b\n\"InstallRoot\"=\"C:\\\\windows\\\\Microsoft.NET\\\\Framework64\\\\\"\n\"OnlyUseLatestCLR\"=dword:00000001\n\n"
	const typo32 = "[Software\\\\Wow6432Node\\\\.NETFramework] 1793326159\n\"OnlyUseLatestCLR\"=dword:00000001\n\n"
	const empty32 = "[Software\\\\Wow6432Node\\\\Microsoft\\\\.NETFramework] 1793326395\n\"InstallRoot\"=\"C:\\\\windows\\\\Microsoft.NET\\\\Framework\\\\\"\n\n"
	const set32 = "[Software\\\\Wow6432Node\\\\Microsoft\\\\.NETFramework] 1793326395\n\"OnlyUseLatestCLR\"=dword:00000001\n\n"
	const off32 = "[Software\\\\Wow6432Node\\\\Microsoft\\\\.NETFramework] 1793326395\n\"OnlyUseLatestCLR\"=dword:00000000\n\n"
	const off64 = "[Software\\\\Microsoft\\\\.NETFramework] 1793326395\n\"OnlyUseLatestCLR\"=dword:00000000\n\n"
	for _, c := range []struct {
		name, reg string
		want      bool
	}{
		{"left by old winetricks", set64 + typo32 + empty32, true},
		{"no 32-bit key", set64, true},
		{"fixed", set64 + typo32 + set32, false},
		{"32-bit set to 0", set64 + off32, false},
		{"64-bit set to 0", off64 + empty32, false},
		{"no .NET", "[Software\\\\Wine] 1793326395\n\"Version\"=\"win10\"\n\n", false},
	} {
		prefix := t.TempDir()
		if err := os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n\n"+c.reg), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := MissingLatestCLR32(prefix)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v %v, want %v", c.name, got, err, c.want)
		}
	}
	if got, err := MissingLatestCLR32(t.TempDir()); got || err != nil {
		t.Errorf("no system.reg: %v %v", got, err)
	}
}

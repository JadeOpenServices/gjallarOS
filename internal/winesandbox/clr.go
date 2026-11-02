package winesandbox

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// winetricks' dotnet40 and dotnet45 verbs (and dotnet48, which runs
// dotnet40) set OnlyUseLatestCLR for 64-bit and 32-bit programs, but
// before winetricks PR #2533 the 32-bit value went to a key without
// Microsoft\. Without it, the 32-bit pass of wineboot -u asks .NET for an
// old runtime, stops at its dialog and never ends (Plane 277, book 753).

// CLRKey32 is the key the 32-bit value belongs in, as reg add takes it.
const CLRKey32 = `HKLM\Software\Wow6432Node\Microsoft\.NETFramework`

const (
	clrSection64 = `[software\\microsoft\\.netframework]`
	clrSection32 = `[software\\wow6432node\\microsoft\\.netframework]`
	clrValue     = `"onlyuselatestclr"=`
)

// MissingLatestCLR32 reports whether prefix has OnlyUseLatestCLR set to 1
// for 64-bit programs and not set at all for 32-bit ones: what winetricks
// left before the fix. A value set to anything for 32-bit programs stays
// as it is. A prefix without system.reg has no .NET setting to fix.
func MissingLatestCLR32(prefix string) (bool, error) {
	f, err := os.Open(filepath.Join(prefix, "system.reg"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	var set64, set32 bool
	section := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.ToLower(sc.Text())
		if strings.HasPrefix(line, "[") {
			section, _, _ = strings.Cut(line, " ")
			continue
		}
		if !strings.HasPrefix(line, clrValue) {
			continue
		}
		switch section {
		case clrSection64:
			set64 = line == clrValue+"dword:00000001"
		case clrSection32:
			set32 = true
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	return set64 && !set32, nil
}

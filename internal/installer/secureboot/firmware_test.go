package secureboot

import (
	"os"
	"path/filepath"
	"testing"
)

// EMULATED efivarfs directories: no Secure Boot variable at all, then each
// of the two on its own (Plane 328).
func TestFirmwareHasSecureBoot(t *testing.T) {
	old := efivarsDir
	t.Cleanup(func() { efivarsDir = old })
	const guid = "8be4df61-93ca-11d2-aa0d-00e098032b8c"
	for _, tc := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"none", nil, false},
		{"other variables only", []string{"Boot0000-" + guid, "SecureBootEnable-" + guid}, false},
		{"SecureBoot", []string{"SecureBoot-" + guid}, true},
		{"SetupMode", []string{"SetupMode-" + guid}, true},
	} {
		efivarsDir = t.TempDir()
		for _, f := range tc.files {
			if err := os.WriteFile(filepath.Join(efivarsDir, f), []byte{6, 0, 0, 0, 0}, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := FirmwareHasSecureBoot()
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

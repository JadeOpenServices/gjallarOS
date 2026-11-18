package oldroot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

// A full gjallarOS needs btrfs subvolumes, which the VM test makes; here a
// plain folder with the same names stands in for a planted or partial disk.
func TestInspectRefusesNonSubvolumes(t *testing.T) {
	cases := map[string]func(top string){
		"nothing there": func(string) {},
		"plain folders": func(top string) {
			for _, sv := range subvolumes {
				mkdir(t, filepath.Join(top, sv))
			}
		},
		"links to the stick's own root": func(top string) {
			for _, sv := range subvolumes {
				if err := os.Symlink("/", filepath.Join(top, sv)); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			top := t.TempDir()
			plant(top)
			d, err := safefs.OpenDir(top)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			_, err = Inspect(d)
			wantRefusal(t, err, NoEvidence)
		})
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

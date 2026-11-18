package oldroot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
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

// The files Inspect reads once the subvolumes are there. An unmanaged
// install whose Secure Boot setup finished has installation-complete too
// (system/security/secure-boot/lifecycle.nix), so only management.enable
// makes it managed.
func TestInspectFiles(t *testing.T) {
	cases := map[string]struct {
		plant   func(top string)
		refusal Reason
		channel string
	}{
		"unmanaged": {plant: func(string) {}, channel: "stable"},
		"unmanaged, Secure Boot setup done": {plant: func(top string) {
			write(t, filepath.Join(top, "@/var/lib/gjallarOS/installation-complete"), "")
		}, channel: "stable"},
		"staging channel": {plant: func(top string) {
			write(t, filepath.Join(top, "@config/user.config.json"), `{"releaseChannel":"staging"}`)
		}, channel: "staging"},
		"managed": {plant: func(top string) {
			write(t, filepath.Join(top, "@config/user.config.json"), `{"management":{"enable":true}}`)
		}, refusal: Managed},
		"unknown channel": {plant: func(top string) {
			write(t, filepath.Join(top, "@config/user.config.json"), `{"releaseChannel":"nightly"}`)
		}, refusal: NoEvidence},
		"config is a link": {plant: func(top string) {
			p := filepath.Join(top, "@config/user.config.json")
			os.Remove(p)
			if err := os.Symlink("/etc/passwd", p); err != nil {
				t.Fatal(err)
			}
		}, refusal: NoEvidence},
		"config is a pipe": {plant: func(top string) {
			p := filepath.Join(top, "@config/user.config.json")
			os.Remove(p)
			if err := unix.Mkfifo(p, 0o644); err != nil {
				t.Fatal(err)
			}
		}, refusal: NoEvidence},
		"no repository file": {plant: func(top string) {
			os.Remove(filepath.Join(top, "@/etc/gjallar/repository"))
		}, refusal: NoEvidence},
		"no system profile": {plant: func(top string) {
			os.Remove(filepath.Join(top, "@nix/var/nix/profiles/system"))
		}, refusal: NoEvidence},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			top := t.TempDir()
			write(t, filepath.Join(top, "@/etc/NIXOS"), "")
			write(t, filepath.Join(top, "@/etc/gjallar/repository"), "/etc/gjallar/system\n")
			write(t, filepath.Join(top, "@config/user.config.json"), `{"management":{"enable":false}}`)
			mkdir(t, filepath.Join(top, "@nix/var/nix/profiles"))
			if err := os.Symlink("system-1-link", filepath.Join(top, "@nix/var/nix/profiles/system")); err != nil {
				t.Fatal(err)
			}
			c.plant(top)
			d, err := safefs.OpenDir(top)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			info, err := inspectFiles(d)
			if c.refusal != "" {
				wantRefusal(t, err, c.refusal)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if info.Channel != c.channel || info.LastUpdated.IsZero() {
				t.Fatalf("info %+v, want channel %s and a time", info, c.channel)
			}
		})
	}
}

func write(t *testing.T, p, data string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

package oldroot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
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

// lt is the longest string checkStrings lets through, all of it <.
var lt = strings.Repeat("<", maxConfigString)

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
	cfg := filepath.Join("@config", "user.config.json")
	with := func(change map[string]any) func(top string) {
		return func(top string) { write(t, filepath.Join(top, cfg), userConfig(t, change)) }
	}
	cases := map[string]struct {
		plant   func(top string)
		refusal Reason
		channel string
	}{
		"unmanaged": {plant: func(string) {}, channel: "stable"},
		"unmanaged, Secure Boot setup done": {plant: func(top string) {
			write(t, filepath.Join(top, "@/var/lib/gjallarOS/installation-complete"), "")
		}, channel: "stable"},
		"staging channel":      {plant: with(map[string]any{"releaseChannel": "staging"}), channel: "staging"},
		"no channel":           {plant: with(map[string]any{"releaseChannel": nil}), channel: "stable"},
		"managed":              {plant: with(map[string]any{"management": map[string]any{"enable": true}}), refusal: Managed},
		"managed, no more":     {plant: func(top string) { write(t, filepath.Join(top, cfg), `{"management":{"enable":true}}`) }, refusal: Managed},
		"managed, unknown key": {plant: with(map[string]any{"management": map[string]any{"enable": true}, "SYNTHETICunknown": 1}), refusal: Managed},
		"no username":          {plant: with(map[string]any{"username": nil}), refusal: NoEvidence},
		"username with a path": {plant: with(map[string]any{"username": "../root"}), refusal: NoEvidence},
		"only a username":      {plant: func(top string) { write(t, filepath.Join(top, cfg), `{"username":"example"}`) }, refusal: BadConfig},
		"unknown channel":      {plant: with(map[string]any{"releaseChannel": "nightly"}), refusal: BadConfig},
		// L65: the 1133 test, an unknown key and an oversized string.
		"unknown key":     {plant: with(map[string]any{"SYNTHETICunknown": true}), refusal: BadConfig},
		"renamed key":     {plant: with(map[string]any{"aiEnable": true}), refusal: BadConfig},
		"oversized value": {plant: with(map[string]any{"name": strings.Repeat("x", maxConfigString+1)}), refusal: BadConfig},
		"oversized key":   {plant: with(map[string]any{strings.Repeat("k", maxConfigString+1): 1}), refusal: BadConfig},
		"wrong type":      {plant: with(map[string]any{"snapshotsKeepDays": "SYNTHETIC"}), refusal: BadConfig},
		// L143: each < takes six bytes once encoded again; the file stays
		// under maxUserConfig, the encoded settings do not.
		"escapes past the answer cap": {plant: with(map[string]any{
			"name": lt, "email": lt, "githubUsername": lt, "weatherCity": lt,
			"weatherCountry": lt, "nextcloudHost": lt, "aiRemoteModel": lt,
		}), refusal: BadConfig},
		// L145: a theme or shell names a file the build imports.
		"theme is a path": {plant: with(map[string]any{"theme": "../../SYNTHETIC"}), refusal: BadConfig},
		"shell is a path": {plant: with(map[string]any{"shell": "../SYNTHETIC"}), refusal: BadConfig},
		"trailing data": {plant: func(top string) {
			write(t, filepath.Join(top, cfg), userConfig(t, nil)+`{"unattendedInstall":true}`)
		}, refusal: NoEvidence},
		"over the cap": {plant: func(top string) {
			write(t, filepath.Join(top, cfg), userConfig(t, nil))
			if err := os.Truncate(filepath.Join(top, cfg), maxUserConfig+1); err != nil {
				t.Fatal(err)
			}
		}, refusal: NoEvidence},
		"config is a link": {plant: func(top string) {
			p := filepath.Join(top, cfg)
			os.Remove(p)
			if err := os.Symlink("/etc/passwd", p); err != nil {
				t.Fatal(err)
			}
		}, refusal: NoEvidence},
		"@config is a link": {plant: func(top string) {
			elsewhere := t.TempDir()
			write(t, filepath.Join(elsewhere, "user.config.json"), userConfig(t, nil))
			os.RemoveAll(filepath.Join(top, "@config"))
			if err := os.Symlink(elsewhere, filepath.Join(top, "@config")); err != nil {
				t.Fatal(err)
			}
		}, refusal: NoEvidence},
		"config is a pipe": {plant: func(top string) {
			p := filepath.Join(top, cfg)
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
			write(t, filepath.Join(top, cfg), userConfig(t, nil))
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
			if info.Channel != c.channel || info.LastUpdated.IsZero() || info.Username != "example" || info.User.Username != "example" {
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

// userConfig is the default settings file with username example (SYNTHETIC)
// and the keys in change set; a nil value removes the key.
func userConfig(t *testing.T, change map[string]any) string {
	t.Helper()
	user, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	user.Username = "example"
	b, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for k, v := range change {
		if v == nil {
			delete(fields, k)
		} else {
			fields[k] = v
		}
	}
	// Written like a person or another tool would: < > & as they are.
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fields); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// L65 and loophole 138: the old disk's file never steers the stick's
// install run. Its installer keys are the ones of a fresh install.
func TestUserConfigInstallerKeys(t *testing.T) {
	top := t.TempDir()
	write(t, filepath.Join(top, "@config/user.config.json"), userConfig(t, map[string]any{
		"unattendedInstall": true, "autoReboot": true, "runUpdateChecks": true,
		"writeConfig": false, "runRebuild": false, "forceRedeploy": true,
		"hostname": "SYNTHETIC-host",
	}))
	d, err := safefs.OpenDir(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	user, err := UserConfig(d)
	if err != nil {
		t.Fatal(err)
	}
	if user.UnattendedInstall || user.AutoReboot || user.RunUpdateChecks || user.ForceRedeploy || !user.WriteConfig || !user.RunRebuild {
		t.Errorf("installer keys from the old disk: %+v", user)
	}
	if user.Hostname != "SYNTHETIC-host" {
		t.Errorf("hostname %q, want the old disk's", user.Hostname)
	}
}

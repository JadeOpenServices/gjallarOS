package storecopy

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

const (
	oldSys = "/nix/store/" + pathHash + "-nixos-system-fw13-26.11.20261101.abcdef0"
	// SYNTHETIC test key in the shape of an ed25519 public key; no real key.
	gjalKey = "gjallaros-build-2:" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/AbCde="
)

// L62: the cache is never "trusted" and signatures are always required,
// whichever way nix spells it.
func TestSubstituteArgs(t *testing.T) {
	args, err := SubstituteArgs("/run/gjallar-move/new/"+CacheName, NixOSKey, gjalKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--option", "extra-substituters", "file:///run/gjallar-move/new/gjallar-move-cache?priority=10",
		"--option", "trusted-public-keys", NixOSKey + " " + gjalKey,
		"--option", "require-sigs", "true",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("got %q", args)
	}
	for _, a := range args {
		if strings.Contains(a, "trusted=") || strings.Contains(a, "?trusted") || strings.Contains(a, "&trusted") {
			t.Errorf("trusted cache: %q", a)
		}
	}
	for _, bad := range [][]string{
		{"relative", NixOSKey},
		{"/c?trusted=1", NixOSKey},
		{"/c&trusted=1", NixOSKey},
		{"/c d", NixOSKey},
		{"/c", gjalKey},
		{"/c"},
		{"/c", NixOSKey, "bad key"},
		{"/c", NixOSKey + " " + gjalKey},
	} {
		if _, err := SubstituteArgs(bad[0], bad[1:]...); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestUnitArgs(t *testing.T) {
	args, err := ExportArgs("/run/gjallar-move/old/@nix", "/run/gjallar-move/new/"+CacheName, oldSys)
	if err != nil {
		t.Fatal(err)
	}
	cmd := slices.Index(args, "--")
	if cmd < 0 || args[cmd+1] != Nix {
		t.Fatalf("no nix after --: %q", args)
	}
	props := args[:cmd]
	for _, want := range []string{
		"--wait", "--pipe", "--collect", "--unit=" + ExportUnit,
		"--property=DynamicUser=yes", "--property=PrivateNetwork=yes",
		"--property=CapabilityBoundingSet=", "--property=NoNewPrivileges=yes",
		"--property=ProtectSystem=strict", "--property=ProtectHome=yes",
		"--property=BindReadOnlyPaths=/run/gjallar-move/old/@nix:/oldnix",
		"--property=BindPaths=/run/gjallar-move/new/gjallar-move-cache:/cache",
		"--property=RuntimeMaxSec=7200",
	} {
		if !slices.Contains(props, want) {
			t.Errorf("missing %q", want)
		}
	}
	nix := args[cmd+1:]
	if !slices.Equal(nix[len(nix)-7:], []string{"copy", "--no-check-sigs", "--from", FromURL, "--to", ToURL, oldSys}) {
		t.Errorf("nix args %q", nix)
	}
	if !strings.Contains(FromURL, "read-only=true") {
		t.Error("old store not read-only")
	}

	args, err = ClosureArgs("/run/gjallar-move/old/@nix", oldSys)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--property=BindPaths=") {
			t.Errorf("closure gets a writable folder: %q", a)
		}
	}
	// systemd splits a bind property at ":" and blanks (comment 3, c7).
	for _, dirs := range [][2]string{
		{"/o:/etc", "/c"}, {"/o", "/c:/etc"}, {"/o x", "/c"}, {"/o", "/c\tx"},
		{"/o", "/c\n"}, {"o", "/c"}, {"/o", "c"}, {"", "/c"}, {"/o", ""},
	} {
		if _, err := ExportArgs(dirs[0], dirs[1], oldSys); err == nil {
			t.Errorf("export folders %q accepted", dirs)
		}
	}
	if _, err := ClosureArgs("/o:/etc", oldSys); err == nil {
		t.Error("closure folder with a colon accepted")
	}
	for _, bad := range []string{"/etc/passwd", "/nix/store/x", oldSys + "/../../x", "--to=file:///", "/nix/store/" + pathHash + "-.hidden"} {
		if _, err := ExportArgs("/o", "/c", bad); err == nil {
			t.Errorf("export %q", bad)
		}
		if _, err := ClosureArgs("/o", bad); err == nil {
			t.Errorf("closure %q", bad)
		}
	}
}

func TestParseClosure(t *testing.T) {
	if n, err := ParseClosure([]byte(oldSys+"\t  12345678\n"), oldSys); err != nil || n != 12345678 {
		t.Fatalf("%d, %v", n, err)
	}
	for _, bad := range []string{
		"",
		oldSys + "\t1",
		oldSys + "\t1\n" + oldSys + "\t2\n",
		"/nix/store/other\t1\n",
		oldSys + "\t-1\n",
		oldSys + "\t0\n",
		oldSys + "\t1e9\n",
		oldSys + "\t99999999999999999999\n",
	} {
		if _, err := ParseClosure([]byte(bad), oldSys); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestVerifyArgs(t *testing.T) {
	args, err := VerifyArgs("/run/gjallar-move/new", oldSys, NixOSKey, gjalKey)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "--no-trust") || !slices.Contains(args, "local?root=/run/gjallar-move/new") {
		t.Errorf("%q", args)
	}
	if _, err := VerifyArgs("/new?root=/", oldSys, NixOSKey); err == nil {
		t.Error("root with options accepted")
	}
}

type links map[string]string

func (l links) Readlink(rel string, max int) (string, error) {
	v, ok := l[rel]
	if !ok {
		return "", os.ErrNotExist
	}
	if len(v) > max {
		return "", safefs.ErrTooBig
	}
	return v, nil
}

func TestOldSystem(t *testing.T) {
	ok := links{Profiles + "/system": "system-42-link", Profiles + "/system-42-link": oldSys}
	if got, err := OldSystem(ok); err != nil || got != oldSys {
		t.Fatalf("%q, %v", got, err)
	}
	for name, l := range map[string]links{
		"no profile":       {},
		"absolute gen":     {Profiles + "/system": "/etc/system-42-link"},
		"gen climbs":       {Profiles + "/system": "../../system-42-link"},
		"gen zero":         {Profiles + "/system": "system-0-link", Profiles + "/system-0-link": oldSys},
		"not a system":     {Profiles + "/system": "system-1-link", Profiles + "/system-1-link": "/nix/store/" + pathHash + "-bash-5.3"},
		"system elsewhere": {Profiles + "/system": "system-1-link", Profiles + "/system-1-link": "/tmp/" + pathHash + "-nixos-system-x"},
		"system path":      {Profiles + "/system": "system-1-link", Profiles + "/system-1-link": oldSys + "/../../etc"},
	} {
		if _, err := OldSystem(l); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The real profile folder: links are read, never followed, and a planted
// link on the way is refused.
func TestOldSystemOnDisk(t *testing.T) {
	nix := t.TempDir()
	prof := filepath.Join(nix, Profiles)
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("system-7-link", filepath.Join(prof, "system")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldSys, filepath.Join(prof, "system-7-link")); err != nil {
		t.Fatal(err)
	}
	d, err := safefs.OpenDir(nix)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got, err := OldSystem(d); err != nil || got != oldSys {
		t.Fatalf("%q, %v", got, err)
	}

	planted := t.TempDir()
	if err := os.MkdirAll(filepath.Join(planted, "var/nix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(prof, filepath.Join(planted, Profiles)); err != nil {
		t.Fatal(err)
	}
	p, err := safefs.OpenDir(planted)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := OldSystem(p); !errors.Is(err, safefs.ErrNotPlain) {
		t.Errorf("planted link on the way: %v", err)
	}
}

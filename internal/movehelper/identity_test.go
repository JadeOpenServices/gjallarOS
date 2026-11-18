package movehelper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/identity"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
)

// identityChild names the folder the namespace child of TestIdentity works
// in; set only in that child.
const identityChild = "GJ_MOVE_IDENTITY_CHILD"

// The caller names items only: an unknown or repeated one is refused
// before anything is opened.
func TestIdentityKeepChecked(t *testing.T) {
	for _, keep := range [][]identity.Item{
		{"shadow"},
		{identity.MachineID, identity.MachineID},
		{""},
	} {
		if _, err := identityOp(t.Context(), t.TempDir(), Request{Keep: keep}); err == nil || !strings.HasPrefix(err.Error(), "identity: ") {
			t.Errorf("%q: %v", keep, err)
		}
	}
}

// Runs TestIdentityChild in a user and mount namespace, as TestNewHome.
func TestIdentity(t *testing.T) {
	if os.Getenv(identityChild) != "" {
		t.Skip("parent test only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), identityChild+"="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: minUserID, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: usersGID, HostID: os.Getgid(), Size: 1}},
		AmbientCaps: []uintptr{unix.CAP_SYS_ADMIN},
	}
	out, err := cmd.CombinedOutput()
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC) {
		t.Skipf("no user namespaces here: %v", err)
	}
	if err != nil || !strings.Contains(string(out), "--- PASS: TestIdentityChild") {
		t.Fatalf("namespace child: %v\n%s", err, out)
	}
}

func TestIdentityChild(t *testing.T) {
	dir := os.Getenv(identityChild)
	if dir == "" {
		t.Skip("namespace child only")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	wasUID, wasGID := rootUID, rootGID
	rootUID, rootGID = uint32(os.Getuid()), uint32(os.Getgid())
	t.Cleanup(func() { rootUID, rootGID = wasUID, wasGID })

	const id = "0123456789abcdef0123456789abcdef\n"
	top, _ := oldHomeTop(t)
	mkdirs(t, filepath.Join(top, "@/etc"))
	if err := os.WriteFile(filepath.Join(top, "@/etc/machine-id"), []byte(id), 0o444); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	// newHomeWith makes a new disk whose installed home holds the
	// Nextcloud settings when cfg is set.
	newHomeWith := func(t *testing.T, name string, cfg bool) (root, home string) {
		t.Helper()
		root, _ = newDisk(t, dir, name)
		mkdirs(t, filepath.Join(root, "etc"))
		home = filepath.Join(root, "home/example")
		if err := os.Mkdir(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if cfg {
			mkdirs(t, filepath.Join(home, ".config/Nextcloud"))
			if err := os.WriteFile(filepath.Join(home, nextcloudCfg), []byte("[Accounts]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root, home
	}

	t.Run("machine-id", func(t *testing.T) {
		root, _ := newHomeWith(t, "mid", false)
		if err := keepMachineID(openTest(t, top), openTest(t, root)); err != nil {
			t.Fatal(err)
		}
		got := filepath.Join(root, newMachineID)
		b, err := os.ReadFile(got)
		st, _ := os.Stat(got)
		if err != nil || string(b) != id || st.Mode().Perm() != 0o444 {
			t.Errorf("written %q %v %v", b, st.Mode(), err)
		}
		// A link planted at the target is refused and its target stays.
		root, _ = newHomeWith(t, "midlink", false)
		if err := os.Symlink(outside, filepath.Join(root, newMachineID)); err != nil {
			t.Fatal(err)
		}
		if err := keepMachineID(openTest(t, top), openTest(t, root)); !errors.Is(err, safefs.ErrNotPlain) {
			t.Errorf("planted link: %v", err)
		}
		if b, _ := os.ReadFile(outside); string(b) != "keep" {
			t.Errorf("link target changed: %q", b)
		}
		// An old id that is not a machine-id is not carried over.
		bad := t.TempDir()
		mkdirs(t, filepath.Join(bad, "@/etc"))
		if err := os.WriteFile(filepath.Join(bad, "@/etc/machine-id"), []byte("00000000000000000000000000000000\n"), 0o444); err != nil {
			t.Fatal(err)
		}
		root, _ = newHomeWith(t, "midbad", false)
		if err := keepMachineID(openTest(t, bad), openTest(t, root)); err == nil {
			t.Error("all-zero id carried over")
		}
		if _, err := os.Lstat(filepath.Join(root, newMachineID)); err == nil {
			t.Error("all-zero id written")
		}
	})

	t.Run("nextcloud keep", func(t *testing.T) {
		root, home := newHomeWith(t, "nckeep", true)
		made, err := keepNextcloud(openTest(t, top), openTest(t, root), "example")
		if err != nil || made != 1 {
			t.Fatalf("made %d, %v", made, err)
		}
		st, err := os.Lstat(filepath.Join(home, "Nextcloud"))
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
			t.Errorf("sync folder: %v %v", st, err)
		}
		if names, _ := os.ReadDir(filepath.Join(home, "Nextcloud")); len(names) != 0 {
			t.Errorf("sync folder not empty: %v", names)
		}
		// A second run finds it.
		if made, err := keepNextcloud(openTest(t, top), openTest(t, root), "example"); err != nil || made != 0 {
			t.Errorf("second run: %d %v", made, err)
		}
		// With two sync folders, one copied along, only the other is made.
		two, _ := oldHomeTop(t)
		oldHome := filepath.Join(two, "@home/example")
		mkdirs(t, filepath.Join(oldHome, "Photos"))
		cfg := "[Accounts]\n0\\Folders\\1\\localPath=/home/example/Nextcloud/\n0\\Folders\\2\\localPath=/home/example/Photos/\n"
		if err := os.WriteFile(filepath.Join(oldHome, nextcloudCfg), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		root, home = newHomeWith(t, "nctwo", true)
		mkdirs(t, filepath.Join(home, "Nextcloud"))
		if made, err := keepNextcloud(openTest(t, two), openTest(t, root), "example"); err != nil || made != 1 {
			t.Errorf("two folders: %d %v", made, err)
		}
		if _, err := os.Lstat(filepath.Join(home, "Photos")); err != nil {
			t.Errorf("second folder: %v", err)
		}
		// No settings came along: nothing to keep.
		root, _ = newHomeWith(t, "ncnone", false)
		if _, err := keepNextcloud(openTest(t, top), openTest(t, root), "example"); err == nil || !strings.Contains(err.Error(), "did not come along") {
			t.Errorf("no settings: %v", err)
		}
		// Settings planted as a link are refused.
		root, home = newHomeWith(t, "nclink", false)
		mkdirs(t, filepath.Join(home, ".config/Nextcloud"))
		if err := os.Symlink(outside, filepath.Join(home, nextcloudCfg)); err != nil {
			t.Fatal(err)
		}
		if _, err := keepNextcloud(openTest(t, top), openTest(t, root), "example"); !errors.Is(err, safefs.ErrNotPlain) {
			t.Errorf("linked settings: %v", err)
		}
	})

	t.Run("nextcloud new", func(t *testing.T) {
		root, home := newHomeWith(t, "ncnew", true)
		if err := renewNextcloud(openTest(t, root), "example"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(home, nextcloudCfg)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("settings left: %v", err)
		}
		root, _ = newHomeWith(t, "ncnewnone", false)
		if err := renewNextcloud(openTest(t, root), "example"); err != nil {
			t.Errorf("no settings: %v", err)
		}
		// A settings folder planted as a link is not followed.
		root, home = newHomeWith(t, "ncnewlink", false)
		mkdirs(t, filepath.Join(home, ".config"), filepath.Join(dir, "elsewhere"))
		if err := os.Symlink(outside, filepath.Join(dir, "elsewhere/nextcloud.cfg")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(home, ".config/Nextcloud")); err != nil {
			t.Fatal(err)
		}
		if err := renewNextcloud(openTest(t, root), "example"); !errors.Is(err, safefs.ErrNotPlain) {
			t.Errorf("linked settings folder: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "elsewhere/nextcloud.cfg")); err != nil {
			t.Errorf("removed through a link: %v", err)
		}
	})

	t.Run("networks", func(t *testing.T) {
		fixture := func(name string) string {
			b, err := os.ReadFile(filepath.Join("../installer/move/identity/testdata", name+".nmconnection"))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		company := strings.Replace(fixture("companywifi"), "ca-cert=/home/joe/.cert/corp.crt", "ca-cert=corp.crt", 1)
		// oldNM makes an old disk with the given profile files.
		oldNM := func(files map[string]string, mode os.FileMode) string {
			old := t.TempDir()
			nm := filepath.Join(old, identity.ProfileDir)
			mkdirs(t, nm)
			for n, s := range files {
				if err := os.WriteFile(filepath.Join(nm, n), []byte(s), mode); err != nil {
					t.Fatal(err)
				}
			}
			return old
		}
		old := oldNM(map[string]string{
			"home.nmconnection":    fixture("personal-wifi"),
			"corp.nmconnection":    company,
			"openvpn.nmconnection": fixture("redhat-openvpn"),
		}, 0o600)
		nm := filepath.Join(old, identity.ProfileDir)
		if err := os.WriteFile(filepath.Join(nm, "corp.crt"), []byte("cert"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Others may read it: NetworkManager does not load it, so it is
		// not counted.
		if err := os.WriteFile(filepath.Join(nm, "open.nmconnection"), []byte(fixture("personal-wifi")), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(nm, "link.nmconnection")); err != nil {
			t.Fatal(err)
		}
		// A FIFO is not opened: the read would hang.
		if err := unix.Mkfifo(filepath.Join(nm, "fifo.nmconnection"), 0o600); err != nil {
			t.Fatal(err)
		}

		root, _ := newHomeWith(t, "nm", false)
		newNM := filepath.Join(root, newProfileDir)
		for it, files := range map[identity.Item][]string{
			identity.WiFi:           {"home.nmconnection"},
			identity.WiFiEnterprise: {"corp.nmconnection", "corp.crt"},
		} {
			if left, err := keepNetworks(openTest(t, old), openTest(t, root), it); err != nil || left != 0 {
				t.Errorf("%s: left %d, %v", it, left, err)
			}
			for _, f := range files {
				st, err := os.Lstat(filepath.Join(newNM, f))
				if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
					t.Errorf("%s: %v %v", f, st, err)
				}
			}
		}
		if st, err := os.Lstat(newNM); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("profile folder: %v %v", st, err)
		}
		b, _ := os.ReadFile(filepath.Join(newNM, "home.nmconnection"))
		want, _ := identity.CleanProfile([]byte(fixture("personal-wifi")))
		if string(b) != string(want.Data) {
			t.Errorf("written profile:\n%s", b)
		}
		names, _ := os.ReadDir(newNM)
		if len(names) != 3 {
			t.Errorf("written: %v", names)
		}
		// A plugin VPN stays behind; with nothing else the item fails.
		if left, err := keepNetworks(openTest(t, old), openTest(t, root), identity.VPN); left != 1 || err == nil || !strings.Contains(err.Error(), "none of its 1") {
			t.Errorf("plugin VPN: left %d, %v", left, err)
		}
		if _, err := os.Lstat(filepath.Join(newNM, "openvpn.nmconnection")); err == nil {
			t.Error("plugin VPN written")
		}
		// An old disk with none of the item.
		if _, err := keepNetworks(openTest(t, oldNM(nil, 0o600)), openTest(t, root), identity.WiFi); err == nil || !strings.Contains(err.Error(), "no such profile") {
			t.Errorf("no profile: %v", err)
		}

		// A certificate planted as a link is not read: the profile stays.
		linked := oldNM(map[string]string{"corp.nmconnection": company}, 0o600)
		if err := os.Symlink(outside, filepath.Join(linked, identity.ProfileDir, "corp.crt")); err != nil {
			t.Fatal(err)
		}
		root, _ = newHomeWith(t, "nmcert", false)
		if left, err := keepNetworks(openTest(t, linked), openTest(t, root), identity.WiFiEnterprise); left != 1 || err == nil {
			t.Errorf("linked cert: left %d, %v", left, err)
		}
		if _, err := os.Lstat(filepath.Join(root, newProfileDir, "corp.nmconnection")); err == nil {
			t.Error("profile of a linked cert written")
		}

		// A link planted at the new profile is refused and its target stays.
		root, _ = newHomeWith(t, "nmlink", false)
		newNM = filepath.Join(root, newProfileDir)
		if err := os.MkdirAll(newNM, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(newNM, "home.nmconnection")); err != nil {
			t.Fatal(err)
		}
		if left, err := keepNetworks(openTest(t, old), openTest(t, root), identity.WiFi); left != 1 || err == nil {
			t.Errorf("planted link: left %d, %v", left, err)
		}
		if b, _ := os.ReadFile(outside); string(b) != "keep" {
			t.Errorf("link target changed: %q", b)
		}
		// A profile folder planted as a link is refused before any write.
		root, _ = newHomeWith(t, "nmdirlink", false)
		mkdirs(t, filepath.Join(root, "etc/NetworkManager"), filepath.Join(dir, "nmelse"))
		if err := os.Symlink(filepath.Join(dir, "nmelse"), filepath.Join(root, newProfileDir)); err != nil {
			t.Fatal(err)
		}
		if _, err := keepNetworks(openTest(t, old), openTest(t, root), identity.WiFi); err == nil {
			t.Error("linked profile folder followed")
		}
		if names, _ := os.ReadDir(filepath.Join(dir, "nmelse")); len(names) != 0 {
			t.Errorf("written through a link: %v", names)
		}
	})

	t.Run("bluetooth", func(t *testing.T) {
		raw, err := os.ReadFile("../installer/move/identity/testdata/bluez-headset.info")
		if err != nil {
			t.Fatal(err)
		}
		// Addresses from the documentation range of RFC 7042.
		const here, other = "00:00:5E:00:53:01", "00:00:5E:00:53:02"
		dev := func(n int) string { return fmt.Sprintf("00:00:5E:00:53:%02X", 0x10+n) }
		old := t.TempDir()
		bz := filepath.Join(old, identity.BluezDir)
		info := func(adapter, device, s string) {
			mkdirs(t, filepath.Join(bz, adapter, device))
			if err := os.WriteFile(filepath.Join(bz, adapter, device, "info"), []byte(s), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		info(here, dev(1), string(raw))
		// Seen, never paired: not carried, not counted.
		info(here, dev(2), "[General]\nName=Example\n")
		// Refused by the allowlist: counted.
		info(here, dev(3), strings.Replace(string(raw), "Key=00112233445566778899AABBCCDDEEFF", "Key=0011", 1))
		// A link and a FIFO are not opened.
		mkdirs(t, filepath.Join(bz, here, dev(4)), filepath.Join(bz, here, dev(5)))
		if err := os.Symlink(outside, filepath.Join(bz, here, dev(4), "info")); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(bz, here, dev(5), "info"), 0o600); err != nil {
			t.Fatal(err)
		}
		// One others could write is not read: counted.
		info(here, dev(7), string(raw))
		if err := os.Chmod(filepath.Join(bz, here, dev(7), "info"), 0o622); err != nil {
			t.Fatal(err)
		}
		// A pairing of an adapter this computer does not have stays.
		info(other, dev(6), string(raw))
		if err := os.WriteFile(filepath.Join(bz, here, "settings"), []byte("[General]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		this := map[string]bool{here: true}

		root, _ := newHomeWith(t, "bt", false)
		newBZ := filepath.Join(root, newBluezDir)
		if left, err := keepBluetooth(openTest(t, old), openTest(t, root), this); err != nil || left != 2 {
			t.Errorf("left %d, %v", left, err)
		}
		b, err := os.ReadFile(filepath.Join(newBZ, here, dev(1), "info"))
		want, _ := identity.CleanBluezInfo(raw)
		if err != nil || string(b) != string(want) {
			t.Errorf("written info %v:\n%s", err, b)
		}
		for _, f := range []string{"", here, here + "/" + dev(1)} {
			if st, err := os.Lstat(filepath.Join(newBZ, f)); err != nil || st.Mode().Perm() != 0o700 {
				t.Errorf("folder %q: %v %v", f, st, err)
			}
		}
		if st, err := os.Lstat(filepath.Join(newBZ, here, dev(1), "info")); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("info: %v %v", st, err)
		}
		for _, f := range []string{other, here + "/" + dev(2), here + "/" + dev(3), here + "/" + dev(4), here + "/" + dev(5), here + "/" + dev(7), here + "/settings"} {
			if _, err := os.Lstat(filepath.Join(newBZ, f)); err == nil {
				t.Errorf("%s written", f)
			}
		}
		// None of this computer's adapters.
		if _, err := keepBluetooth(openTest(t, old), openTest(t, root), map[string]bool{}); err == nil || !strings.Contains(err.Error(), "no pairing") {
			t.Errorf("other computer: %v", err)
		}

		// A link planted at the new info is refused and its target stays.
		root, _ = newHomeWith(t, "btlink", false)
		newBZ = filepath.Join(root, newBluezDir)
		mkdirs(t, filepath.Join(newBZ, here, dev(1)))
		if err := os.Symlink(outside, filepath.Join(newBZ, here, dev(1), "info")); err != nil {
			t.Fatal(err)
		}
		if left, err := keepBluetooth(openTest(t, old), openTest(t, root), this); left != 3 || err == nil {
			t.Errorf("planted link: left %d, %v", left, err)
		}
		if b, _ := os.ReadFile(outside); string(b) != "keep" {
			t.Errorf("link target changed: %q", b)
		}
		// A device folder planted as a link is not followed.
		root, _ = newHomeWith(t, "btdirlink", false)
		newBZ = filepath.Join(root, newBluezDir)
		mkdirs(t, filepath.Join(newBZ, here), filepath.Join(dir, "btelse"))
		if err := os.Symlink(filepath.Join(dir, "btelse"), filepath.Join(newBZ, here, dev(1))); err != nil {
			t.Fatal(err)
		}
		if _, err := keepBluetooth(openTest(t, old), openTest(t, root), this); err == nil {
			t.Error("linked device folder followed")
		}
		if names, _ := os.ReadDir(filepath.Join(dir, "btelse")); len(names) != 0 {
			t.Errorf("written through a link: %v", names)
		}
	})
}

package identity

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/safefs"
	"golang.org/x/sys/unix"
)

// Places on the old disk, below the btrfs top level.
const (
	nmDir        = "@/etc/NetworkManager/system-connections"
	bluezDir     = "@/var/lib/bluetooth"
	fprintDir    = "@/var/lib/fprint"
	tailscaleRel = "@/var/lib/tailscale/tailscaled.state"
	machineIDRel = "@/etc/machine-id"
	homeDir      = "@home"
	nextcloudRel = ".config/Nextcloud/nextcloud.cfg"
)

var sshHostKeys = []string{"@/etc/ssh/ssh_host_rsa_key", "@/etc/ssh/ssh_host_ecdsa_key", "@/etc/ssh/ssh_host_ed25519_key"}

// Caps. A real system stays far below each; a crafted one cannot make root
// list or read without end.
const (
	maxProfiles   = 4096
	maxProfile    = 64 << 10
	maxAdapters   = 16
	maxAdapterDir = 1024
	maxFprintDirs = 64
	maxFingers    = 32
)

// rootUID is the owner NetworkManager demands of a profile; tests change it.
var rootUID = uint32(0)

var (
	btAddress = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)
	// fprintd names a finger file by the hex number of the finger, 1 to a.
	fingerName = regexp.MustCompile(`^([1-9]|a)$`)
)

// Scan counts the identity items below top, the btrfs top level of the old
// disk, mounted read-only. username is the old account (oldroot.Info);
// adapters holds the Bluetooth addresses of this computer, uppercase. Scan
// never fails: what is missing counts zero, what is odd counts in Skipped.
func Scan(top *safefs.Dir, username string, adapters map[string]bool) Inventory {
	s := scanner{top: top}
	s.networks()
	s.bluetooth(adapters)
	if config.ValidUsername(username) {
		s.fingerprints(fprintDir + "/" + username)
		s.inv.Nextcloud = s.plain(homeDir + "/" + username + "/" + nextcloudRel)
	} else {
		s.inv.Skipped++
	}
	s.inv.Tailscale = s.plain(tailscaleRel)
	for _, k := range sshHostKeys {
		if s.plain(k) {
			s.inv.SSHHostKeys++
		}
	}
	s.inv.MachineID = s.machineID()
	return s.inv
}

type scanner struct {
	top *safefs.Dir
	inv Inventory
}

// odd tells whether err stops the look at one place. A missing place is
// simply not there; anything else is counted as skipped.
func (s *scanner) odd(err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, unix.ENOENT) {
		s.inv.Skipped++
	}
	return true
}

// plain tells whether rel is a plain, non-empty file.
func (s *scanner) plain(rel string) bool {
	st, err := s.top.Lstat(rel)
	if s.odd(err) {
		return false
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size == 0 {
		s.inv.Skipped++
		return false
	}
	return true
}

// folder lists rel when it is a real folder.
func (s *scanner) folder(rel string, max int) ([]string, bool) {
	names, err := s.top.Names(rel, max)
	if s.odd(err) {
		return nil, false
	}
	return names, true
}

func (s *scanner) networks() {
	names, ok := s.folder(nmDir, maxProfiles)
	if !ok {
		return
	}
	for _, n := range names {
		// NetworkManager ignores hidden files; it writes *.nmconnection.
		if strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".nmconnection") {
			s.inv.Skipped++
			continue
		}
		rel := nmDir + "/" + n
		st, err := s.top.Lstat(rel)
		if s.odd(err) {
			continue
		}
		// NetworkManager loads only root-owned profiles nobody else can
		// read; any other file is not a live profile on the old system.
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != rootUID || st.Mode&0o077 != 0 {
			s.inv.Skipped++
			continue
		}
		raw, err := s.top.ReadFile(rel, maxProfile)
		if s.odd(err) {
			continue
		}
		switch profileKind(raw) {
		case kindWiFi:
			s.inv.WiFi++
		case kindWiFiEnterprise:
			s.inv.WiFiEnterprise++
		case kindVPN:
			s.inv.VPN++
		case kindBad:
			s.inv.Skipped++
		}
		clear(raw)
	}
}

func (s *scanner) bluetooth(adapters map[string]bool) {
	names, ok := s.folder(bluezDir, maxAdapters)
	if !ok {
		return
	}
	for _, a := range names {
		if !btAddress.MatchString(a) {
			continue
		}
		devices, ok := s.folder(bluezDir+"/"+a, maxAdapterDir)
		if !ok {
			continue
		}
		for _, d := range devices {
			// Next to the devices BlueZ keeps "settings" and "cache".
			if !btAddress.MatchString(d) || !s.plain(bluezDir+"/"+a+"/"+d+"/info") {
				continue
			}
			if adapters[a] {
				s.inv.Bluetooth++
			} else {
				s.inv.BluetoothOther++
			}
		}
	}
}

// fingerprints counts <driver>/<device>/<finger> files below the account
// folder of fprintd.
func (s *scanner) fingerprints(user string) {
	drivers, ok := s.folder(user, maxFprintDirs)
	if !ok {
		return
	}
	for _, dr := range drivers {
		devs, ok := s.folder(user+"/"+dr, maxFprintDirs)
		if !ok {
			continue
		}
		for _, dev := range devs {
			dir := user + "/" + dr + "/" + dev
			fingers, ok := s.folder(dir, maxFingers)
			if !ok {
				continue
			}
			for _, f := range fingers {
				if !fingerName.MatchString(f) {
					s.inv.Skipped++
					continue
				}
				if s.plain(dir + "/" + f) {
					s.inv.Fingerprints++
				}
			}
		}
	}
}

// machineID is true for a valid machine-id(5): 32 lowercase hex digits and
// a newline, not all zero. The value never leaves this function.
func (s *scanner) machineID() bool {
	raw, err := s.top.ReadFile(machineIDRel, 64)
	if s.odd(err) {
		return false
	}
	defer clear(raw)
	if !validMachineID(raw) {
		s.inv.Skipped++
		return false
	}
	return true
}

// validMachineID is true for 32 lowercase hex digits and a newline, not
// all zero.
func validMachineID(raw []byte) bool {
	id, ok := bytes.CutSuffix(raw, []byte("\n"))
	if !ok || len(id) != 32 || bytes.Count(id, []byte("0")) == 32 {
		return false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// ReadMachineID reads the old disk's machine-id below top, checked as Scan
// checks it: exactly what goes into the new disk's /etc/machine-id. The
// caller clears it after use.
func ReadMachineID(top *safefs.Dir) ([]byte, error) {
	raw, err := top.ReadFile(machineIDRel, 64)
	if err != nil {
		return nil, err
	}
	if !validMachineID(raw) {
		clear(raw)
		return nil, errors.New("the old machine-id is not a valid machine-id")
	}
	return raw, nil
}

type kind int

const (
	kindOther kind = iota // a profile that is not an identity item (ethernet, bridge, ...)
	kindBad               // not a profile NetworkManager would load
	kindWiFi
	kindWiFiEnterprise
	kindVPN
)

// profileKind reads the connection type of a keyfile, nm-settings-keyfile(5):
// the GLib key file format, comments with #, [group] lines and key=value
// lines. Anything else makes the whole file bad, as it does for
// NetworkManager. Only the type and the presence of [802-1x] are looked at.
func profileKind(raw []byte) kind {
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return kindBad
	}
	group, typ, enterprise, typed := "", "", false, false
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimRight(strings.TrimLeft(line, " \t"), "\r")
		switch {
		case line == "" || line[0] == '#':
		case line[0] == '[':
			if len(line) < 3 || line[len(line)-1] != ']' || strings.ContainsAny(line[1:len(line)-1], "[]") {
				return kindBad
			}
			group = line[1 : len(line)-1]
			if group == "802-1x" {
				enterprise = true
			}
		default:
			key, value, ok := strings.Cut(line, "=")
			key = strings.TrimRight(key, " \t")
			if !ok || key == "" || group == "" {
				return kindBad
			}
			if group != "connection" || key != "type" {
				continue
			}
			value = strings.TrimSpace(value)
			if typed && value != typ {
				return kindBad
			}
			typ, typed = value, true
		}
	}
	switch typ {
	case "":
		return kindBad
	case "wifi", "802-11-wireless":
		if enterprise {
			return kindWiFiEnterprise
		}
		return kindWiFi
	case "vpn", "wireguard":
		return kindVPN
	}
	return kindOther
}

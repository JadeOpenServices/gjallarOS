package nettrust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"
)

// PausePath holds the pause the settings service granted. It lives in /run
// only, so a reboot ends it. The settings service may write its folder and
// nothing else in /run.
var PausePath = "/run/gjallar/vpn-pause/pause.json"

// pauseOwner must own the pause file; tests use their own uid.
var pauseOwner = uint32(0)

// MaxPause is the longest a pause may last.
const MaxPause = time.Hour

// Pause lets one network carry the traffic without the tunnel while the
// VPN is blocked there. It holds only while the network key matches.
type Pause struct {
	Network string    `json:"network"`
	User    string    `json:"user"`
	UID     uint32    `json:"uid"`
	Started time.Time `json:"started"`
	Until   time.Time `json:"until"`
}

// PauseState is the pause as the verdict shows it.
type PauseState struct {
	User  string    `json:"user"`
	Until time.Time `json:"until"`
	// Blocked is the reason the network would be blocked without it.
	Blocked string `json:"blocked"`
}

// NetworkKey names a network for a pause: device, Wi-Fi name and key
// management, gateway address and hardware address, and the subnets. A
// Wi-Fi name alone can be cloned; any of these changing ends the pause.
// Empty without a network or without the gateway's hardware address.
func NetworkKey(n *Network) string {
	if n == nil || n.GatewayMAC == "" {
		return ""
	}
	subnets := make([]string, 0, len(n.Subnets))
	for _, prefix := range n.Subnets {
		subnets = append(subnets, prefix.Masked().String())
	}
	slices.Sort(subnets)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		n.Device, n.SSID, n.KeyMgmt, n.Gateway.String(), strings.ToLower(n.GatewayMAC), strings.Join(subnets, ","),
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// Holds reports whether p still lets network key carry the traffic at now.
func (p Pause) Holds(key string, now time.Time) bool {
	return key != "" && p.Network == key &&
		!now.Before(p.Started) && now.Before(p.Until) &&
		p.Until.Sub(p.Started) <= MaxPause
}

// LoadPause reads the pause file. It counts only when root wrote it and
// nobody else can change it; ok is false when there is none.
func LoadPause(path string) (p Pause, ok bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Pause{}, false, nil
	}
	if err != nil {
		return Pause{}, false, err
	}
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !isStat || st.Uid != pauseOwner || info.Mode().Perm()&0o022 != 0 {
		return Pause{}, false, fmt.Errorf("%s is not a root-only file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Pause{}, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return Pause{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return p, true, nil
}

// WritePause replaces the pause file.
func WritePause(path string, p Pause) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RemovePause ends the pause; no file is not an error.
func RemovePause(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

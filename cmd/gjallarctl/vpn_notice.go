package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

var (
	vpnShowNotice = showNotice
	vpnPauseCall  = func() (string, error) { return vpnSettingsCall("PauseVPN") }
	// vpnNoticePoll is how often an open notice checks that it still applies.
	vpnNoticePoll = 2 * time.Second
)

// runVPNNotice shows one notice when the VPN blocks the network this device
// joined, with a way to pause the block there. It runs at login and each
// time the trust decision changes (apps/tailscale/home.nix). A network gets
// one notice per join: a blocked network that stays blocked, unblocks or
// blocks again stays quiet until the device leaves it.
func runVPNNotice(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, vpnUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		v, err := readVPNVerdict()
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		seen := readVPNNoticeSeen()
		show, next := vpnNoticeStep(v, seen)
		if next != seen {
			if err := writeVPNNoticeSeen(next); err != nil {
				fmt.Fprintf(stderr, "ERROR: %v\n", err)
				return 1
			}
		}
		if !show {
			return 0
		}
		choice, err := showVPNNotice(ctx, v)
		if ctx.Err() != nil {
			return 0
		}
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		if choice == "pause" {
			if _, err := vpnPauseCall(); err != nil {
				fmt.Fprintf(stderr, "VPN pause: %v\n", err)
				vpnShowNotice(ctx, desktopNotice{
					App:     "GjallarOS",
					Icon:    "network-vpn",
					Summary: "VPN not paused",
					Body:    html.EscapeString(err.Error()),
					Actions: []noticeAction{{"ok", "OK"}},
				})
			}
		}
		// The network may have changed while the notice was open.
	}
}

// vpnNoticeStep decides from the verdict and the network the last notice
// was for whether to show one now, and what to remember.
func vpnNoticeStep(v nettrust.Verdict, seen string) (show bool, next string) {
	key := vpnNoticeKey(v)
	switch {
	case key == seen:
		return false, seen
	case v.Blocked == "":
		// Another network, not blocked: the device left the noticed one.
		return false, ""
	case key == "":
		return false, seen
	}
	return true, key
}

// vpnNoticeKey names the network for the notice. A network without a key
// (no gateway hardware address) cannot be paused, but still gets its one
// notice, by device.
func vpnNoticeKey(v nettrust.Verdict) string {
	if v.Network != "" {
		return v.Network
	}
	if v.Device != "" {
		return "device:" + v.Device
	}
	return ""
}

// showVPNNotice shows the notice and closes it when the network changes or
// the block ends while it is open.
func showVPNNotice(ctx context.Context, v nettrust.Verdict) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	key := vpnNoticeKey(v)
	go func() {
		tick := time.NewTicker(vpnNoticePoll)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				now, err := readVPNVerdict()
				if err != nil || now.Blocked == "" || vpnNoticeKey(now) != key {
					cancel()
					return
				}
			}
		}
	}()
	summary, body := vpnNoticeText(v)
	actions := []noticeAction{{"ok", "OK"}}
	if v.Network != "" {
		actions = []noticeAction{{"pause", "Pause VPN on this network"}, {"ok", "Keep blocked"}}
	}
	choice, err := vpnShowNotice(ctx, desktopNotice{
		App:     "GjallarOS",
		Icon:    "network-vpn",
		Summary: summary,
		Body:    body,
		Actions: actions,
	})
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		// Closed because it no longer applies; the caller looks again.
		return "", nil
	}
	return choice, err
}

// vpnNoticeText says which side failed and what a pause gives away. The
// notice popup shows about seven short lines, so the risk comes first and
// the summary already says the network is blocked.
func vpnNoticeText(v nettrust.Verdict) (summary, body string) {
	reason, _, _ := strings.Cut(nettrust.BlockText(v.Blocked), ";")
	if reason != "" {
		reason = strings.ToUpper(reason[:1]) + reason[1:] + "."
	}
	if v.SSID != "" {
		body = "Wi-Fi " + html.EscapeString(v.SSID) + ": " + reason
	} else {
		body = reason
	}
	if v.Network == "" {
		return "VPN blocking this network", body + "\nThis network cannot be told apart from others, so it cannot be paused."
	}
	return "VPN blocking this network", body + fmt.Sprintf(
		"\nPausing (max %d min, admin password) skips the tunnel here; this network then sees where apps connect.",
		int(nettrust.MaxPause.Minutes()))
}

func readVPNVerdict() (nettrust.Verdict, error) {
	var v nettrust.Verdict
	data, err := os.ReadFile(vpnStatusPath)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("parse %s: %w", vpnStatusPath, err)
	}
	return v, nil
}

func vpnNoticeSeenPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "gjallar", "vpn-notice-network")
}

// readVPNNoticeSeen returns the network the last notice was for. A missing
// or broken file only means the notice comes once more.
func readVPNNoticeSeen() string {
	data, _ := os.ReadFile(vpnNoticeSeenPath())
	return strings.TrimSpace(string(data))
}

func writeVPNNoticeSeen(key string) error {
	path := vpnNoticeSeenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

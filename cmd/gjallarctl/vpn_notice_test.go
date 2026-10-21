package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

// Network keys, Wi-Fi names and device names here are SYNTHETIC.

func TestVPNNoticeStepOncePerJoin(t *testing.T) {
	blockedA := nettrust.Verdict{Blocked: nettrust.BlockExitNodeOffline, Network: "synthetic-net-a", Device: "wlan0"}
	pausedA := nettrust.Verdict{Network: "synthetic-net-a", Device: "wlan0", Paused: &nettrust.PauseState{}}
	openB := nettrust.Verdict{Network: "synthetic-net-b", Device: "wlan0"}
	offline := nettrust.Verdict{}
	noKey := nettrust.Verdict{Blocked: nettrust.BlockTailscaleDown, Device: "eth0"}

	seen := ""
	steps := []struct {
		v    nettrust.Verdict
		show bool
	}{
		{blockedA, true},
		{blockedA, false}, // rewritten verdict, same network
		{pausedA, false},
		{blockedA, false}, // pause ended on the same network
		{openB, false},    // left it
		{blockedA, true},  // joined again
		{offline, false},
		{noKey, true},
		{noKey, false},
	}
	for i, s := range steps {
		show, next := vpnNoticeStep(s.v, seen)
		if show != s.show {
			t.Fatalf("step %d: show = %v, want %v (seen %q)", i, show, s.show, seen)
		}
		seen = next
	}
}

func TestVPNNoticeText(t *testing.T) {
	summary, body := vpnNoticeText(nettrust.Verdict{Blocked: nettrust.BlockExitNodeOffline, Network: "synthetic-net-a", SSID: "Synthetic <Cafe>"})
	if summary != "VPN blocking this network" ||
		!strings.Contains(body, "Wi-Fi Synthetic &lt;Cafe&gt;") ||
		!strings.Contains(body, "The exit node is offline;") ||
		!strings.Contains(body, "without the tunnel for up to 60 minutes") {
		t.Fatalf("summary %q body %q", summary, body)
	}
	_, body = vpnNoticeText(nettrust.Verdict{Blocked: nettrust.BlockTailscaleDown, Device: "eth0"})
	if !strings.Contains(body, "this network. Tailscale is not running") || !strings.Contains(body, "cannot be paused") {
		t.Fatalf("body %q", body)
	}
}

func fakeVPNNotice(t *testing.T, verdict string) (posted *[]desktopNotice, choose func(string), paused *int) {
	t.Helper()
	fakeVPNPaths(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.WriteFile(vpnStatusPath, []byte(verdict), 0o644); err != nil {
		t.Fatal(err)
	}
	oldShow, oldPause, oldPoll := vpnShowNotice, vpnPauseCall, vpnNoticePoll
	t.Cleanup(func() { vpnShowNotice, vpnPauseCall, vpnNoticePoll = oldShow, oldPause, oldPoll })
	var notices []desktopNotice
	answer := "ok"
	vpnShowNotice = func(ctx context.Context, n desktopNotice) (string, error) {
		notices = append(notices, n)
		return answer, nil
	}
	n := 0
	vpnPauseCall = func() (string, error) {
		n++
		// The service paused it: the next decision is no longer blocked.
		return "{}", os.WriteFile(vpnStatusPath, []byte(`{"trust":"untrusted","bypass":[],"network":"synthetic-net-a","paused":{"user":"synthetic-user"}}`), 0o644)
	}
	return &notices, func(a string) { answer = a }, &n
}

const blockedVerdictJSON = `{"trust":"untrusted","bypass":[],"blocked":"exit-node-offline","network":"synthetic-net-a","device":"wlan0"}`

func TestVPNNoticePauses(t *testing.T) {
	posted, choose, paused := fakeVPNNotice(t, blockedVerdictJSON)
	choose("pause")
	var errOut bytes.Buffer
	if code := runVPNNotice(nil, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if len(*posted) != 1 || *paused != 1 || (*posted)[0].Actions[0].Key != "pause" {
		t.Fatalf("posted %+v, paused %d", *posted, *paused)
	}
	// Login again on the same network: quiet.
	if code := runVPNNotice(nil, &errOut); code != 0 || len(*posted) != 1 {
		t.Fatalf("second run posted %d notices", len(*posted))
	}
}

func TestVPNNoticeKeepBlocked(t *testing.T) {
	posted, _, paused := fakeVPNNotice(t, blockedVerdictJSON)
	var errOut bytes.Buffer
	for range 2 {
		if code := runVPNNotice(nil, &errOut); code != 0 {
			t.Fatalf("code %d: %s", code, errOut.String())
		}
	}
	if len(*posted) != 1 || *paused != 0 {
		t.Fatalf("posted %d, paused %d", len(*posted), *paused)
	}
}

func TestVPNNoticePauseRefused(t *testing.T) {
	posted, choose, _ := fakeVPNNotice(t, blockedVerdictJSON)
	choose("pause")
	vpnPauseCall = func() (string, error) { return "", errors.New("synthetic refusal") }
	var errOut bytes.Buffer
	if code := runVPNNotice(nil, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if len(*posted) != 2 || (*posted)[1].Summary != "VPN not paused" || (*posted)[1].Body != "synthetic refusal" {
		t.Fatalf("posted %+v", *posted)
	}
}

func TestVPNNoticeClosesWhenTheNetworkChanges(t *testing.T) {
	posted, _, _ := fakeVPNNotice(t, blockedVerdictJSON)
	vpnNoticePoll = 10 * time.Millisecond
	vpnShowNotice = func(ctx context.Context, n desktopNotice) (string, error) {
		*posted = append(*posted, n)
		if len(*posted) == 1 {
			if err := os.WriteFile(vpnStatusPath, []byte(`{"trust":"untrusted","bypass":[],"network":"synthetic-net-b"}`), 0o644); err != nil {
				t.Error(err)
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "", errors.New("the notice stayed open")
		}
	}
	var errOut bytes.Buffer
	if code := runVPNNotice(nil, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if len(*posted) != 1 || readVPNNoticeSeen() != "" {
		t.Fatalf("posted %d, seen %q", len(*posted), readVPNNoticeSeen())
	}
}

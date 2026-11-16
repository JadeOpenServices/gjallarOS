package stick

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/region"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickkeyboard"
)

// fakeNet is a made-up NetworkManager. devices returns the device list for
// the n-th look (counting from 0).
type fakeNet struct {
	devices      func(n int) string
	connectivity string
	wifiList     string
	looks        int
	calls        []string
	// interactive answers programs run on the terminal; nil means success.
	interactive func(name string, args []string) error
}

func (f *fakeNet) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch call {
	case "nmcli -t -f DEVICE,TYPE,STATE device":
		out := f.devices(f.looks)
		f.looks++
		return []byte(out), nil
	case "nmcli -t networking connectivity check":
		return []byte(f.connectivity + "\n"), nil
	case "nmcli -t -f IN-USE,SSID,SIGNAL,SECURITY,BSSID device wifi list --rescan yes":
		return []byte(f.wifiList), nil
	}
	return nil, fmt.Errorf("unexpected %s", call)
}

func (f *fakeNet) Interactive(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, "RUN "+name+" "+strings.Join(args, " "))
	if f.interactive != nil {
		return f.interactive(name, args)
	}
	return nil
}

func (f *fakeNet) ran(prefix string) []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "RUN "+prefix) {
			out = append(out, strings.TrimPrefix(c, "RUN "))
		}
	}
	return out
}

func always(s string) func(int) string { return func(int) string { return s } }

const (
	cable   = "eth0:ethernet:connected\nlo:loopback:connected (externally)\n"
	nothing = "eth0:ethernet:unavailable\nlo:loopback:connected (externally)\n"
	wifiOff = "wlan0:wifi:disconnected\n"
	wifiOn  = "wlan0:wifi:connected\n"
)

// harness is one menu run with typed input and a log in a test folder.
type harness struct {
	s      *Stick
	net    *fakeNet
	screen *bytes.Buffer
	log    *installlog.Log
	layout string
}

func newHarness(t *testing.T, mode, input string, net *fakeNet) *harness {
	t.Helper()
	l, err := installlog.OpenStick(filepath.Join(t.TempDir(), "logs"), time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	installlog.SetCurrent(l)
	t.Cleanup(func() { installlog.SetCurrent(nil); l.Close() })
	h := &harness{net: net, screen: &bytes.Buffer{}, log: l, layout: "de"}
	h.s = &Stick{
		Cmd:   net,
		Mode:  mode,
		Sleep: func() {},
		ReadLayout: func() (string, error) {
			if h.layout == "" {
				return "", stickkeyboard.ErrNone
			}
			return h.layout, nil
		},
		WriteLayout: func(v string) error { h.layout = v; return nil },
		ErrorLog:    l.Tee(io.Discard),
	}
	h.s.UI.Reader = bufio.NewReader(strings.NewReader(input))
	h.s.UI.Out = h.screen
	return h
}

func (h *harness) run(t *testing.T) error {
	t.Helper()
	err := h.s.Run(context.Background())
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrRestartDisplay) {
		t.Fatalf("Run: %v\nscreen:\n%s", err, h.screen)
	}
	return err
}

func (h *harness) logText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.log.Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSplitTerse(t *testing.T) {
	got := splitTerse(`a\:b:c\\d:`)
	want := []string{"a:b", `c\d`, ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("splitTerse = %q, want %q", got, want)
	}
}

func TestParseDevices(t *testing.T) {
	for _, c := range []struct {
		out  string
		via  string
		wifi bool
	}{
		{cable + wifiOff, "ethernet", true},
		{wifiOn + cable, "ethernet", true},
		{wifiOn, "wifi", true},
		{"wlan0:wifi:unavailable\n", "", false},
		{"wlan0:wifi:unmanaged\n" + nothing, "", false},
		{"eth0:ethernet:connected (externally)\n", "ethernet", false},
		{"garbage\n", "", false},
	} {
		via, wifi := parseDevices(c.out)
		if via != c.via || wifi != c.wifi {
			t.Errorf("parseDevices(%q) = %q, %v; want %q, %v", c.out, via, wifi, c.via, c.wifi)
		}
	}
}

func TestParseWifi(t *testing.T) {
	lines := []string{
		` :Cafe:40:WPA2:AA\:BB\:CC\:DD\:EE\:01`,
		` :Cafe:70:WPA2:aa\:bb\:cc\:dd\:ee\:02`,
		` :Cafe:90:--:AA\:BB\:CC\:DD\:EE\:03`,
		` ::99:WPA2:AA\:BB\:CC\:DD\:EE\:04`,
		` :--:99:WPA2:AA\:BB\:CC\:DD\:EE\:05`,
		` :Bad:99:WPA2:not-a-bssid`,
		` :Bad:x:WPA2:AA\:BB\:CC\:DD\:EE\:06`,
		` :Home:70:WPA3:AA\:BB\:CC\:DD\:EE\:07`,
	}
	got := parseWifi(strings.Join(lines, "\n"))
	want := []wifiNetwork{
		{ssid: "Cafe", security: "", bssid: "AA:BB:CC:DD:EE:03", signal: 90},
		{ssid: "Cafe", security: "WPA2", bssid: "AA:BB:CC:DD:EE:02", signal: 70},
		{ssid: "Home", security: "WPA3", bssid: "AA:BB:CC:DD:EE:07", signal: 70},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parseWifi = %+v\nwant %+v", got, want)
	}

	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, fmt.Sprintf(` :n%02d:%d:WPA2:AA\:BB\:CC\:DD\:EE\:%02X`, i, i, i))
	}
	if got := parseWifi(strings.Join(many, "\n")); len(got) != maxNetworks || got[0].ssid != "n29" {
		t.Fatalf("cap: got %d networks, first %q", len(got), got[0].ssid)
	}
}

// A neighbour's network name must not be able to move the cursor, clear the
// screen or flip the text direction of the menu.
func TestShowName(t *testing.T) {
	for in, want := range map[string]string{
		"Home":                  "Home",
		"\x1b[2J\x1b[H":         "?[2J?[H",
		"ab\u202ecd":            "ab?cd",
		"bad\xffutf8":           "bad?utf8",
		"tab\there":             "tab?here",
		"Café":                  "Café",
		strings.Repeat("x", 40): strings.Repeat("x", 32),
	} {
		if got := showName(in); got != want {
			t.Errorf("showName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOnlineGoesToMenuAndMarksOnce(t *testing.T) {
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "6\n", net) // Network, then end of input
	if err := h.run(t); !errors.Is(err, io.EOF) {
		t.Fatalf("Run = %v, want end of input", err)
	}
	log := h.logText(t)
	if !strings.Contains(log, "GJA-2010 ") || !strings.Contains(log, "mode=window") {
		t.Errorf("no display anchor:\n%s", log)
	}
	if n := strings.Count(log, "GJA-2020 "); n != 1 || !strings.Contains(log, "via=ethernet") {
		t.Errorf("online anchor %d times, want 1:\n%s", n, log)
	}
	if strings.Contains(h.screen.String(), "Keyboard layout:") {
		t.Error("asked for the keyboard although a layout was stored")
	}
}

func TestLookAgainFindsCable(t *testing.T) {
	net := &fakeNet{connectivity: "full", devices: func(n int) string {
		if n < 3 {
			return nothing
		}
		return cable
	}}
	h := newHarness(t, ModeConsole, "1\n", net) // Look again
	h.run(t)
	log := h.logText(t)
	for _, want := range []string{"GJA-2021 ", "tries=1", "GJA-2020 ", "via=ethernet"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if !strings.Contains(h.screen.String(), "gjallarOS installer") {
		t.Error("menu not shown after the cable came up")
	}
}

func TestInstallWithoutNetworkAsksAgain(t *testing.T) {
	net := &fakeNet{devices: always(nothing)}
	// Go on without; Install; Go on without again; end of input.
	h := newHarness(t, ModeConsole, "2\n1\n2\n", net)
	h.run(t)
	if got := net.ran("gjallar-recover"); len(got) != 0 {
		t.Fatalf("installer started offline: %q", got)
	}
	if n := strings.Count(h.logText(t), "GJA-2022 "); n != 2 {
		t.Errorf("no-network anchor %d times, want 2", n)
	}
	if !strings.Contains(h.screen.String(), "Installing needs a network connection.") {
		t.Error("no reason shown for asking again")
	}
}

func TestPortalAndNoInternetAreTold(t *testing.T) {
	for conn, want := range map[string]string{
		"portal":  "sign-in page",
		"limited": "internet cannot be reached",
		"none":    "internet cannot be reached",
	} {
		net := &fakeNet{devices: always(cable), connectivity: conn}
		h := newHarness(t, ModeConsole, "", net)
		h.run(t)
		if !strings.Contains(h.screen.String(), want) {
			t.Errorf("connectivity %s: screen lacks %q", conn, want)
		}
		if strings.Contains(h.logText(t), "GJA-2020 ") {
			t.Errorf("connectivity %s counted as online", conn)
		}
	}
}

func TestInstallChannels(t *testing.T) {
	for _, c := range []struct {
		input, want, anchor string
	}{
		{"1\n", "gjallar-recover guided fresh", ""},
		{"2\nstaging\n1\n", "gjallar-recover guided fresh --channel staging", "channel=staging"},
		{"2\n3\n1\n", "gjallar-recover guided fresh --channel dev", "channel=dev"},
		{"2\ndev\n2\nstable\n1\n", "gjallar-recover guided fresh", "channel=stable"},
	} {
		net := &fakeNet{devices: always(cable), connectivity: "full"}
		h := newHarness(t, ModeWindow, c.input, net)
		h.run(t)
		got := net.ran("gjallar-recover")
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("input %q: ran %q, want %q", c.input, got, c.want)
		}
		log := h.logText(t)
		if !strings.Contains(log, "choice=install") {
			t.Errorf("input %q: no install anchor", c.input)
		}
		if c.anchor != "" && !strings.Contains(log, c.anchor) {
			t.Errorf("input %q: log lacks %q", c.input, c.anchor)
		}
	}
}

func TestFailedStepIsLoggedWithoutItsOutput(t *testing.T) {
	exitThree := exec.Command("sh", "-c", "exit 3").Run()
	net := &fakeNet{devices: always(cable), connectivity: "full",
		interactive: func(string, []string) error { return exitThree }}
	h := newHarness(t, ModeWindow, "1\n", net)
	h.run(t)
	log := h.logText(t)
	if !strings.Contains(log, "GJA-2098 ") || !strings.Contains(log, "ERROR: the installer ended with exit code 3") {
		t.Errorf("failure not logged:\n%s", log)
	}
	if !strings.Contains(h.screen.String(), "You are back in the stick menu.") {
		t.Error("screen does not say where the user is")
	}
}

func TestOtherEntries(t *testing.T) {
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "3\n4\n7\n8\n", net)
	h.run(t)
	want := []string{
		"gjallar-recovery-console --from-stick",
		"runuser -l nixos",
		"systemctl reboot",
		"systemctl poweroff",
	}
	var got []string
	for _, c := range net.calls {
		if strings.HasPrefix(c, "RUN ") {
			got = append(got, strings.TrimPrefix(c, "RUN "))
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("ran %q, want %q", got, want)
	}
	log := h.logText(t)
	for _, w := range []string{"choice=recovery", "choice=shell"} {
		if !strings.Contains(log, w) {
			t.Errorf("log lacks %q", w)
		}
	}
}

// Loophole check: names of nearby networks are shown on the screen only,
// never written to the log, and the connection is made by access point so
// no typed or scanned name reaches the command line.
func TestWifiNamesStayOutOfTheLog(t *testing.T) {
	const name = "NeighbourSecretNet"
	connected := false
	net := &fakeNet{
		connectivity: "full",
		wifiList:     ` :` + name + `:80:WPA2:AA\:BB\:CC\:DD\:EE\:FF` + "\n",
		devices: func(int) string {
			if connected {
				return wifiOn
			}
			return wifiOff
		},
		interactive: func(string, []string) error { connected = true; return nil },
	}
	h := newHarness(t, ModeWindow, "1\n1\n", net) // Wi-Fi, first network
	h.run(t)
	got := net.ran("nmcli")
	if len(got) != 1 || got[0] != "nmcli --ask device wifi connect AA:BB:CC:DD:EE:FF" {
		t.Fatalf("ran %q", got)
	}
	if !strings.Contains(h.screen.String(), name) {
		t.Error("network name not shown")
	}
	log := h.logText(t)
	if strings.Contains(log, name) || strings.Contains(log, "AA:BB") {
		t.Fatalf("log holds the network name or access point:\n%s", log)
	}
	if !strings.Contains(log, "via=wifi") {
		t.Errorf("no online anchor:\n%s", log)
	}
}

func TestOpenNetworkWarnsAndBackReturns(t *testing.T) {
	net := &fakeNet{devices: always(wifiOff), connectivity: "full",
		wifiList: ` :Open:50:--:AA\:BB\:CC\:DD\:EE\:01` + "\n"}
	h := newHarness(t, ModeWindow, "1\n2\n1\n1\n", net) // Wi-Fi, Back; Wi-Fi, Open
	h.run(t)
	if got := net.ran("nmcli"); len(got) != 1 {
		t.Fatalf("ran %q, want one connect", got)
	}
	if !strings.Contains(h.screen.String(), "This network is open") {
		t.Error("no warning for an open network")
	}
}

func TestHiddenWifiRefusesOddNames(t *testing.T) {
	for _, bad := range []string{"esc\x1bname", strings.Repeat("x", 33)} {
		net := &fakeNet{devices: always(wifiOff)}
		h := newHarness(t, ModeWindow, "2\n"+bad+"\n", net)
		h.run(t)
		if got := net.ran("nmcli"); len(got) != 0 {
			t.Errorf("name %q went to nmcli: %q", bad, got)
		}
	}
	net := &fakeNet{devices: always(wifiOff)}
	h := newHarness(t, ModeWindow, "2\nMy Net\n", net)
	h.run(t)
	if got := net.ran("nmcli"); len(got) != 1 || got[0] != "nmcli --ask device wifi connect My Net hidden yes" {
		t.Errorf("ran %q", got)
	}
}

func withLayouts(t *testing.T, names ...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "base.lst")
	text := "! model\n  pc105 Generic\n\n! layout\n"
	for _, n := range names {
		text += "  " + n + "  Some layout\n"
	}
	text += "\n! variant\n  nodeadkeys de: German\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	old := region.XKBRules
	region.XKBRules = path
	t.Cleanup(func() { region.XKBRules = old })
}

func TestFirstKeyboardInWindowRestartsIt(t *testing.T) {
	withLayouts(t, "us", "de", "fr", "gb")
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "fr\n", net)
	h.layout = ""
	if err := h.run(t); !errors.Is(err, ErrRestartDisplay) {
		t.Fatalf("Run = %v, want ErrRestartDisplay", err)
	}
	if h.layout != "fr" {
		t.Fatalf("stored %q, want fr", h.layout)
	}
}

func TestFirstKeyboardEnterKeepsDefault(t *testing.T) {
	withLayouts(t, "us", "de")
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "\n", net)
	h.layout = ""
	if err := h.run(t); !errors.Is(err, io.EOF) {
		t.Fatalf("Run = %v, want the menu to go on", err)
	}
	if h.layout != region.DefaultKeyboard {
		t.Fatalf("stored %q, want %s", h.layout, region.DefaultKeyboard)
	}
}

func TestKeyboardOnConsoleLoadsAtOnce(t *testing.T) {
	withLayouts(t, "us", "de", "fr")
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeConsole, "5\nfr\n", net) // Keyboard layout, fr
	if err := h.run(t); !errors.Is(err, io.EOF) {
		t.Fatalf("Run = %v", err)
	}
	got := net.ran("sh")
	want := "sh -c " + `ckbcomp -layout "$1" | loadkeys -q` + " sh "
	// Once at start for the stored layout, once for the new one.
	if len(got) != 2 || got[0] != want+"de" || got[1] != want+"fr" {
		t.Fatalf("ran %q", got)
	}
}

func TestKeyboardUnknownNameKeepsCurrent(t *testing.T) {
	withLayouts(t, "us", "de")
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	// "zz" matches nothing; Pick asks again and "de" is taken.
	h := newHarness(t, ModeWindow, "5\nzz\nde\n", net)
	h.run(t)
	if h.layout != "de" {
		t.Fatalf("stored %q", h.layout)
	}
}

func TestMainArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"menu"}, {"menu", "--mode", "x11"}, {"layout", "extra"}, {"menu", "--mode", "window", "x"}} {
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
			t.Errorf("Main(%q) = %d, %q", args, code, errOut.String())
		}
	}
}

func TestSecureBootOn(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		data []byte
		want bool
	}{
		"on":    {[]byte{6, 0, 0, 0, 1}, true},
		"off":   {[]byte{6, 0, 0, 0, 0}, false},
		"short": {[]byte{6, 0, 0, 0}, false},
		"long":  {[]byte{6, 0, 0, 0, 1, 1}, false},
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, c.data, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := secureBootOn(path); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if secureBootOn(filepath.Join(dir, "missing")) {
		t.Error("missing variable counted as on")
	}
}

func TestFirstStart(t *testing.T) {
	dir := t.TempDir()
	if !firstStart(dir) {
		t.Error("empty folder is not a first start")
	}
	if firstStart(filepath.Join(dir, "missing")) {
		t.Error("missing folder counted as first start")
	}
	if err := os.WriteFile(filepath.Join(dir, "stick-x.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if firstStart(dir) {
		t.Error("a folder with a log is a first start")
	}
}

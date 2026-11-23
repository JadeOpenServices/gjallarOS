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

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/region"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickkeyboard"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/stickrelease"
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
	// disks is what lsblk prints; empty means no disks. boot is what
	// efibootmgr -v prints. bootNext is set by efibootmgr --bootnext unless
	// setFails.
	disks, boot, bootNext string
	setFails              bool
	// diskLooks counts lsblk calls; disksGone empties the disks from that
	// look on.
	diskLooks, disksGone int
	// pages lists the page title each terminal program got, "" for none.
	pages []string
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
	case "lsblk -J -l -p -o PATH,TYPE,PARTUUID,PARTLABEL,PARTTYPE,PKNAME":
		f.diskLooks++
		if f.disks == "" || (f.disksGone > 0 && f.diskLooks >= f.disksGone) {
			return []byte(`{"blockdevices":[]}`), nil
		}
		return []byte(f.disks), nil
	case "efibootmgr -v":
		return []byte(f.boot), nil
	case "efibootmgr ": // no arguments: the key ends in the space
		if f.bootNext == "" {
			return []byte("BootCurrent: 0001\n"), nil
		}
		return []byte("BootNext: " + f.bootNext + "\nBootCurrent: 0001\n"), nil
	}
	if e, ok := strings.CutPrefix(call, "efibootmgr --bootnext "); ok {
		if f.setFails {
			return nil, errors.New("efivars read-only")
		}
		f.bootNext = e
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected %s", call)
}

func (f *fakeNet) Interactive(ctx context.Context, name string, args ...string) error {
	f.calls = append(f.calls, "RUN "+name+" "+strings.Join(args, " "))
	title := ""
	for _, e := range prompt.PageEnviron(ctx, nil, calmmenu.Size{Cols: 80, Rows: 24}) {
		if t, ok := strings.CutPrefix(e, prompt.PageTitleEnv+"="); ok {
			title = t
		}
	}
	f.pages = append(f.pages, name+"="+title)
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
	// release is what the release check finds; Current by default.
	release stickrelease.Outcome
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
	h.s.CheckRelease = func(context.Context) stickrelease.Outcome { return h.release }
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
	h := newHarness(t, ModeWindow, "4\n", net) // Network, then end of input
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
	if strings.Contains(h.screen.String(), "Type a keyboard layout") {
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
		{"6\nstaging\n1\n", "gjallar-recover guided fresh --channel staging", "channel=staging"},
		{"6\n3\n1\n", "gjallar-recover guided fresh --channel dev", "channel=dev"},
		{"6\ndev\n6\nstable\n1\n", "gjallar-recover guided fresh", "channel=stable"},
	} {
		net := &fakeNet{devices: always(cable), connectivity: "full"}
		h := newHarness(t, ModeWindow, c.input, net)
		h.run(t)
		got := net.ran("gjallar-recover")
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("input %q: ran %q, want %q", c.input, got, c.want)
		}
		if strings.Join(net.pages, "|") != "gjallar-recover=Install gjallarOS" {
			t.Errorf("input %q: page titles %q, want only the installer's", c.input, net.pages)
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

// The release check (Q233) runs before the installer: below the floor,
// without the key, with bad files or no answer the installer does not start
// and the menu says why; a newer release is one line and the install goes
// on.
func TestInstallReleaseCheck(t *testing.T) {
	newest := stickrelease.Release{Version: "v2026.11.3", Date: "2026-11-20", Floor: "v2026.11.1"}
	for _, c := range []struct {
		name    string
		release stickrelease.Outcome
		nilFunc bool
		runs    bool
		anchor  string
		screen  string
		note    string
	}{
		{name: "current", release: stickrelease.Outcome{Kind: stickrelease.Current, Stick: "v2026.10.66"}, runs: true},
		{name: "older", release: stickrelease.Outcome{Kind: stickrelease.Older, Stick: "v2026.11.2", Newest: newest}, runs: true,
			anchor: "GJA-2041 stick.release.older", screen: "A newer release, v2026.11.3, is out. This stick installs v2026.11.2"},
		{name: "floor", release: stickrelease.Outcome{Kind: stickrelease.BelowFloor, Stick: "v2026.10.66", Newest: newest},
			anchor: "reason=floor", screen: "older than v2026.11.1, the oldest release that is still safe to install",
			note: "This stick is too old to install from. Make a new stick."},
		{name: "no key", release: stickrelease.Outcome{Kind: stickrelease.NoKey, Stick: "v2026.10.66"},
			anchor: "reason=no-key", screen: "built without the gjallarOS release key",
			note: "This stick cannot check its release. Use a released stick."},
		{name: "nil", nilFunc: true, anchor: "reason=no-key", screen: "built without the gjallarOS release key",
			note: "This stick cannot check its release. Use a released stick."},
		{name: "bad", release: stickrelease.Outcome{Kind: stickrelease.Bad, Stick: "v2026.10.66", Err: errors.New("\x1b[2Jfrom the network")},
			anchor: "reason=bad", screen: "did not match the gjallarOS signature",
			note: "The release check failed: the files did not match."},
		{name: "unreachable", release: stickrelease.Outcome{Kind: stickrelease.Unreachable, Stick: "v2026.10.66", Err: errors.New("\x1b[2Jfrom the network")},
			anchor: "reason=unreachable", screen: "Check the network and try again.",
			note: "Could not reach the gjallarOS releases. Check the network."},
	} {
		net := &fakeNet{devices: always(cable), connectivity: "full"}
		h := newHarness(t, ModeWindow, "1\n\n", net)
		h.release = c.release
		if c.nilFunc {
			h.s.CheckRelease = nil
		}
		h.run(t)
		if got := len(net.ran("gjallar-recover")); got != map[bool]int{true: 1}[c.runs] {
			t.Errorf("%s: installer ran %d times", c.name, got)
		}
		log, screen := h.logText(t), flat(h.screen.String())
		if c.anchor != "" && !strings.Contains(log, c.anchor) {
			t.Errorf("%s: log lacks %q:\n%s", c.name, c.anchor, log)
		}
		if !strings.Contains(screen, c.screen) {
			t.Errorf("%s: screen lacks %q", c.name, c.screen)
		}
		if strings.Contains(screen+log, "from the network") {
			t.Errorf("%s: network error text shown or logged", c.name)
		}
		if !c.runs && !strings.Contains(screen, "Nothing on this computer was changed.") {
			t.Errorf("%s: screen does not say nothing changed", c.name)
		}
		// The refusal is a page; the menu after it keeps a short note.
		if !c.runs && (len([]rune(c.note)) > 66 || !strings.Contains(lastScreen(h.screen.String()), c.note)) {
			t.Errorf("%s: menu after the refusal lacks the note %q", c.name, c.note)
		}
	}
}

// flat joins wrapped screen lines, so a check can find a sentence that
// the calm menu width broke across lines. Escape codes, such as the one
// that starts each line of a page in its column, count as spaces.
func flat(screen string) string {
	return strings.Join(strings.Fields(ansi.ReplaceAllString(screen, " ")), " ")
}

// lastScreen is what the screen shows after the last clear.
func lastScreen(screen string) string {
	return flat(screen[strings.LastIndex(screen, calmmenu.Clear)+len(calmmenu.Clear):])
}

func TestSayWrapsAtMenuWidth(t *testing.T) {
	var out strings.Builder
	s := &Stick{UI: prompt.UI{Out: &out}}
	text := "This network wants a sign-in page first, and the stick cannot show one. Use another network."
	s.say(text)
	got := out.String()
	for _, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if n := len([]rune(l)); n > 66 || n == 0 {
			t.Errorf("line of %d runes: %q", n, l)
		}
	}
	if flat(got) != text || !strings.HasSuffix(got, "\n") {
		t.Errorf("words changed: %q", got)
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
	if !strings.Contains(flat(h.screen.String()), "You are back in the stick menu.") {
		t.Error("screen does not say where the user is")
	}
}

func TestOtherEntries(t *testing.T) {
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "2\n\n3\n7\n8\n", net)
	h.run(t)
	want := []string{
		"gjallar-recovery-console --from-stick",
		"runuser -l nixos -c exec gjallar-stick-shell 5 2 72 22",
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
	// A shell or the recovery console never gets page mode.
	for _, p := range net.pages {
		if !strings.HasSuffix(p, "=") {
			t.Errorf("%s got page mode", p)
		}
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
	for _, args := range [][]string{nil, {"menu"}, {"menu", "--mode", "x11"}, {"layout", "extra"}, {"font"}, {"font", "--mode"}, {"font", "--mode", "x11"}, {"font", "--mode", "window", "x"}, {"text-console", "x"}, {"menu", "--mode", "window", "x"}, {"menu", "--mode", "console", "--logo", "/x"}, {"menu", "--mode", "window", "--logo"}, {"menu", "--mode", "window", "--icon", "/x"}, {"menu", "--mode", "window", "--logo", "/x"}, {"menu", "--mode", "console", "--release", "/r", "--logo", "/x"}, {"menu", "--mode", "window", "--release", "/r", "--release", "/r"}, {"menu", "--mode", "window", "--release", ""}, {"menu", "--mode", "window", "--release"}} {
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
			t.Errorf("Main(%q) = %d, %q", args, code, errOut.String())
		}
	}
}

func TestMenuArgs(t *testing.T) {
	for _, c := range []struct {
		args                []string
		mode, release, logo string
	}{
		{[]string{"menu", "--mode", "console", "--release", "/r"}, ModeConsole, "/r", ""},
		{[]string{"menu", "--mode", "window", "--release", "/r"}, ModeWindow, "/r", ""},
		{[]string{"menu", "--mode", "window", "--release", "/r", "--logo", "/l"}, ModeWindow, "/r", "/l"},
		{[]string{"menu", "--mode", "window", "--logo", "/l", "--release", "/r"}, ModeWindow, "/r", "/l"},
	} {
		mode, release, logo, ok := menuArgs(c.args)
		if !ok || mode != c.mode || release != c.release || logo != c.logo {
			t.Errorf("menuArgs(%q) = %q %q %q %v", c.args, mode, release, logo, ok)
		}
	}
}

// The menu runs as root: the release config must be a small regular file,
// not a link, FIFO or relative path.
func TestLoadRelease(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "release.json")
	if err := os.WriteFile(good, []byte(`{"version":"v2026.10.66","url":"https://example.org/r","key":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadRelease(good)
	if err != nil || cfg.Version != "v2026.10.66" || cfg.Key != nil {
		t.Fatalf("loadRelease = %+v, %v", cfg, err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo.json")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, bytes.Repeat([]byte(" "), stickrelease.MaxConfig+1), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":"v2026.10.66","url":"http://example.org/r","key":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, fifo, big, bad, "release.json", filepath.Join(dir, "missing.json")} {
		if _, err := loadRelease(path); err == nil {
			t.Errorf("loadRelease(%s) took it", path)
		}
	}
}

// textMode leaves anything that is not a virtual terminal alone.
func TestTextModeRefusesNonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := textMode(int(r.Fd())); err == nil {
		t.Fatal("textMode on a pipe returned no error")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := textMode(int(f.Fd())); err == nil {
		t.Fatal("textMode on a file returned no error")
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

// Loophole check: a scanned network name is drawn on the Wi-Fi page through
// showName. A name holding a screen-clearing escape or a text-direction mark
// must not reach the terminal raw.
func TestWifiPageShowsHostileNamesSafely(t *testing.T) {
	net := &fakeNet{devices: always(wifiOff), connectivity: "full",
		wifiList: " :Evil\x1b[2J‮Net:70:WPA2:AA\\:BB\\:CC\\:DD\\:EE\\:02\n"}
	h := newHarness(t, ModeWindow, "1\n", net) // Wi-Fi, then end of input
	h.run(t)
	screen := h.screen.String()
	if got, pages := strings.Count(screen, "\x1b[2J"), strings.Count(screen, calmmenu.Clear); got != pages {
		t.Errorf("%d screen clears for %d pages: a network name cleared the screen", got, pages)
	}
	if strings.Contains(screen, "‮") {
		t.Error("text-direction mark reached the screen")
	}
	page := lastScreen(screen)
	if !strings.HasPrefix(page, "Network ") || !strings.Contains(page, "1 Evil?[2J?Net (70%, WPA2) 2 Back") {
		t.Errorf("Wi-Fi page:\n%s", page)
	}
}

func TestOfflineInstallPageSaysWhy(t *testing.T) {
	net := &fakeNet{devices: always(nothing)}
	h := newHarness(t, ModeConsole, "2\n1\n", net) // go on without; Install
	h.run(t)
	page := lastScreen(h.screen.String())
	if !strings.HasPrefix(page, "Network ") || !strings.Contains(page, "─ Installing needs a network connection. No network yet.") {
		t.Errorf("network page:\n%s", page)
	}
}

func TestKeyboardNumberPicksFromList(t *testing.T) {
	withLayouts(t, "ba", "be", "bg", "by", "de")
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "5\nb\n3\n", net) // Keyboard layout, "b", third
	h.run(t)
	if !strings.Contains(flat(h.screen.String()), "Part of a name shows a list. 1 ba 2 be 3 bg 4 by Keyboard layout [de]:") {
		t.Errorf("no list page:\n%s", flat(h.screen.String()))
	}
	if h.layout != "bg" {
		t.Fatalf("stored %q, want bg", h.layout)
	}
}

func TestNoteShowsOnce(t *testing.T) {
	withLayouts(t, "us", "de")
	net := &fakeNet{devices: always(cable), connectivity: "full"}
	h := newHarness(t, ModeWindow, "5\nzz\nde\n", net)
	h.run(t)
	if n := strings.Count(flat(h.screen.String()), `Nothing matches "zz"`); n != 1 {
		t.Errorf("note shown %d times, want once", n)
	}
}

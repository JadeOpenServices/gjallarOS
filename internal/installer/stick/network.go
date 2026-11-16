package stick

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
)

// netState is what NetworkManager reports.
type netState struct {
	// via is "ethernet" or "wifi" when a cable or Wi-Fi is connected.
	via string
	// connectivity is NetworkManager's check: full, limited, portal,
	// none or unknown (no check configured).
	connectivity string
	// wifi is true when a Wi-Fi device can scan.
	wifi bool
}

func (n netState) online() bool {
	return n.via != "" && (n.connectivity == "full" || n.connectivity == "unknown")
}

// splitTerse splits one line of nmcli -t output. nmcli escapes ':' and
// '\' inside a value with a backslash.
func splitTerse(line string) []string {
	var fields []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			i++
			b.WriteByte(line[i])
		case c == ':':
			fields = append(fields, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(fields, b.String())
}

// parseDevices reads `nmcli -t -f DEVICE,TYPE,STATE device`.
func parseDevices(out string) (via string, wifi bool) {
	ethernet, wireless := false, false
	for _, line := range strings.Split(out, "\n") {
		f := splitTerse(line)
		if len(f) != 3 {
			continue
		}
		kind, state := f[1], f[2]
		connected := state == "connected" || state == "connected (externally)"
		switch kind {
		case "ethernet":
			ethernet = ethernet || connected
		case "wifi":
			wireless = wireless || connected
			if state != "unavailable" && state != "unmanaged" {
				wifi = true
			}
		}
	}
	switch {
	case ethernet:
		via = "ethernet"
	case wireless:
		via = "wifi"
	}
	return via, wifi
}

func (s *Stick) readNet(ctx context.Context) netState {
	var n netState
	if out, err := s.Cmd.Output(ctx, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE", "device"); err == nil {
		n.via, n.wifi = parseDevices(string(out))
	}
	n.connectivity = "none"
	if n.via != "" {
		if out, err := s.Cmd.Output(ctx, "nmcli", "-t", "networking", "connectivity", "check"); err == nil {
			n.connectivity = strings.TrimSpace(string(out))
		}
	}
	return n
}

// wifiNetwork is one network from a scan. bssid picks the access point to
// connect to and is never shown: it identifies a device like a MAC does.
type wifiNetwork struct {
	ssid, security, bssid string
	signal                int
}

var bssidPattern = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)

// maxNetworks caps the list shown.
const maxNetworks = 20

// parseWifi reads `nmcli -t -f IN-USE,SSID,SIGNAL,SECURITY,BSSID device
// wifi list`: hidden networks out, one entry per name and security (the
// strongest access point), strongest first, at most maxNetworks.
func parseWifi(out string) []wifiNetwork {
	best := map[string]wifiNetwork{}
	for _, line := range strings.Split(out, "\n") {
		f := splitTerse(line)
		if len(f) != 5 {
			continue
		}
		ssid, security, bssid := f[1], f[3], strings.ToUpper(f[4])
		signal, err := strconv.Atoi(f[2])
		if ssid == "" || ssid == "--" || err != nil || !bssidPattern.MatchString(bssid) {
			continue
		}
		if security == "--" {
			security = ""
		}
		key := ssid + "\x00" + security
		if old, ok := best[key]; !ok || signal > old.signal {
			best[key] = wifiNetwork{ssid: ssid, security: security, bssid: bssid, signal: signal}
		}
	}
	list := make([]wifiNetwork, 0, len(best))
	for _, n := range best {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].signal != list[j].signal {
			return list[i].signal > list[j].signal
		}
		if list[i].ssid != list[j].ssid {
			return list[i].ssid < list[j].ssid
		}
		return list[i].security < list[j].security
	})
	if len(list) > maxNetworks {
		list = list[:maxNetworks]
	}
	return list
}

// showName makes a network name safe to print: anything that is not a
// printable character (control codes, text-direction marks, broken UTF-8)
// becomes '?', and at most 32 characters are kept. A neighbour can name a
// network anything.
func showName(ssid string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(ssid) && n < 32; n++ {
		r, size := utf8.DecodeRuneInString(ssid[i:])
		i += size
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (w wifiNetwork) label() string {
	security := w.security
	if security == "" {
		security = "open"
	}
	return fmt.Sprintf("%s  (%d%%, %s)", showName(w.ssid), w.signal, security)
}

// Network-step choices.
const (
	netWifi    = "Wi-Fi"
	netHidden  = "Hidden Wi-Fi network (type its name)"
	netAgain   = "Look again (after plugging in a cable or adapter)"
	netWithout = "Go on without network (recovery and shell only)"
	netBack    = "Back"
)

// lookAgainPolls and pollPause are how long "Look again" waits for a new
// cable or adapter: about five seconds.
const lookAgainPolls = 5

// networkStep shows the connection and helps make one. It returns once
// the stick is online or the user goes on without.
func (s *Stick) networkStep(ctx context.Context) error {
	for {
		n := s.readNet(ctx)
		if n.online() {
			s.markOnline(n.via)
			fmt.Fprintf(s.UI.Out, "Connected to the network (%s).\n", viaName(n.via))
			return nil
		}
		switch {
		case n.via != "" && n.connectivity == "portal":
			fmt.Fprintln(s.UI.Out, "This network wants a sign-in page first, and the stick cannot show one. Use another network.")
		case n.via != "":
			fmt.Fprintln(s.UI.Out, "Connected, but the internet cannot be reached.")
		default:
			fmt.Fprintln(s.UI.Out, "No network yet. Plug in a cable, or pick a Wi-Fi network.")
		}
		options := []string{}
		if n.wifi {
			options = append(options, netWifi, netHidden)
		}
		options = append(options, netAgain, netWithout)
		choice, err := s.choose(ctx, "Network", options)
		if err != nil {
			return err
		}
		switch choice {
		case netWifi:
			if err := s.pickWifi(ctx); err != nil {
				return err
			}
		case netHidden:
			if err := s.hiddenWifi(ctx); err != nil {
				return err
			}
		case netAgain:
			s.lookAgain++
			installlog.Mark(installlog.StickNetworkLookAgain, installlog.Count("tries", s.lookAgain))
			fmt.Fprintln(s.UI.Out, "Looking for a network...")
			for i := 0; i < lookAgainPolls && !s.readNet(ctx).online(); i++ {
				s.Sleep()
			}
		case netWithout:
			installlog.Mark(installlog.StickNetworkNone)
			s.offline = true
			return nil
		}
	}
}

func viaName(via string) string {
	if via == "wifi" {
		return "Wi-Fi"
	}
	return "cable"
}

func (s *Stick) markOnline(via string) {
	s.offline = false
	if s.markedVia == via {
		return
	}
	s.markedVia = via
	switch via {
	case "ethernet":
		installlog.Mark(installlog.StickNetworkOnline, installlog.Choice("via", "ethernet"))
	case "wifi":
		installlog.Mark(installlog.StickNetworkOnline, installlog.Choice("via", "wifi"))
	}
}

func (s *Stick) pickWifi(ctx context.Context) error {
	fmt.Fprintln(s.UI.Out, "Looking for Wi-Fi networks...")
	out, err := s.Cmd.Output(ctx, "nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY,BSSID", "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		fmt.Fprintln(s.UI.Out, "The Wi-Fi scan failed.")
		return nil
	}
	networks := parseWifi(string(out))
	if len(networks) == 0 {
		fmt.Fprintln(s.UI.Out, "No Wi-Fi networks found.")
		return nil
	}
	options := make([]string, 0, len(networks)+1)
	for _, n := range networks {
		options = append(options, n.label())
	}
	options = append(options, netBack)
	choice, err := s.chooseIndex(ctx, "Wi-Fi network", options)
	if err != nil || choice == len(networks) {
		return err
	}
	picked := networks[choice]
	if picked.security == "" {
		fmt.Fprintln(s.UI.Out, "This network is open, so others nearby can read what goes over it. The installer checks everything it downloads.")
	}
	// nmcli asks for the password on the terminal itself; it never passes
	// through this program or its log.
	if err := s.Cmd.Interactive(ctx, "nmcli", "--ask", "device", "wifi", "connect", picked.bssid); err != nil {
		fmt.Fprintln(s.UI.Out, "Could not connect. Check the password and try again.")
	}
	return nil
}

func (s *Stick) hiddenWifi(ctx context.Context) error {
	s.flush()
	name, err := s.UI.Value(ctx, "Network name (empty to go back)", "")
	if err != nil || name == "" {
		return err
	}
	if len(name) > 32 || strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) {
		fmt.Fprintln(s.UI.Out, "A network name has at most 32 bytes of printable text.")
		return nil
	}
	if err := s.Cmd.Interactive(ctx, "nmcli", "--ask", "device", "wifi", "connect", name, "hidden", "yes"); err != nil {
		fmt.Fprintln(s.UI.Out, "Could not connect. Check the name and password and try again.")
	}
	return nil
}

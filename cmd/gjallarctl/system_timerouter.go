package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

// The router fallback of gjallar-time-check (ROADMAP section 18, step 3):
// on a proven home or site network where no NTS source works, chronyd takes
// small corrections from the home router. It never counts as confirmed.

const (
	timeRouterPath = timeRunDir + "/router.json"
	ntpProbeWait   = 3 * time.Second
	// ntpEpochOffset is the seconds from 1900 (NTP) to 1970 (Unix).
	ntpEpochOffset = 2208988800
)

// routerConf is the fallback. The confdir comes last in chrony.conf, so
// these lines win over the strict ones. With authselectmode prefer chronyd
// would drop the router while any NTS source is configured.
const routerConf = `# gjallar-time-check: home router fallback, removed when NTS works again or the network changes
authselectmode ignore
minsources 1
makestep 1 0
maxchange 60 0 -1
maxslewrate 1000
maxdrift 500
server %s iburst minpoll 6 maxpoll 10
`

// timeRouter is the fallback in use. The file lives in /run, so it is gone
// after a reboot and status can read it as any user.
type timeRouter struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	// Network is the network key the router was proven on.
	Network string    `json:"network"`
	Since   time.Time `json:"since"`
}

var readTimeRouter = func() *timeRouter {
	data, err := os.ReadFile(timeRouterPath)
	if err != nil {
		return nil
	}
	var r timeRouter
	if json.Unmarshal(data, &r) != nil || r.Addr == "" {
		return nil
	}
	return &r
}

func (c *timeCheck) routerConf() string { return filepath.Join(c.runDir, "chrony", "router.conf") }
func (c *timeCheck) routerPath() string { return filepath.Join(c.runDir, "router.json") }

// routerCandidates picks the addresses to ask from a verdict that is for
// the network the device is on now: tailnet addresses first, IPv4 before
// IPv6, then the local address the proof came from.
func routerCandidates(v nettrust.Verdict, network string) (string, []string, string) {
	switch {
	case v.Trust != nettrust.TrustHome && v.Trust != nettrust.TrustSite:
		return "", nil, fmt.Sprintf("the network is %q, not a proven home or site network", v.Trust)
	case v.RouterVia == "":
		return "", nil, "no tailnet router is proven on this network"
	case network == "" || v.Network != network:
		return "", nil, "the network verdict is not for the current network"
	}
	var v4, v6 []string
	for _, s := range v.RouterTailnet {
		addr, err := netip.ParseAddr(s)
		switch {
		case err != nil:
		case addr.Is4():
			v4 = append(v4, addr.String())
		default:
			v6 = append(v6, addr.String())
		}
	}
	addrs := append(v4, v6...)
	if via, err := netip.ParseAddr(v.RouterVia); err == nil {
		addrs = append(addrs, via.String())
	}
	if len(addrs) == 0 {
		return "", nil, "the router has no usable address"
	}
	name, _, _ := strings.Cut(v.Router, " via ")
	return name, addrs, ""
}

func (c *timeCheck) readRouter() *timeRouter {
	data, err := os.ReadFile(c.routerPath())
	if err != nil {
		return nil
	}
	var r timeRouter
	if json.Unmarshal(data, &r) != nil || r.Addr == "" {
		return &timeRouter{}
	}
	return &r
}

// dropRouter removes the fallback; it reports whether it was there.
func (c *timeCheck) dropRouter() bool {
	had := false
	for _, path := range []string{c.routerConf(), c.routerPath()} {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.logf("ERROR", "%v", err)
		}
		had = had || err == nil
	}
	return had
}

func (c *timeCheck) candidates() (string, []string, string) {
	data, err := os.ReadFile(c.verdictPath)
	if err != nil {
		return "", nil, "no network verdict"
	}
	var v nettrust.Verdict
	if err := json.Unmarshal(data, &v); err != nil {
		return "", nil, "unreadable network verdict"
	}
	return routerCandidates(v, c.networkKey())
}

// routerStays says why a running fallback has to end; empty when it may
// stay. chronyd counts a source reachable only after an authenticated
// answer, so two reachable NTS sources mean NTS works again.
func (c *timeCheck) routerStays(active *timeRouter) string {
	_, addrs, reason := c.candidates()
	switch {
	case reason != "":
		return reason
	case !slices.Contains(addrs, active.Addr):
		return "the proven router changed"
	case c.ntsReachable(active.Addr) >= 2:
		return "NTS sources answer again"
	}
	return ""
}

// startRouter turns the fallback on when the verdict allows it and the
// router answers NTP.
func (c *timeCheck) startRouter() {
	name, addrs, reason := c.candidates()
	if reason != "" {
		c.logf("INFO", "no router fallback: %s", reason)
		return
	}
	addr := ""
	for _, candidate := range addrs {
		if err := c.ntpProbe(candidate); err != nil {
			c.logf("INFO", "router %s at %s: %v", name, candidate, err)
			continue
		}
		addr = candidate
		break
	}
	if addr == "" {
		c.logf("WARNING", "no router fallback: the router %s does not answer NTP", name)
		return
	}
	state, _ := json.Marshal(timeRouter{Name: name, Addr: addr, Network: c.networkKey(), Since: c.now()})
	if err := writePublicFile(c.routerPath(), append(state, '\n')); err != nil {
		c.logf("ERROR", "%v", err)
		return
	}
	if err := writePublicFile(c.routerConf(), fmt.Appendf(nil, routerConf, addr)); err != nil {
		c.logf("ERROR", "%v", err)
		c.dropRouter()
		return
	}
	if err := c.chronyd("restart"); err != nil {
		c.logf("ERROR", "restart chronyd: %v", err)
		c.dropRouter()
		return
	}
	c.logf("WARNING", "no NTS source works; taking small corrections from the home router %s at %s", name, addr)
}

// probeNTP sends one plain client request and checks the answer: a server
// reply, a usable stratum and our transmit time echoed back.
func probeNTP(addr string) error {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(addr, "123"), ntpProbeWait)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(ntpProbeWait)); err != nil {
		return err
	}
	req := make([]byte, 48)
	req[0] = 4<<3 | 3 // version 4, client
	// A random transmit time, as RFC 9109 asks for, so only an answer to
	// this request matches.
	if _, err := rand.Read(req[40:48]); err != nil {
		return err
	}
	if _, err := conn.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 128)
	n, err := conn.Read(resp)
	if err != nil {
		return err
	}
	return checkNTPReply(req, resp[:n])
}

func checkNTPReply(req, resp []byte) error {
	switch {
	case len(resp) < 48:
		return errors.New("short answer")
	case resp[0]&7 != 4:
		return errors.New("not a server answer")
	case resp[0]>>6 == 3:
		return errors.New("its clock is not synchronised")
	case resp[1] < 1 || resp[1] > 15:
		return fmt.Errorf("stratum %d", resp[1])
	case string(resp[24:32]) != string(req[40:48]):
		return errors.New("answer is not for this request")
	case binary.BigEndian.Uint32(resp[40:44]) < ntpEpochOffset:
		return errors.New("no time in the answer")
	}
	return nil
}

// chronySourcesReachable counts reachable sources other than exclude, from
// chronyc -c -n sources: mode, state, address, stratum, poll, reach (octal).
func chronySourcesReachable(exclude string) int {
	out, err := exec.Command("chronyc", "-c", "-n", "sources").Output()
	if err != nil {
		return 0
	}
	return countReachable(string(out), exclude)
}

func countReachable(out, exclude string) int {
	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) < 6 || fields[0] != "^" || fields[2] == exclude {
			continue
		}
		if reach, err := strconv.ParseUint(fields[5], 8, 16); err == nil && reach != 0 {
			count++
		}
	}
	return count
}

func currentNetworkKey() string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	network, err := nettrust.NewSystem().Network(ctx)
	if err != nil {
		return ""
	}
	return nettrust.NetworkKey(network)
}

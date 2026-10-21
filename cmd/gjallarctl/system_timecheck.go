package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// time-check runs as the gjallar-time-check service (system/services/time.nix).
// It confirms the clock over NTS. When the clock is so far behind that no
// NTS certificate is valid yet, it sets the clock once per boot with the
// certificate date check off, then confirms it again with the check on
// (ROADMAP section 18, step 2).

const (
	timeRunDir       = "/run/gjallar-time"
	timeStatePath    = timeRunDir + "/state.json"
	timeStampPath    = "/var/lib/gjallar-time/confirmed"
	chronyStateDir   = "/var/lib/chrony"
	systemCABundle   = "/etc/ssl/certs/ca-certificates.crt"
	timePollInterval = 2 * time.Second
	timeSyncWait     = 2 * time.Minute
	// A step back smaller than this is ordinary correction, not a refusal.
	timeBackSlack = 2 * time.Second
)

// timeBoot is what the check did this boot. The file lives in /run, so it
// starts empty on every boot and status can read it as any user.
type timeBoot struct {
	// bootstrap: chronyd runs with the date check off right now.
	// confirmed: set with the check off, then confirmed with it on.
	// unconfirmed: set with the check off, not yet confirmed with it on.
	// failed: the clock looked behind, but no source answered with the
	// check off either.
	// refused: the update would have moved the clock back; it was put back.
	State string `json:"state"`
	// Step is how far the clock moved, in seconds.
	Step int64 `json:"step,omitempty"`
}

var readTimeBoot = func() timeBoot {
	var boot timeBoot
	if data, err := os.ReadFile(timeStatePath); err == nil {
		_ = json.Unmarshal(data, &boot)
	}
	return boot
}

type certVerdict int

const (
	certUnreachable certVerdict = iota
	// certValid: the chain verifies at the current clock.
	certValid
	// certBehind: the chain verifies, but only from a date after the
	// current clock, and the certificate is still valid after the last
	// confirmed time.
	certBehind
	// certAhead: the chain verifies, but its validity ended before the
	// current clock.
	certAhead
	// certUntrusted: the chain does not verify for the host at any date, or
	// it ended before the last confirmed time.
	certUntrusted
)

func (v certVerdict) String() string {
	return [...]string{"unreachable", "valid", "clock behind", "clock ahead", "untrusted"}[v]
}

// classifyChain says where the current clock sits against the chain a
// server sent. roots holds only the pinned roots of the four operators.
func classifyChain(chain []*x509.Certificate, host string, roots *x509.CertPool, now, floor time.Time) certVerdict {
	if len(chain) == 0 {
		return certUntrusted
	}
	leaf := chain[0]
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	opts := x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediates, CurrentTime: now}
	if _, err := leaf.Verify(opts); err == nil {
		return certValid
	}
	opts.CurrentTime = leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) / 2)
	if _, err := leaf.Verify(opts); err != nil {
		return certUntrusted
	}
	switch {
	case now.Before(leaf.NotBefore) && leaf.NotAfter.After(floor):
		return certBehind
	case now.Before(leaf.NotBefore):
		return certUntrusted
	default:
		return certAhead
	}
}

// bootstrapAllowed decides on the exception from what the servers sent.
// It needs the same two operators chronyd needs (minsources 2), at least
// one of them showing the clock behind, and none showing it ahead: a clock
// ahead never gets the exception, so an old leaked certificate cannot pull
// it back.
func bootstrapAllowed(verdicts map[string]certVerdict) (bool, string) {
	count := map[certVerdict]int{}
	for _, v := range verdicts {
		count[v]++
	}
	switch {
	case count[certAhead] > 0:
		return false, "a server certificate ended before the current clock; the clock may be ahead"
	case count[certBehind] == 0:
		return false, "no server certificate shows the clock behind"
	case count[certValid]+count[certBehind] < 2:
		return false, "fewer than two operators answer with a trusted certificate"
	}
	return true, ""
}

// forwardOK says whether the clock chronyd set may stay: never earlier than
// the clock before the exception plus the time that passed since, and never
// earlier than the last confirmed time.
func forwardOK(now, expected, floor time.Time) bool {
	return !now.Before(expected.Add(-timeBackSlack)) && !now.Before(floor)
}

// pinnedRoots picks the operators' roots out of the system bundle by exact
// common name. It returns the names it did not find.
func pinnedRoots(bundle []byte, names []string) ([]*x509.Certificate, []string) {
	var found []*x509.Certificate
	seen := map[string]bool{}
	for {
		var block *pem.Block
		block, bundle = pem.Decode(bundle)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !slices.Contains(names, cert.Subject.CommonName) {
			continue
		}
		found = append(found, cert)
		seen[cert.Subject.CommonName] = true
	}
	var missing []string
	for _, name := range names {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	return found, missing
}

// probeChain fetches the chain an NTS-KE server sends. Verification is off
// in the handshake only because the clock is the thing in question;
// classifyChain verifies the chain itself, and nothing is sent over the
// connection.
func probeChain(host string) ([]*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{},
		Config: &tls.Config{
			ServerName:         host,
			NextProtos:         []string{"ntske/1"},
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "4460"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.(*tls.Conn).ConnectionState().PeerCertificates, nil
}

type timeCheck struct {
	servers, roots []string
	wait           time.Duration
	runDir         string
	stampPath      string
	chronyDir      string
	bundle         string
	stderr         io.Writer

	clock func() clockState
	// now returns the wall clock without a monotonic reading, so a step of
	// the system clock shows. elapsed is monotonic.
	now      func() time.Time
	elapsed  func() time.Duration
	sleep    func(time.Duration)
	probe    func(host string) ([]*x509.Certificate, error)
	chronyd  func(action string) error
	setClock func(time.Time) error
}

func (c *timeCheck) bootstrapConf() string {
	return filepath.Join(c.runDir, "chrony", "bootstrap.conf")
}
func (c *timeCheck) rootsPath() string { return filepath.Join(c.runDir, "roots.pem") }
func (c *timeCheck) statePath() string { return filepath.Join(c.runDir, "state.json") }

func (c *timeCheck) logf(level, format string, args ...any) {
	fmt.Fprintf(c.stderr, level+": "+format+"\n", args...)
}

// waitSynced polls chronyd until an update after since confirms the clock.
func (c *timeCheck) waitSynced(limit time.Duration, since time.Time) (clockState, bool) {
	end := c.elapsed() + limit
	for {
		state := c.clock()
		if state.Synced && !state.Ref.IsZero() && !state.Ref.Before(since) && c.now().Sub(state.Ref) <= clockFresh {
			return state, true
		}
		if c.elapsed() >= end {
			return state, false
		}
		c.sleep(timePollInterval)
	}
}

func (c *timeCheck) readState() (timeBoot, bool) {
	data, err := os.ReadFile(c.statePath())
	if err != nil {
		return timeBoot{}, false
	}
	var boot timeBoot
	if err := json.Unmarshal(data, &boot); err != nil {
		return timeBoot{}, true
	}
	return boot, true
}

func (c *timeCheck) writeState(boot timeBoot) {
	data, _ := json.Marshal(boot)
	if err := writePublicFile(c.statePath(), append(data, '\n')); err != nil {
		c.logf("WARNING", "%v", err)
	}
}

func (c *timeCheck) readFloor() time.Time {
	data, err := os.ReadFile(c.stampPath)
	if err != nil {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func (c *timeCheck) writeStamp(ref time.Time) {
	if err := writePublicFile(c.stampPath, []byte(strconv.FormatInt(ref.Unix(), 10)+"\n")); err != nil {
		c.logf("WARNING", "%v", err)
	}
}

// restartStrict restarts chronyd without the keys it holds, so every
// source does a new key exchange with the date check on.
func (c *timeCheck) restartStrict() error {
	if err := c.chronyd("stop"); err != nil {
		return err
	}
	dumps, _ := filepath.Glob(filepath.Join(c.chronyDir, "*.nts"))
	for _, dump := range dumps {
		if err := os.Remove(dump); err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.logf("WARNING", "%v", err)
		}
	}
	return c.chronyd("start")
}

// dropBootstrap removes the exception; reports whether it was there.
func (c *timeCheck) dropBootstrap() bool {
	err := os.Remove(c.bootstrapConf())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.logf("ERROR", "%v", err)
	}
	return err == nil
}

func (c *timeCheck) run() int {
	start := c.now()
	floor := c.readFloor()
	boot, ranThisBoot := c.readState()

	// A run that stopped during the exception leaves it on: take it off
	// before anything else.
	if c.dropBootstrap() {
		c.logf("WARNING", "an earlier run left the certificate date check off; turning it back on")
		if err := c.restartStrict(); err != nil {
			c.logf("ERROR", "restart chronyd: %v", err)
			return 1
		}
		if boot.State == "bootstrap" {
			boot.State = "unconfirmed"
			c.writeState(boot)
		}
	}

	if state, ok := c.waitSynced(c.wait, time.Time{}); ok {
		c.writeStamp(state.Ref)
		if boot.State == "unconfirmed" {
			// Every key since the strict restart came with the date check on.
			boot.State = "confirmed"
			c.writeState(boot)
		}
		return 0
	}
	if ranThisBoot {
		c.logf("INFO", "time not confirmed; the exception already ran this boot (%s)", boot.State)
		return 0
	}

	bundle, err := os.ReadFile(c.bundle)
	if err != nil {
		c.logf("ERROR", "%v", err)
		return 1
	}
	roots, missing := pinnedRoots(bundle, c.roots)
	for _, name := range missing {
		c.logf("INFO", "root %q is not in %s", name, c.bundle)
	}
	if len(roots) == 0 {
		c.logf("ERROR", "none of the pinned roots is in %s", c.bundle)
		return 1
	}
	pool := x509.NewCertPool()
	for _, root := range roots {
		pool.AddCert(root)
	}

	now := c.now()
	verdicts := map[string]certVerdict{}
	type result struct {
		host  string
		chain []*x509.Certificate
		err   error
	}
	results := make(chan result, len(c.servers))
	for _, host := range c.servers {
		go func() {
			chain, err := c.probe(host)
			results <- result{host, chain, err}
		}()
	}
	for range c.servers {
		r := <-results
		if r.err != nil {
			verdicts[r.host] = certUnreachable
		} else {
			verdicts[r.host] = classifyChain(r.chain, r.host, pool, now, floor)
		}
		c.logf("INFO", "%s: %s", r.host, verdicts[r.host])
	}
	if ok, reason := bootstrapAllowed(verdicts); !ok {
		c.logf("WARNING", "time not confirmed, no exception: %s", reason)
		return 0
	}

	var pemRoots []byte
	for _, root := range roots {
		pemRoots = append(pemRoots, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw})...)
	}
	if err := writePublicFile(c.rootsPath(), pemRoots); err != nil {
		c.logf("ERROR", "%v", err)
		return 1
	}
	c.writeState(timeBoot{State: "bootstrap"})
	conf := "# gjallar-time-check: one-time exception, removed after the first update\n" +
		"nocerttimecheck 1\nnosystemcert\nntstrustedcerts " + c.rootsPath() + "\n"
	if err := writePublicFile(c.bootstrapConf(), []byte(conf)); err != nil {
		c.logf("ERROR", "%v", err)
		return 1
	}
	c.logf("WARNING", "the clock is behind every NTS certificate; setting it once with the certificate date check off")
	restartErr := c.chronyd("restart")
	var synced bool
	if restartErr == nil {
		_, synced = c.waitSynced(timeSyncWait, time.Time{})
	}
	c.dropBootstrap()
	if !synced {
		if restartErr != nil {
			c.logf("ERROR", "restart chronyd: %v", restartErr)
		}
		c.logf("WARNING", "no update came with the date check off either; clock unchanged")
		if err := c.restartStrict(); err != nil {
			c.logf("ERROR", "restart chronyd: %v", err)
		}
		c.writeState(timeBoot{State: "failed"})
		return 1
	}

	expected := start.Add(c.elapsed())
	after := c.now()
	step := after.Sub(expected)
	if !forwardOK(after, expected, floor) {
		c.logf("ERROR", "the update moved the clock back by %s; refused, putting it back", -step.Round(time.Second))
		if err := c.chronyd("stop"); err != nil {
			c.logf("ERROR", "stop chronyd: %v", err)
		}
		back := start.Add(c.elapsed())
		if err := c.setClock(back); err != nil {
			c.logf("ERROR", "set the clock back: %v", err)
		}
		// chronyd -s ignores an RTC time older than the drift file, so a
		// clock the update dragged back into the RTC stays out.
		_ = os.Chtimes(filepath.Join(c.chronyDir, "chrony.drift"), back, back)
		if err := c.restartStrict(); err != nil {
			c.logf("ERROR", "restart chronyd: %v", err)
		}
		c.writeState(timeBoot{State: "refused", Step: int64(step.Seconds())})
		return 1
	}

	c.logf("INFO", "clock moved forward by %s; confirming with the date check on", step.Round(time.Second))
	restarted := c.now()
	if err := c.restartStrict(); err != nil {
		c.logf("ERROR", "restart chronyd: %v", err)
		c.writeState(timeBoot{State: "unconfirmed", Step: int64(step.Seconds())})
		return 1
	}
	state, ok := c.waitSynced(timeSyncWait, restarted)
	if !ok {
		c.logf("WARNING", "time set, but not yet confirmed with the date check on")
		c.writeState(timeBoot{State: "unconfirmed", Step: int64(step.Seconds())})
		return 1
	}
	c.writeStamp(state.Ref)
	c.writeState(timeBoot{State: "confirmed", Step: int64(step.Seconds())})
	c.logf("INFO", "time confirmed with the date check on")
	return 0
}

// writePublicFile writes a file every user may read: chronyd reads the
// exception as its own user, status reads the state as the logged-in user.
func writePublicFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func systemTimeCheck(args []string, stderr io.Writer) int {
	begin := time.Now()
	c := &timeCheck{
		wait:      90 * time.Second,
		runDir:    timeRunDir,
		stampPath: timeStampPath,
		chronyDir: chronyStateDir,
		bundle:    systemCABundle,
		stderr:    stderr,
		clock:     readClock,
		now:       func() time.Time { return time.Now().Round(0) },
		elapsed:   func() time.Duration { return time.Since(begin) },
		sleep:     time.Sleep,
		probe:     probeChain,
		chronyd: func(action string) error {
			return exec.Command("systemctl", action, "chronyd.service").Run()
		},
		setClock: func(t time.Time) error {
			tv := syscall.NsecToTimeval(t.UnixNano())
			return syscall.Settimeofday(&tv)
		},
	}
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "ERROR: %s needs a value\n", args[i])
			return 2
		}
		value := args[i+1]
		switch args[i] {
		case "--wait":
			d, err := time.ParseDuration(value)
			if err != nil {
				fmt.Fprintf(stderr, "ERROR: --wait: %v\n", err)
				return 2
			}
			c.wait = d
		case "--server":
			c.servers = append(c.servers, value)
		case "--root":
			c.roots = append(c.roots, value)
		default:
			fmt.Fprintf(stderr, "ERROR: unknown option %s\n", args[i])
			return 2
		}
		i++
	}
	if len(c.servers) == 0 || len(c.roots) == 0 {
		fmt.Fprintln(stderr, "ERROR: time-check needs --server and --root")
		return 2
	}
	return c.run()
}

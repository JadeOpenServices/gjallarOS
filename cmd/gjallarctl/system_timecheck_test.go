package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// All certificates here are SYNTHETIC, made for the test.

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             day("2020-01-01"),
		NotAfter:              day("2040-01-01"),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert, key}
}

func (ca testCA) leaf(t *testing.T, host string, from, until time.Time) []*x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    from,
		NotAfter:     until,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return []*x509.Certificate{cert}
}

func (ca testCA) pem() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
}

func TestClassifyChain(t *testing.T) {
	ca, other := newTestCA(t, "SYNTHETIC Root A"), newTestCA(t, "SYNTHETIC Root B")
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	chain := ca.leaf(t, "nts.example.org", day("2026-09-01"), day("2026-12-01"))
	floor := day("2026-08-01")
	for _, c := range []struct {
		name  string
		chain []*x509.Certificate
		host  string
		now   time.Time
		floor time.Time
		want  certVerdict
	}{
		{"valid", chain, "nts.example.org", day("2026-10-21"), floor, certValid},
		{"behind", chain, "nts.example.org", day("2026-06-01"), floor, certBehind},
		{"ahead", chain, "nts.example.org", day("2027-01-01"), floor, certAhead},
		{"wrong host", chain, "nts.example.net", day("2026-06-01"), floor, certUntrusted},
		{"unknown root", other.leaf(t, "nts.example.org", day("2026-09-01"), day("2026-12-01")), "nts.example.org", day("2026-06-01"), floor, certUntrusted},
		{"ended before the last confirmed time", chain, "nts.example.org", day("2026-06-01"), day("2027-01-01"), certUntrusted},
		{"no chain", nil, "nts.example.org", day("2026-06-01"), floor, certUntrusted},
	} {
		if got := classifyChain(c.chain, c.host, roots, c.now, c.floor); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestBootstrapAllowed(t *testing.T) {
	for _, c := range []struct {
		verdicts []certVerdict
		want     bool
	}{
		{[]certVerdict{certBehind, certBehind, certBehind, certValid}, true},
		{[]certVerdict{certBehind, certValid, certUnreachable, certUnreachable}, true},
		{[]certVerdict{certBehind, certUnreachable, certUntrusted, certUnreachable}, false},
		{[]certVerdict{certValid, certValid, certUnreachable, certUnreachable}, false},
		{[]certVerdict{certBehind, certBehind, certAhead, certValid}, false},
		{[]certVerdict{certUnreachable, certUnreachable, certUnreachable, certUnreachable}, false},
	} {
		verdicts := map[string]certVerdict{}
		for i, v := range c.verdicts {
			verdicts[string(rune('a'+i))] = v
		}
		if got, reason := bootstrapAllowed(verdicts); got != c.want {
			t.Errorf("%v: got %t (%s), want %t", c.verdicts, got, reason, c.want)
		}
	}
}

func TestPinnedRoots(t *testing.T) {
	a, b := newTestCA(t, "SYNTHETIC Root A"), newTestCA(t, "SYNTHETIC Root AB")
	bundle := slices.Concat(b.pem(), []byte("# comment\n"), pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: []byte{1}}), a.pem())
	found, missing := pinnedRoots(bundle, []string{"SYNTHETIC Root A", "SYNTHETIC Root C"})
	if len(found) != 1 || found[0].Subject.CommonName != "SYNTHETIC Root A" {
		t.Errorf("found %d roots", len(found))
	}
	if !slices.Equal(missing, []string{"SYNTHETIC Root C"}) {
		t.Errorf("missing %v", missing)
	}
}

func TestForwardOK(t *testing.T) {
	expected, floor := day("2026-06-01"), day("2026-05-01")
	for _, c := range []struct {
		now  time.Time
		want bool
	}{
		{day("2026-10-21"), true},
		{expected, true},
		{expected.Add(-time.Second), true},
		{expected.Add(-time.Minute), false},
		{day("2026-01-01"), false},
	} {
		if got := forwardOK(c.now, expected, floor); got != c.want {
			t.Errorf("%s: got %t", c.now, got)
		}
	}
	if forwardOK(day("2026-04-01"), day("2026-03-01"), floor) {
		t.Error("clock before the last confirmed time accepted")
	}
}

func TestTimeLines(t *testing.T) {
	now := day("2026-10-21").Add(12 * time.Hour)
	synced := clockState{Running: true, Synced: true, Ref: now.Add(-time.Minute)}
	step := int64(142 * 24 * 3600)
	for _, c := range []struct {
		boot timeBoot
		want []string
	}{
		{timeBoot{}, []string{"Time: confirmed by NTS at "}},
		{timeBoot{State: "unconfirmed", Step: step}, []string{"Time: not confirmed, set by NTS with the certificate date check off", "  The clock was 142 days behind"}},
		{timeBoot{State: "bootstrap"}, []string{"Time: not confirmed, set by NTS with the certificate date check off", "  The clock is behind"}},
		{timeBoot{State: "confirmed", Step: step}, []string{"Time: confirmed by NTS at ", "  The clock was 142 days behind at boot; set with the date check off, then confirmed"}},
		{timeBoot{State: "refused", Step: -3600}, []string{"Time: confirmed by NTS at ", "  An NTS update tried to move the clock back"}},
		{timeBoot{State: "failed"}, []string{"Time: confirmed by NTS at ", "  The clock looks behind"}},
	} {
		got := timeLines(synced, c.boot, now)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %q", c.boot.State, got)
		}
		for i := range got {
			if !strings.HasPrefix(got[i], c.want[i]) {
				t.Errorf("%s line %d: %q", c.boot.State, i, got[i])
			}
		}
	}
	if got := spanText(5 * time.Hour); got != "5 hours" {
		t.Errorf("spanText %q", got)
	}
}

// fakeTime is a machine whose clock and chronyd the test controls.
type fakeTime struct {
	wall    time.Time
	mono    time.Duration
	synced  bool
	ref     time.Time
	actions []string
	set     []time.Time
	// onChronyd runs for every systemctl action on chronyd.
	onChronyd func(f *fakeTime, action string)
}

func newTimeCheck(t *testing.T, f *fakeTime, chains map[string][]*x509.Certificate, bundle []byte) (*timeCheck, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"run/chrony", "lib", "chrony"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bundle.crt"), bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	var servers []string
	for host := range chains {
		servers = append(servers, host)
	}
	var stderr bytes.Buffer
	return &timeCheck{
		servers:   servers,
		roots:     []string{"SYNTHETIC Root A"},
		wait:      90 * time.Second,
		runDir:    filepath.Join(dir, "run"),
		stampPath: filepath.Join(dir, "lib", "confirmed"),
		chronyDir: filepath.Join(dir, "chrony"),
		bundle:    filepath.Join(dir, "bundle.crt"),
		stderr:    &stderr,
		clock: func() clockState {
			return clockState{Running: true, Synced: f.synced, Ref: f.ref}
		},
		now:     func() time.Time { return f.wall },
		elapsed: func() time.Duration { return f.mono },
		sleep: func(d time.Duration) {
			f.mono += d
			f.wall = f.wall.Add(d)
		},
		probe: func(host string) ([]*x509.Certificate, error) {
			if chains[host] == nil {
				return nil, os.ErrDeadlineExceeded
			}
			return chains[host], nil
		},
		chronyd: func(action string) error {
			f.actions = append(f.actions, action)
			if f.onChronyd != nil {
				f.onChronyd(f, action)
			}
			return nil
		},
		setClock: func(t time.Time) error {
			f.set = append(f.set, t)
			f.wall = t
			return nil
		},
	}, &stderr
}

// The clock as an install with a dead RTC battery might start it, months
// before the servers' certificates.
func behindChains(t *testing.T, ca testCA) map[string][]*x509.Certificate {
	return map[string][]*x509.Certificate{
		"a.example.org": ca.leaf(t, "a.example.org", day("2026-02-17"), day("2027-02-17")),
		"b.example.org": ca.leaf(t, "b.example.org", day("2026-09-18"), day("2026-12-17")),
		"c.example.org": ca.leaf(t, "c.example.org", day("2026-09-13"), day("2026-12-12")),
		"d.example.org": nil,
	}
}

func TestTimeCheckSyncedWritesStamp(t *testing.T) {
	ca := newTestCA(t, "SYNTHETIC Root A")
	f := &fakeTime{wall: day("2026-10-21"), synced: true}
	f.ref = f.wall.Add(-time.Minute)
	c, stderr := newTimeCheck(t, f, behindChains(t, ca), ca.pem())
	if code := c.run(); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if !c.readFloor().Equal(f.ref) || len(f.actions) != 0 {
		t.Fatalf("stamp %s, actions %v", c.readFloor(), f.actions)
	}
	if _, ran := c.readState(); ran {
		t.Fatal("state written without an exception")
	}
}

func TestTimeCheckBootstrapConfirms(t *testing.T) {
	ca := newTestCA(t, "SYNTHETIC Root A")
	real := day("2026-10-21")
	f := &fakeTime{wall: day("2026-06-01")}
	c, stderr := newTimeCheck(t, f, behindChains(t, ca), ca.pem())
	dump := filepath.Join(c.chronyDir, "a.nts")
	f.onChronyd = func(f *fakeTime, action string) {
		switch action {
		case "restart":
			conf, err := os.ReadFile(c.bootstrapConf())
			if err != nil || !strings.Contains(string(conf), "nocerttimecheck 1\nnosystemcert\n") {
				t.Errorf("exception at restart: %q %v", conf, err)
			}
			_ = os.WriteFile(dump, []byte("SYNTHETIC"), 0o600)
			f.wall, f.synced, f.ref = real, true, real
		case "stop":
			f.synced = false
		case "start":
			if _, err := os.Stat(dump); err == nil {
				t.Error("keys from the exception kept")
			}
			f.synced, f.ref = true, f.wall
		}
	}
	if code := c.run(); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if !slices.Equal(f.actions, []string{"restart", "stop", "start"}) {
		t.Errorf("actions %v", f.actions)
	}
	if _, err := os.Stat(c.bootstrapConf()); err == nil {
		t.Error("exception left on")
	}
	boot, _ := c.readState()
	if boot.State != "confirmed" || boot.Step < 140*24*3600 {
		t.Errorf("state %+v", boot)
	}
	if c.readFloor().IsZero() {
		t.Error("no stamp")
	}
	info, err := os.Stat(c.rootsPath())
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("roots: %v", err)
	}

	// Once per boot: a later run that does not see the clock confirmed
	// does not try again.
	f.synced, f.actions = false, nil
	if code := c.run(); code != 0 || len(f.actions) != 0 {
		t.Errorf("second run: code %d, actions %v", code, f.actions)
	}
}

func TestTimeCheckRefusesClockBack(t *testing.T) {
	ca := newTestCA(t, "SYNTHETIC Root A")
	f := &fakeTime{wall: day("2026-06-01")}
	c, stderr := newTimeCheck(t, f, behindChains(t, ca), ca.pem())
	f.onChronyd = func(f *fakeTime, action string) {
		if action == "restart" {
			f.wall, f.synced, f.ref = day("2026-05-01"), true, day("2026-05-01")
		}
		if action == "stop" {
			f.synced = false
		}
	}
	if code := c.run(); code != 1 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if len(f.set) != 1 || f.set[0].Before(day("2026-06-01")) {
		t.Errorf("clock put back to %v", f.set)
	}
	if boot, _ := c.readState(); boot.State != "refused" {
		t.Errorf("state %+v", boot)
	}
	if !strings.Contains(stderr.String(), "ERROR: the update moved the clock back") {
		t.Errorf("stderr %s", stderr)
	}
}

func TestTimeCheckNoExceptionWhenAhead(t *testing.T) {
	ca := newTestCA(t, "SYNTHETIC Root A")
	f := &fakeTime{wall: day("2027-01-01")}
	c, stderr := newTimeCheck(t, f, behindChains(t, ca), ca.pem())
	if code := c.run(); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if len(f.actions) != 0 {
		t.Errorf("actions %v", f.actions)
	}
	if _, ran := c.readState(); ran {
		t.Error("state written")
	}
	if !strings.Contains(stderr.String(), "may be ahead") {
		t.Errorf("stderr %s", stderr)
	}
}

func TestTimeCheckDropsLeftoverException(t *testing.T) {
	ca := newTestCA(t, "SYNTHETIC Root A")
	f := &fakeTime{wall: day("2026-10-21")}
	c, stderr := newTimeCheck(t, f, behindChains(t, ca), ca.pem())
	c.writeState(timeBoot{State: "bootstrap", Step: 3600})
	if err := os.WriteFile(c.bootstrapConf(), []byte("nocerttimecheck 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.onChronyd = func(f *fakeTime, action string) {
		if action == "start" {
			f.synced, f.ref = true, f.wall
		}
	}
	if code := c.run(); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if _, err := os.Stat(c.bootstrapConf()); err == nil {
		t.Error("exception left on")
	}
	if boot, _ := c.readState(); boot.State != "confirmed" {
		t.Errorf("state %+v", boot)
	}
}

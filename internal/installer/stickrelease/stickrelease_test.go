package stickrelease

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 3072)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

func pubPEM(t *testing.T, k *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func sign(t *testing.T, k *rsa.PrivateKey, data []byte) []byte {
	t.Helper()
	d := sha256.Sum256(data)
	sig, err := rsa.SignPSS(rand.Reader, k, crypto.SHA256, d[:], &rsa.PSSOptions{SaltLength: 32, Hash: crypto.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func sum(data []byte) string {
	d := sha256.Sum256(data)
	return hex.EncodeToString(d[:])
}

// files are a release folder as the release CI publishes it.
type files map[string][]byte

func release(t *testing.T, version, floor string) files {
	t.Helper()
	rel := []byte(fmt.Sprintf("version=%s\ndate=2026-11-16\nfloor=%s\n", version, floor))
	sums := []byte(fmt.Sprintf("%s  gjallarOS-installer-%s.iso\n%s  gjlogs.img\n%s  RELEASE\n%s  make-stick.sh\n",
		strings.Repeat("a", 64), version, strings.Repeat("b", 64), sum(rel), strings.Repeat("c", 64)))
	return files{"SHA256SUMS": sums, "SHA256SUMS.sig": sign(t, key(t), sums), "RELEASE": rel}
}

func (f files) resign(t *testing.T) {
	f["SHA256SUMS.sig"] = sign(t, key(t), f["SHA256SUMS"])
}

func serve(t *testing.T, f files) (Config, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := f[strings.TrimPrefix(r.URL.Path, "/latest/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return Config{Version: "v2026.10.66", URL: srv.URL + "/latest", Key: &key(t).PublicKey}, NewClient(pool)
}

func TestCheckKinds(t *testing.T) {
	for _, tc := range []struct {
		version, floor string
		want           Kind
	}{
		{"v2026.10.66", "v2026.10.60", Current},
		{"v2026.10.65", "v2026.10.60", Current}, // an old RELEASE tells nothing new
		{"v2026.10.67", "v2026.10.60", Older},
		{"v2026.11.1", "v2026.10.66", Older},
		{"v2026.11.1", "v2026.10.67", BelowFloor},
		{"v2026.10.100", "v2026.10.100", BelowFloor}, // 100 > 66, not by text
	} {
		c, client := serve(t, release(t, tc.version, tc.floor))
		o := Check(context.Background(), c, client)
		if o.Kind != tc.want || o.Err != nil {
			t.Errorf("%s floor %s: %v %v, want %v", tc.version, tc.floor, o.Kind, o.Err, tc.want)
		}
		if o.MayInstall() != (tc.want != BelowFloor) {
			t.Errorf("%s floor %s: MayInstall %v", tc.version, tc.floor, o.MayInstall())
		}
		if o.Newest.Version != tc.version || o.Newest.Floor != tc.floor || o.Stick != "v2026.10.66" {
			t.Errorf("outcome %+v", o)
		}
	}
}

func TestCheckNoKey(t *testing.T) {
	c, client := serve(t, release(t, "v2026.10.66", "v2026.10.60"))
	c.Key = nil
	if o := Check(context.Background(), c, client); o.Kind != NoKey || o.MayInstall() {
		t.Fatalf("got %+v", o)
	}
}

func TestCheckUnreachable(t *testing.T) {
	c, client := serve(t, release(t, "v2026.10.66", "v2026.10.60"))
	c.URL = strings.TrimSuffix(c.URL, "/latest") + "/missing"
	if o := Check(context.Background(), c, client); o.Kind != Unreachable || o.MayInstall() {
		t.Fatalf("got %+v", o)
	}
	// A server the system does not trust is unreachable too.
	c, _ = serve(t, release(t, "v2026.10.66", "v2026.10.60"))
	if o := Check(context.Background(), c, NewClient(x509.NewCertPool())); o.Kind != Unreachable {
		t.Fatalf("untrusted TLS: %+v", o)
	}
}

func TestCheckBad(t *testing.T) {
	other, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(f files){
		"other key": func(f files) { f["SHA256SUMS.sig"] = sign(t, other, f["SHA256SUMS"]) },
		"sums changed": func(f files) {
			f["SHA256SUMS"] = append(f["SHA256SUMS"], []byte(strings.Repeat("d", 64)+"  make-stick.ps1\n")...)
		},
		"release changed": func(f files) { f["RELEASE"] = []byte("version=v2026.10.66\ndate=2026-11-16\nfloor=v2026.01.1\n") },
		"sums too big": func(f files) {
			f["SHA256SUMS"] = []byte(strings.Repeat(strings.Repeat("e", 64)+"  make-stick.sh\n", 1+maxSums/80))
			f.resign(t)
		},
		"sig too big": func(f files) { f["SHA256SUMS.sig"] = make([]byte, maxSig+1) },
		"two isos": func(f files) {
			f["SHA256SUMS"] = append(f["SHA256SUMS"], []byte(strings.Repeat("d", 64)+"  gjallarOS-installer-v2026.10.70.iso\n")...)
			f.resign(t)
		},
		"unknown file": func(f files) {
			f["SHA256SUMS"] = append(f["SHA256SUMS"], []byte(strings.Repeat("d", 64)+"  evil.sh\n")...)
			f.resign(t)
		},
		"no gjlogs line": func(f files) {
			s := string(f["SHA256SUMS"])
			f["SHA256SUMS"] = []byte(s[:strings.Index(s, strings.Repeat("b", 64))] + s[strings.Index(s, "  gjlogs.img\n")+len("  gjlogs.img\n"):])
			f.resign(t)
		},
	} {
		f := release(t, "v2026.10.70", "v2026.10.60")
		change(f)
		c, client := serve(t, f)
		if o := Check(context.Background(), c, client); o.Kind != Bad || !errors.Is(o.Err, ErrBad) || o.MayInstall() {
			t.Errorf("%s: got %v %v", name, o.Kind, o.Err)
		}
	}
}

func TestParseSums(t *testing.T) {
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	good := a + "  gjallarOS-installer-v2026.10.66.iso\n" + b + "  gjlogs.img\n" + c + "  RELEASE\n"
	if tag, rel, err := parseSums([]byte(good)); err != nil || tag != "v2026.10.66" || rel != c {
		t.Fatalf("good: %q %q %v", tag, rel, err)
	}
	for _, s := range []string{
		strings.TrimSuffix(good, "\n"),
		good + "\n",
		strings.Replace(good, "  gjlogs", " gjlogs", 1),
		strings.Replace(good, a, strings.ToUpper(a), 1),
		good + b + "  gjlogs.img\n",
		good + c + "  RELEASE\n",
		strings.Replace(good, "v2026.10.66.iso", "v2026.10.66.iso.x", 1),
		strings.Replace(good, "gjallarOS-installer", "../installer", 1),
		b + "  gjlogs.img\n" + c + "  RELEASE\n",
		strings.Replace(good, "\n", "\r\n", 1),
	} {
		if _, _, err := parseSums([]byte(s)); !errors.Is(err, ErrBad) {
			t.Errorf("took %q", s)
		}
	}
}

func TestParseRelease(t *testing.T) {
	good := "version=v2026.10.66\ndate=2026-11-16\nfloor=v2026.10.60\n"
	if r, err := parseRelease([]byte(good), "v2026.10.66"); err != nil || r != (Release{"v2026.10.66", "2026-11-16", "v2026.10.60"}) {
		t.Fatalf("good: %+v %v", r, err)
	}
	for _, s := range []string{
		"version=v2026.10.67\ndate=2026-11-16\nfloor=v2026.10.60\n",
		"version=v2026.10.66\ndate=2026-13-16\nfloor=v2026.10.60\n",
		"version=v2026.10.66\ndate=2026-11-16\nfloor=v2026.10.67\n",
		"version=v2026.10.66\ndate=2026-11-16\nfloor=v2026.13.1\n",
		"version=v2026.10.66\ndate=2026-11-16\nfloor=v2026.10.60",
		"version=v2026.10.66\ndate=2026-11-16\nfloor=v2026.10.60\nx=1\n",
		"version=v2026.10.66\ndate=2026-11-16\nfloor=v2026.10.60 \n",
		"version=v2026.10.66\nfloor=v2026.10.60\ndate=2026-11-16\n",
		"version=v2026.10.66\ndate=2026-11-16\nfloor=\x1b[2Jv2026.10.60\n",
	} {
		if _, err := parseRelease([]byte(s), "v2026.10.66"); !errors.Is(err, ErrBad) {
			t.Errorf("took %q", s)
		}
	}
	// The ISO name is checked loosely in SHA256SUMS; RELEASE needs a real
	// release name.
	if _, err := parseRelease([]byte("version=v2026.13.1\ndate=2026-11-16\nfloor=v2026.10.60\n"), "v2026.13.1"); !errors.Is(err, ErrBad) {
		t.Error("took month 13")
	}
}

func TestVerKeyOrder(t *testing.T) {
	for _, p := range [][2]string{{"v2026.10.9", "v2026.10.10"}, {"v2026.09.100", "v2026.10.1"}, {"v2025.12.5", "v2026.01.1"}, {"v2026.10.07", "v2026.10.8"}} {
		if !less(p[0], p[1]) || less(p[1], p[0]) {
			t.Errorf("%s < %s", p[0], p[1])
		}
	}
	for _, n := range []string{"v2026.10", "2026.10.1", "v2026.00.1", "v2026.10.1234567", "v2026.1.1", "v2026.10.-1", "v2026.10.+1"} {
		if _, ok := verKey(n); ok {
			t.Errorf("took %q", n)
		}
	}
}

func TestParseConfig(t *testing.T) {
	pub := pubPEM(t, &key(t).PublicKey)
	cfg := func(m map[string]any) []byte {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	base := func() map[string]any {
		return map[string]any{"version": "v2026.10.66", "url": "https://example.org/releases/latest/download", "key": pub}
	}
	c, err := ParseConfig(cfg(base()))
	if err != nil || c.Key == nil || c.Version != "v2026.10.66" {
		t.Fatalf("good: %+v %v", c, err)
	}
	m := base()
	m["key"] = nil
	if c, err := ParseConfig(cfg(m)); err != nil || c.Key != nil {
		t.Fatalf("null key: %+v %v", c, err)
	}
	small, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(m map[string]any){
		"http":        func(m map[string]any) { m["url"] = "http://example.org/x" },
		"slash":       func(m map[string]any) { m["url"] = "https://example.org/x/" },
		"user":        func(m map[string]any) { m["url"] = "https://a@example.org/x" },
		"query":       func(m map[string]any) { m["url"] = "https://example.org/x?y" },
		"version":     func(m map[string]any) { m["version"] = "2026.10.66" },
		"extra":       func(m map[string]any) { m["floor"] = "v2026.10.1" },
		"no key text": func(m map[string]any) { m["key"] = "x" },
		"small key":   func(m map[string]any) { m["key"] = pubPEM(t, &small.PublicKey) },
		"two keys":    func(m map[string]any) { m["key"] = pub + pub },
	} {
		m := base()
		change(m)
		if _, err := ParseConfig(cfg(m)); err == nil {
			t.Errorf("%s: took it", name)
		}
	}
	if _, err := ParseConfig(append(cfg(base()), []byte(`{}`)...)); err == nil {
		t.Error("took two objects")
	}
}

// A redirect to plain http is refused.
func TestRedirectHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/SHA256SUMS", http.StatusFound)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c := Config{Version: "v2026.10.66", URL: srv.URL, Key: &key(t).PublicKey}
	o := Check(context.Background(), c, NewClient(pool))
	if o.Kind != Unreachable || o.Err == nil || !strings.Contains(o.Err.Error(), "https") {
		t.Fatalf("got %+v", o)
	}
}

// Package stickrelease checks the installer stick against the newest signed
// release before it installs (Q233). The stick installs its own tree, so a
// stick below the release floor would install a system with known holes:
// the menu refuses to install from it. The check reads the same signed
// files as the USB makers (SHA256SUMS, SHA256SUMS.sig, RELEASE) and parses
// them as strictly as make-stick.sh does.
//
// The menu runs as root and everything fetched here comes from the network:
// each file has a size cap, SHA256SUMS is checked against the release key
// before a byte of it is parsed, RELEASE must match its signed checksum, and
// only release names that passed the strict pattern reach the screen.
package stickrelease

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Size caps, the same as the makers'.
const (
	maxSums    = 64 << 10
	maxSig     = 4 << 10
	maxRelease = 512
	// MaxConfig caps the stick's own config file.
	MaxConfig = 16 << 10
	// minKeyBits is the smallest release key taken.
	minKeyBits = 3072
)

// ErrBad marks files that are not what the release key signed, or not in
// the published form.
var ErrBad = errors.New("not the files we published")

// Config is the stick's own release and where the newest one is, from the
// image (stick.nix writes it into the store).
type Config struct {
	// Version is the release this stick installs, as system/release.
	Version string
	// URL is the folder of the newest release's files, https only.
	URL string
	// Key is the release key; nil on a build without one.
	Key *rsa.PublicKey
}

type configFile struct {
	Version string  `json:"version"`
	URL     string  `json:"url"`
	Key     *string `json:"key"`
}

// ParseConfig reads the stick's config: version, url and key (PEM, or null
// on a build without the release key). Unknown fields are refused.
func ParseConfig(data []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f configFile
	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("release config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("release config: data after the object")
	}
	if _, ok := verKey(f.Version); !ok {
		return Config{}, fmt.Errorf("release config: version %q is not a release name", f.Version)
	}
	u, err := url.Parse(f.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(u.Path, "/") {
		return Config{}, fmt.Errorf("release config: url %q is not an https folder", f.URL)
	}
	c := Config{Version: f.Version, URL: f.URL}
	if f.Key != nil {
		c.Key, err = parseKey(*f.Key)
		if err != nil {
			return Config{}, fmt.Errorf("release config: %w", err)
		}
	}
	return c, nil
}

func parseKey(s string) (*rsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(s))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("key is not one PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("key: %w", err)
	}
	rk, ok := k.(*rsa.PublicKey)
	if !ok || rk.N.BitLen() < minKeyBits {
		return nil, fmt.Errorf("key is not an RSA key of at least %d bits", minKeyBits)
	}
	return rk, nil
}

// Release is the newest release's RELEASE file.
type Release struct {
	Version string
	Date    string
	Floor   string
}

// Kind is what the check found.
type Kind int

const (
	// Current: the stick is the newest release, or newer.
	Current Kind = iota
	// Older: a newer release is out; the stick is still at or above the
	// floor and may install.
	Older
	// BelowFloor: the stick is older than the oldest release still safe.
	BelowFloor
	// NoKey: the stick was built without the release key.
	NoKey
	// Unreachable: the release files could not be fetched.
	Unreachable
	// Bad: the files are not what the release key signed.
	Bad
)

// Outcome is the result of Check.
type Outcome struct {
	Kind Kind
	// Stick is the stick's own release.
	Stick string
	// Newest is the signed RELEASE for Current, Older and BelowFloor.
	Newest Release
	// Err is the reason for Unreachable and Bad, for the log only: it can
	// hold text from the network.
	Err error
}

// MayInstall reports whether the menu may go on to install.
func (o Outcome) MayInstall() bool { return o.Kind == Current || o.Kind == Older }

// Check fetches the newest signed release and compares the stick with it.
// client nil means NewClient(nil).
func Check(ctx context.Context, c Config, client *http.Client) Outcome {
	if c.Key == nil {
		return Outcome{Kind: NoKey, Stick: c.Version}
	}
	if client == nil {
		client = NewClient(nil)
	}
	r, err := fetch(ctx, client, c)
	switch {
	case errors.Is(err, ErrBad):
		return Outcome{Kind: Bad, Stick: c.Version, Err: err}
	case err != nil:
		return Outcome{Kind: Unreachable, Stick: c.Version, Err: err}
	}
	return Outcome{Kind: compare(c.Version, r), Stick: c.Version, Newest: r}
}

// compare: a RELEASE older than the stick (a cache or a replay) tells
// nothing new, so the stick counts as current.
func compare(stick string, r Release) Kind {
	switch {
	case less(stick, r.Floor):
		return BelowFloor
	case less(stick, r.Version):
		return Older
	}
	return Current
}

// NewClient returns the https client for the check: TLS 1.2 or newer, no
// proxy from the environment, redirects only to https (GitHub sends the
// files from another host), and time limits. roots nil means the system's.
func NewClient(roots *x509.CertPool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("redirect away from https")
			}
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

var (
	sumsLine  = regexp.MustCompile(`^([0-9a-f]{64})  ([A-Za-z0-9._-]+)$`)
	isoName   = regexp.MustCompile(`^gjallarOS-installer-(v[0-9]{4}\.[0-9]{2}\.[0-9]+)\.iso$`)
	dateField = regexp.MustCompile(`^date=([0-9]{4}-[0-9]{2}-[0-9]{2})$`)
	verName   = regexp.MustCompile(`^v([0-9]{4})\.(0[1-9]|1[0-2])\.([0-9]{1,6})$`)
)

func fetch(ctx context.Context, client *http.Client, c Config) (Release, error) {
	sums, err := get(ctx, client, c.URL+"/SHA256SUMS", maxSums)
	if err != nil {
		return Release{}, err
	}
	sig, err := get(ctx, client, c.URL+"/SHA256SUMS.sig", maxSig)
	if err != nil {
		return Release{}, err
	}
	digest := sha256.Sum256(sums)
	if err := rsa.VerifyPSS(c.Key, crypto.SHA256, digest[:], sig, &rsa.PSSOptions{SaltLength: 32, Hash: crypto.SHA256}); err != nil {
		return Release{}, fmt.Errorf("SHA256SUMS: %w", ErrBad)
	}
	isoTag, relSum, err := parseSums(sums)
	if err != nil {
		return Release{}, err
	}
	rel, err := get(ctx, client, c.URL+"/RELEASE", maxRelease)
	if err != nil {
		return Release{}, err
	}
	if got := sha256.Sum256(rel); hex.EncodeToString(got[:]) != relSum {
		return Release{}, fmt.Errorf("RELEASE: %w", ErrBad)
	}
	return parseRelease(rel, isoTag)
}

// get reads url, refusing more than limit bytes.
func get(ctx context.Context, client *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%s: more than %d bytes: %w", u, limit, ErrBad)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: more than %d bytes: %w", u, limit, ErrBad)
	}
	return data, nil
}

// parseSums takes the signed SHA256SUMS as make-stick.sh does: one ISO,
// gjlogs.img, RELEASE and the makers, nothing else, each once. It returns
// the ISO's release name and RELEASE's checksum.
func parseSums(sums []byte) (isoTag, relSum string, err error) {
	bad := fmt.Errorf("SHA256SUMS: %w", ErrBad)
	text := string(sums)
	if !strings.HasSuffix(text, "\n") {
		return "", "", bad
	}
	var img bool
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		m := sumsLine.FindStringSubmatch(line)
		if m == nil {
			return "", "", bad
		}
		switch name := m[2]; name {
		case "gjlogs.img":
			if img {
				return "", "", bad
			}
			img = true
		case "RELEASE":
			if relSum != "" {
				return "", "", bad
			}
			relSum = m[1]
		case "make-stick.sh", "make-stick.ps1":
		default:
			im := isoName.FindStringSubmatch(name)
			if im == nil || isoTag != "" {
				return "", "", bad
			}
			isoTag = im[1]
		}
	}
	if isoTag == "" || !img || relSum == "" {
		return "", "", bad
	}
	return isoTag, relSum, nil
}

// parseRelease takes exactly
//
//	version=vYYYY.MM.N
//	date=YYYY-MM-DD
//	floor=vYYYY.MM.N
//
// with version the ISO's name and floor not above version.
func parseRelease(data []byte, isoTag string) (Release, error) {
	bad := fmt.Errorf("RELEASE: %w", ErrBad)
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		return Release{}, bad
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "version="+isoTag {
		return Release{}, bad
	}
	d := dateField.FindStringSubmatch(lines[1])
	if d == nil {
		return Release{}, bad
	}
	if _, err := time.Parse("2006-01-02", d[1]); err != nil {
		return Release{}, bad
	}
	floor, ok := strings.CutPrefix(lines[2], "floor=")
	if !ok {
		return Release{}, bad
	}
	if _, ok := verKey(isoTag); !ok {
		return Release{}, bad
	}
	if _, ok := verKey(floor); !ok || less(isoTag, floor) {
		return Release{}, bad
	}
	return Release{Version: isoTag, Date: d[1], Floor: floor}, nil
}

// verKey turns a release name into a key that sorts by release, as the
// makers' ver_key does.
func verKey(name string) (string, bool) {
	m := verName.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s%s%06d", m[1], m[2], n), true
}

// less reports a < b for two names that passed verKey.
func less(a, b string) bool {
	ka, _ := verKey(a)
	kb, _ := verKey(b)
	return ka < kb
}

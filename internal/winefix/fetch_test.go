package winefix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// SYNTHETIC download content.
var fetchBody = []byte("SYNTHETIC fix file for the fetch test\n")

func fetchServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/file.exe", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(fetchBody)
	})
	mux.HandleFunc("/big.exe", func(w http.ResponseWriter, r *http.Request) {
		w.Write(append(fetchBody, 'x'))
	})
	mux.HandleFunc("/gone.exe", http.NotFound)
	mux.HandleFunc("/to-http.exe", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/file.exe", http.StatusFound)
	})
	mux.HandleFunc("/to-https.exe", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file.exe", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func pinned(url string) File {
	sum := sha256.Sum256(fetchBody)
	return File{Verb: "demo", URL: url, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(fetchBody))}
}

func TestFetch(t *testing.T) {
	srv, hits := fetchServer(t)
	dir := t.TempDir()
	f := pinned(srv.URL + "/file.exe")
	got, err := Fetch(context.Background(), srv.Client(), dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, f.SHA256) {
		t.Errorf("path %s", got)
	}
	if data, err := os.ReadFile(got); err != nil || string(data) != string(fetchBody) {
		t.Errorf("content %q %v", data, err)
	}
	if st, err := os.Stat(got); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", st.Mode(), err)
	}
	// A second fetch uses the kept file.
	if _, err := Fetch(context.Background(), srv.Client(), dir, f); err != nil || hits.Load() != 1 {
		t.Errorf("second fetch: %v, %d downloads", err, hits.Load())
	}
	// A changed kept file is downloaded again.
	if err := os.WriteFile(got, []byte("SYNTHETIC changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), srv.Client(), dir, f); err != nil || hits.Load() != 2 {
		t.Errorf("after change: %v, %d downloads", err, hits.Load())
	}
	// So is a link in its place.
	os.Remove(got)
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), got); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), srv.Client(), dir, f); err != nil || hits.Load() != 3 {
		t.Errorf("after link: %v, %d downloads", err, hits.Load())
	}
	if st, err := os.Lstat(got); err != nil || !st.Mode().IsRegular() {
		t.Errorf("link kept: %v %v", st, err)
	}
	// A redirect that stays on https is fine.
	if _, err := Fetch(context.Background(), srv.Client(), t.TempDir(), pinned(srv.URL+"/to-https.exe")); err != nil {
		t.Errorf("https redirect: %v", err)
	}
}

func TestFetchRefuses(t *testing.T) {
	srv, _ := fetchServer(t)
	other := pinned(srv.URL + "/file.exe")
	other.SHA256 = strings.Repeat("0", 64)
	short := pinned(srv.URL + "/file.exe")
	short.Size--
	for name, tc := range map[string]struct {
		f    File
		want string
	}{
		"sha256":   {other, "sha256"},
		"larger":   {pinned(srv.URL + "/big.exe"), "larger"},
		"size":     {short, "larger"},
		"status":   {pinned(srv.URL + "/gone.exe"), "404"},
		"redirect": {pinned(srv.URL + "/to-http.exe"), "off https"},
		"http":     {pinned("http://" + srv.Listener.Addr().String() + "/file.exe"), "not an https"},
		"pin":      {File{URL: srv.URL + "/file.exe", SHA256: "zz", Size: 1}, "no usable pin"},
		"huge":     {File{URL: srv.URL + "/file.exe", SHA256: other.SHA256, Size: MaxFile + 1}, "no usable pin"},
	} {
		dir := t.TempDir()
		_, err := Fetch(context.Background(), srv.Client(), dir, tc.f)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Errorf("%s: left %v", name, left)
		}
	}
}

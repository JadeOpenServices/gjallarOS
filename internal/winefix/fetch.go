package winefix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
)

// Fetch gives the path of f in the download folder dir, downloading it
// first when dir does not hold it yet. A file is kept under its sha256 and
// used only after its size and sha256 match the table, also when it was
// downloaded before. A download goes over https only, redirects included,
// and stops one byte past the pinned size. Nothing is ever run here: the
// file only goes into the app's own sandbox (fix.go).
func Fetch(ctx context.Context, client *http.Client, dir string, f File) (string, error) {
	if len(f.SHA256) != sha256.Size*2 || f.Size <= 0 || f.Size > MaxFile {
		return "", fmt.Errorf("%s has no usable pin", f.Name())
	}
	if _, err := hex.DecodeString(f.SHA256); err != nil {
		return "", fmt.Errorf("%s has no usable pin", f.Name())
	}
	dst := filepath.Join(dir, f.SHA256)
	switch err := verify(dst, f); {
	case err == nil:
		return dst, nil
	case errors.Is(err, os.ErrNotExist):
	default:
		// A wrong file under the name: drop it and download again.
		if err := os.Remove(dst); err != nil {
			return "", err
		}
	}

	if u, err := url.Parse(f.URL); err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("%s is not an https address", f.URL)
	}
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("%s sends the download off https", via[len(via)-1].URL.Host)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", f.Name(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", f.Name(), resp.Status)
	}

	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, f.Size+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", f.Name(), err)
	}
	if err := match(f, n, h.Sum(nil)); err != nil {
		return "", err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// verify checks the file at p against f's pin without following a link.
func verify(p string, f File) error {
	fd, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return fmt.Errorf("%s is a link", p)
		}
		return err
	}
	defer fd.Close()
	st, err := fd.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a plain file", p)
	}
	if st.Size() != f.Size {
		return match(f, st.Size(), nil)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(fd, f.Size+1))
	if err != nil {
		return err
	}
	return match(f, n, h.Sum(nil))
}

func match(f File, n int64, sum []byte) error {
	switch {
	case n > f.Size:
		return fmt.Errorf("%s is larger than the %d bytes the table pins", f.Name(), f.Size)
	case n != f.Size:
		return fmt.Errorf("%s has %d bytes, the table pins %d", f.Name(), n, f.Size)
	}
	want, _ := hex.DecodeString(f.SHA256)
	if !bytes.Equal(sum, want) {
		return fmt.Errorf("%s does not match its pinned sha256", f.Name())
	}
	return nil
}

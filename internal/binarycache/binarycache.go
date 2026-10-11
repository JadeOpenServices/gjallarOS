// Package binarycache reads the GjallarOS release cache from
// deployment/binary-cache.json. system/maintenance/binary-cache.nix adds it to
// nix.conf, but that only takes effect after the first switch. Installs and
// rebuilds pass it on the command line as well, so the build that brings the
// setting in can already download instead of compiling.
package binarycache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type cache struct {
	URL       string `json:"url"`
	PublicKey string `json:"publicKey"`
}

// Args returns the nix options that add the cache, or nil when the checkout
// names none. The rules match the NixOS module: an https url and a key.
func Args(repo string) []string {
	data, err := os.ReadFile(filepath.Join(repo, "deployment", "binary-cache.json"))
	if err != nil {
		return nil
	}
	var c cache
	if json.Unmarshal(data, &c) != nil {
		return nil
	}
	if !strings.HasPrefix(c.URL, "https://") || c.PublicKey == "" {
		return nil
	}
	return []string{
		"--option", "extra-substituters", c.URL,
		"--option", "extra-trusted-public-keys", c.PublicKey,
	}
}

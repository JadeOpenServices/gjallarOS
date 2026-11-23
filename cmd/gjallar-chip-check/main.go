// Command gjallar-chip-check asks systemd-pcrlock at boot whether the TPM can
// carry the startup policy and writes the answer for the settings service,
// which has no device access. The file holds {"chipReason": "..."}; an empty
// reason means chip unlock can be switched on. A chip without
// PolicyAuthorizeNV makes make-policy refuse during the rebuild (friend's
// T480s, .68, 2026-11-22), so the service refuses the change up front.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 2 || !filepath.IsAbs(os.Args[1]) {
		log.Fatal("Usage: gjallar-chip-check /run/gjallar/chip-check.json")
	}
	path := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	reason := chipReason(ctx, diskcrypto.CheckPCRLockSupport)
	data, err := json.Marshal(struct {
		ChipReason string `json:"chipReason"`
	}{reason})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		log.Fatal(err)
	}
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Fatal(err)
	}
	if reason != "" {
		log.Printf("chip unlock cannot be switched on: %s", reason)
	} else {
		log.Print("the security chip supports the startup policy")
	}
}

// chipReason is empty when the chip supports the startup policy. An answer
// that cannot be read counts as unsupported.
func chipReason(ctx context.Context, check func(context.Context) (diskcrypto.PCRLockSupport, error)) string {
	support, err := check(ctx)
	if err != nil {
		return "the security chip could not be checked (" + err.Error() + ")"
	}
	if support != diskcrypto.PCRLockSupported {
		return support.Describe()
	}
	return ""
}

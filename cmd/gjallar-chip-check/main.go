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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
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
	if err := writeFact(path, append(data, '\n'), 0); err != nil {
		log.Fatal(err)
	}
	if reason != "" {
		log.Printf("chip unlock cannot be switched on: %s", reason)
	} else {
		log.Print("the security chip supports the startup policy")
	}
}

// writeFact replaces path with data as root. /run/gjallar is 0755 root
// (tmpfiles), so no user can plant there today; the checks keep it that way
// if a later rule loosens the folder: the folder must be a real directory
// owned by ownerUID and not writable by group or others, and the temporary
// file is created fresh (O_EXCL) without following a link. A planted link or
// file at the temporary name makes the check fail, so no answer is written
// and readers keep chip unlock off. Rename replaces a link at path itself
// instead of writing through it.
func writeFact(path string, data []byte, ownerUID int) error {
	dir := filepath.Dir(path)
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != ownerUID || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is not a directory that only its owner can change", dir)
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
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

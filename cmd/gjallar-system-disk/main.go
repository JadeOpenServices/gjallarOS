// Command gjallar-system-disk checks at boot whether the system runs from a
// USB disk and writes the answer for the settings service, which has no
// device access (wiki 752 Q11). The file holds {"usbReason": "..."}; an
// empty reason means the disk is known and not on USB.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 2 || !filepath.IsAbs(os.Args[1]) {
		log.Fatal("Usage: gjallar-system-disk /run/gjallar/system-disk.json")
	}
	path := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reason := targetdisk.MountedUSBReason(ctx, "/")
	data, err := json.Marshal(struct {
		USBReason string `json:"usbReason"`
	}{reason})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Fatal(err)
	}
	if reason != "" {
		log.Printf("USB blocking cannot be turned on: %s", reason)
	} else {
		log.Print("the system disk is not on USB")
	}
}

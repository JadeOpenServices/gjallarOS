// Command gjallar-storage-icon tells the udev storage icon rule what kind of
// medium a USB disk holds (Plane 233). It runs the classifier the media
// notices use, so the file manager and the notice show the same icon. It
// prints at most one GJALLAR_STORAGE_KIND line, and only a fixed kind name:
// device strings never reach the udev database through it.
package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/mediagate"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 2 {
		log.Fatal("Usage: gjallar-storage-icon DEVPATH")
	}
	if err := run("/sys", os.Args[1], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// run classifies the disk at devpath, a udev $devpath such as
// /devices/.../block/sda.
func run(sysfs, devpath string, w io.Writer) error {
	if !strings.HasPrefix(devpath, "/devices/") || strings.Contains(devpath, "..") {
		return fmt.Errorf("%q is not a device path", devpath)
	}
	scsi, err := filepath.EvalSymlinks(filepath.Join(sysfs, devpath, "device"))
	if err != nil {
		return err
	}
	kind, err := mediagate.DiskKind(sysfs, filepath.Base(scsi))
	if err != nil || kind == "" {
		return err
	}
	_, err = fmt.Fprintf(w, "GJALLAR_STORAGE_KIND=%s\n", kind)
	return err
}

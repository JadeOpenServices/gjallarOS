// Command gjallar-usbguard-boot writes the USBGuard settings the daemon starts
// with. The fallback for devices without a rule comes from the system
// configuration, but while the stored trust state says blocking is armed, or
// cannot be read, it becomes block: until the broker reconciles, a machine
// whose blocking flag was turned off before the disarm stays closed
// (loophole 23, Plane 194).
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

const fallbackKey = "ImplicitPolicyTarget="

func main() {
	log.SetFlags(0)
	if len(os.Args) != 4 {
		log.Fatal("Usage: gjallar-usbguard-boot STATE_DIR IN_CONF OUT_CONF")
	}
	stateDir, in, out := os.Args[1], os.Args[2], os.Args[3]
	for _, path := range os.Args[1:] {
		if !filepath.IsAbs(path) {
			log.Fatalf("%s is not an absolute path", path)
		}
	}
	data, err := os.ReadFile(in)
	if err != nil {
		log.Fatal(err)
	}

	block := false
	armed, err := usbtrust.StoredArmed(stateDir)
	switch {
	case err != nil:
		block = true
		log.Printf("the stored USB blocking state could not be read, so USBGuard starts blocking: %v", err)
	case armed:
		block = true
		log.Print("USB blocking is armed in the stored state, so USBGuard starts blocking")
	}
	if block {
		if data, err = blockFallback(data); err != nil {
			log.Fatal(err)
		}
	}

	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(tmp, out); err != nil {
		log.Fatal(err)
	}
}

// blockFallback sets the fallback line to block. A settings file without
// that line is an error, not a silent allow.
func blockFallback(data []byte) ([]byte, error) {
	var out bytes.Buffer
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(strings.TrimSpace(line), fallbackKey) {
			line = fallbackKey + "block"
			found = true
		}
		fmt.Fprintln(&out, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("the USBGuard settings have no %s line", strings.TrimSuffix(fallbackKey, "="))
	}
	return out.Bytes(), nil
}

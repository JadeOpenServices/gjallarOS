// Package btaddr reads the address of each USB Bluetooth controller, so a
// built-in Bluetooth part can be told apart from a swapped one.
//
// The address is what the controller reports about itself. Vendor commands
// can rewrite it, so it shows that an ordinary part was swapped, not that a
// copy was made.
package btaddr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// Linux include/net/bluetooth/hci_sock.h: HCIGETDEVINFO is
// _IOR('H', 211, int) and fills struct hci_dev_info.
const (
	btprotoHCI    = 1
	hciGetDevInfo = 0x800448d3
	// sizeof(struct hci_dev_info) is 92; the extra room is never read.
	devInfoSize = 128
	// Offsets in struct hci_dev_info.
	devInfoAddress = 10
	devInfoType    = 20
	// HCI_USB, the bus in the low nibble of the type byte.
	busUSB = 1
)

var hciName = regexp.MustCompile(`^hci([0-9]+)$`)

var usbInterface = regexp.MustCompile(`^([0-9]+-[0-9.]+):[0-9]+\.[0-9]+$`)

// InfoFunc returns the raw struct hci_dev_info of controller index.
type InfoFunc func(index int) ([]byte, error)

// Sysfs reads controllers from a sysfs mount, normally "/sys". Info is
// optional and defaults to the HCIGETDEVINFO ioctl.
type Sysfs struct {
	Root string
	Info InfoFunc
}

// Addresses maps the kernel port path of each USB Bluetooth controller
// ("1-5", the same name usbguard reports as via-port) to its address.
//
// It is best effort: a controller that is still starting, switched off by
// rfkill or not readable is left out. A missing address means "not checked
// yet" and never blocks a device on its own.
func (s Sysfs) Addresses(ctx context.Context) map[string]string {
	addresses := map[string]string{}
	if ctx.Err() != nil {
		return addresses
	}

	matches, err := filepath.Glob(
		filepath.Join(s.Root, "class", "bluetooth", "hci*"),
	)
	if err != nil || len(matches) == 0 {
		return addresses
	}

	info := s.Info
	if info == nil {
		info = deviceInfo
	}

	for _, link := range matches {
		name := hciName.FindStringSubmatch(filepath.Base(link))
		if name == nil {
			continue
		}

		index, err := strconv.Atoi(name[1])
		if err != nil {
			continue
		}

		port, ok := usbPort(link)
		if !ok {
			continue
		}

		raw, err := info(index)
		if err != nil {
			continue
		}

		address, ok := parseInfo(raw)
		if !ok {
			continue
		}

		addresses[port] = address
	}

	return addresses
}

// usbPort names the USB device behind hciN. The device link points at the
// USB interface ("1-5:1.0"); its parent directory is the device ("1-5").
func usbPort(hci string) (string, bool) {
	device, err := filepath.EvalSymlinks(filepath.Join(hci, "device"))
	if err != nil {
		return "", false
	}

	match := usbInterface.FindStringSubmatch(filepath.Base(device))
	if match == nil || filepath.Base(filepath.Dir(device)) != match[1] {
		return "", false
	}

	return match[1], true
}

// parseInfo takes the address out of struct hci_dev_info. Controllers on
// other buses, and addresses that are all zero or all ones (not read from
// the controller yet), are skipped.
func parseInfo(raw []byte) (string, bool) {
	if len(raw) <= devInfoType || raw[devInfoType]&0x0f != busUSB {
		return "", false
	}

	address := raw[devInfoAddress : devInfoAddress+6]
	zero, ones := true, true

	for _, b := range address {
		zero = zero && b == 0
		ones = ones && b == 0xff
	}

	if zero || ones {
		return "", false
	}

	// bdaddr_t is stored lowest byte first.
	parts := make([]string, 6)
	for i, b := range address {
		parts[5-i] = fmt.Sprintf("%02X", b)
	}

	return strings.Join(parts, ":"), true
}

func deviceInfo(index int) ([]byte, error) {
	fd, err := syscall.Socket(
		syscall.AF_BLUETOOTH,
		syscall.SOCK_RAW|syscall.SOCK_CLOEXEC,
		btprotoHCI,
	)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	defer syscall.Close(fd)

	buf := make([]byte, devInfoSize)
	buf[0], buf[1] = byte(index), byte(index>>8)

	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		hciGetDevInfo,
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if errno != 0 {
		return nil, os.NewSyscallError("HCIGETDEVINFO", errno)
	}

	return buf, nil
}

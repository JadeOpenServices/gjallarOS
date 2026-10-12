// Package roothub names the USB root port behind each device from sysfs.
// It has no dependency on the hardware probe, so the USB trust broker can
// use it without building the installer's device scanner.
package roothub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

// RootHub is one USB root hub as the kernel shows it.
type RootHub struct {
	// Bus is the kernel bus number ("1" for usb1).
	Bus string
	// Controller is the PCI vendor:device of the host controller, empty
	// when the controller is not a PCI device.
	Controller string
	// Hub is "usb2" or "usb3", empty for other root hub versions.
	Hub string
}

var rootHubPath = regexp.MustCompile(`^usb[0-9]+$`)

var rootPortPath = regexp.MustCompile(`^([0-9]+)-([0-9]+)$`)

// RootHubs reads the root hubs below sysfs (normally "/sys").
func RootHubs(sysfs string) ([]RootHub, error) {
	matches, err := filepath.Glob(
		filepath.Join(sysfs, "bus", "usb", "devices", "usb*"),
	)
	if err != nil {
		return nil, err
	}

	var hubs []RootHub

	for _, link := range matches {
		name := filepath.Base(link)
		if !rootHubPath.MatchString(name) {
			continue
		}

		real, err := filepath.EvalSymlinks(link)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", name, err)
		}

		version, err := readTrimmed(filepath.Join(real, "version"))
		if err != nil {
			return nil, fmt.Errorf("read %s version: %w", name, err)
		}

		parent := filepath.Dir(real)

		hubs = append(hubs, RootHub{
			Bus:        strings.TrimPrefix(name, "usb"),
			Controller: pciID(parent),
			Hub:        hubName(version),
		})
	}

	sort.Slice(hubs, func(i, j int) bool {
		return hubs[i].Bus < hubs[j].Bus
	})

	return hubs, nil
}

// hubName maps a root hub's USB version to the hub name ODDC uses in
// usbPort. ODDC's "usb2"/"usb3" is read as the root hub's USB version,
// not the kernel bus number: on xHCI the USB 3 root hub is often usb2.
func hubName(version string) string {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")

	switch major {
	case "2":
		return "usb2"
	case "3":
		return "usb3"
	}

	return ""
}

func pciID(dir string) string {
	vendor, err := readTrimmed(filepath.Join(dir, "vendor"))
	if err != nil {
		return ""
	}

	device, err := readTrimmed(filepath.Join(dir, "device"))
	if err != nil {
		return ""
	}

	vendor = strings.ToLower(strings.TrimPrefix(vendor, "0x"))
	device = strings.ToLower(strings.TrimPrefix(device, "0x"))

	if len(vendor) != 4 || len(device) != 4 {
		return ""
	}

	return vendor + ":" + device
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

// Wiring names the root port behind a kernel port path like "1-9". It
// returns nil for devices behind another hub, for root hubs and for
// controllers the kernel cannot name.
func Wiring(port string, hubs []RootHub) *usbtrust.ObservedPort {
	match := rootPortPath.FindStringSubmatch(strings.TrimSpace(port))
	if match == nil {
		return nil
	}

	rootPort, err := strconv.Atoi(match[2])
	if err != nil || rootPort <= 0 {
		return nil
	}

	var found *RootHub

	for i := range hubs {
		if hubs[i].Bus == match[1] {
			found = &hubs[i]
			break
		}
	}

	if found == nil || found.Controller == "" || found.Hub == "" {
		return nil
	}

	shared := false

	for _, other := range hubs {
		if other.Bus != found.Bus &&
			other.Controller == found.Controller &&
			other.Hub == found.Hub {
			shared = true
			break
		}
	}

	return &usbtrust.ObservedPort{
		Controller: found.Controller,
		Hub:        found.Hub,
		RootPort:   rootPort,
		Shared:     shared,
	}
}

// AttachWiring sets Wiring on each device from its port path. The path
// is Identity.Port when set (usbguard via-port), else the runtime id
// (sysfs device name).
func AttachWiring(devices []usbtrust.ObservedDevice, hubs []RootHub) {
	for i := range devices {
		port := devices[i].Identity.Port
		if port == "" {
			port = devices[i].RuntimeID
		}

		devices[i].Wiring = Wiring(port, hubs)
	}
}

// Sysfs reads root hubs from a sysfs mount, normally "/sys".
type Sysfs string

func (root Sysfs) RootHubs(ctx context.Context) ([]RootHub, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return RootHubs(string(root))
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/winesandbox"
)

// The doctor's graphics part (Plane 277 CP8, wiki 931) reports what an
// app can use for 3D and speed: the GPUs the kernel has, the Vulkan
// devices Wine sees inside the app's own sandbox, and whether the kernel
// offers ntsync. It only reads; it changes neither the sandbox nor the
// system.

// vulkaninfoPath is vulkan-tools' vulkaninfo, set by the package.
var vulkaninfoPath = "vulkaninfo"

// sysRoot is where /sys, /dev and /run are; tests point it elsewhere.
var sysRoot = "/"

// vulkanNeed is the Vulkan version DXVK 2.x and VKD3D-Proton 3.x need:
// DXVK 2.0 and VKD3D-Proton 2.8 made Vulkan 1.3 the minimum.
const vulkanNeed = "1.3"

// gpu is one GPU the kernel has, from /sys/class/drm/cardN/device.
type gpu struct {
	card    string
	vendor  string // PCI vendor id, "0x1002"
	driver  string // kernel driver, "amdgpu"; empty when none is bound
	bootVGA bool   // the firmware showed its screen on this one
}

var (
	cardName   = regexp.MustCompile(`^card[0-9]{1,3}$`)
	pciID      = regexp.MustCompile(`^0x[0-9a-f]{4,5}$`)
	driverName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// gpus lists the GPUs in /sys/class/drm. Connectors (card1-eDP-1) and
// render nodes are left out; a card whose files do not look as the
// kernel writes them is left out too.
func gpus() ([]gpu, error) {
	dir := filepath.Join(sysRoot, "sys", "class", "drm")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []gpu
	for _, e := range entries {
		if !cardName.MatchString(e.Name()) {
			continue
		}
		dev := filepath.Join(dir, e.Name(), "device")
		vendor := readSmall(filepath.Join(dev, "vendor"))
		if !pciID.MatchString(vendor) {
			continue
		}
		g := gpu{card: e.Name(), vendor: vendor, bootVGA: readSmall(filepath.Join(dev, "boot_vga")) == "1"}
		if link, err := os.Readlink(filepath.Join(dev, "driver")); err == nil && driverName.MatchString(filepath.Base(link)) {
			g.driver = filepath.Base(link)
		}
		out = append(out, g)
	}
	return out, nil
}

// readSmall reads a one-line sysfs file; anything else reads as "".
func readSmall(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64 {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// vendorName names the PCI vendors of GPUs and of the GPUs a virtual
// machine shows.
func vendorName(id string) string {
	switch id {
	case "0x1002":
		return "AMD"
	case "0x8086":
		return "Intel"
	case "0x10de":
		return "NVIDIA"
	case "0x1af4":
		return "virtio"
	case "0x1234":
		return "QEMU"
	}
	return "vendor " + id
}

// vkDevice is one device from vulkaninfo --summary.
type vkDevice struct {
	name   string
	api    string // "1.4.354"
	kind   string // deviceType without its PHYSICAL_DEVICE_TYPE_ prefix
	vendor string // vendorID, "0x1002"
	driver string // driverName, "radv"
}

var (
	vkGPU     = regexp.MustCompile(`^GPU[0-9]{1,2}:$`)
	vkField   = regexp.MustCompile(`^\t([A-Za-z]+) += (.*)$`)
	vkVersion = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,5}$`)
	vkKinds   = map[string]bool{"INTEGRATED_GPU": true, "DISCRETE_GPU": true, "VIRTUAL_GPU": true, "CPU": true, "OTHER": true}
)

// parseVulkan reads the devices from vulkaninfo --summary. It keeps a
// device only when its version, type and vendor look as vulkaninfo
// writes them; names lose control characters and are cut to 80.
func parseVulkan(out []byte) []vkDevice {
	var devs []vkDevice
	in := false
	var cur *vkDevice
	keep := func() {
		if cur != nil && vkVersion.MatchString(cur.api) && vkKinds[cur.kind] && pciID.MatchString(cur.vendor) && cur.name != "" {
			devs = append(devs, *cur)
		}
		cur = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "Devices:":
			in = true
		case !in:
		case vkGPU.MatchString(line):
			keep()
			cur = &vkDevice{}
		case cur != nil:
			m := vkField.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			v := strings.TrimSpace(m[2])
			switch m[1] {
			case "apiVersion":
				cur.api = v
			case "deviceType":
				cur.kind = strings.TrimPrefix(v, "PHYSICAL_DEVICE_TYPE_")
			case "vendorID":
				cur.vendor = v
			case "deviceName":
				cur.name = cleanName(v)
			case "driverName":
				if driverName.MatchString(v) {
					cur.driver = v
				}
			}
		}
	}
	keep()
	return devs
}

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return strings.TrimSpace(s)
}

// atLeast tells whether version a.b.c is at least need (a.b).
func atLeast(version, need string) bool {
	v := strings.SplitN(version, ".", 3)
	n := strings.SplitN(need, ".", 2)
	if len(v) < 2 || len(n) < 2 {
		return false
	}
	vMaj, err1 := strconv.Atoi(v[0])
	vMin, err2 := strconv.Atoi(v[1])
	nMaj, err3 := strconv.Atoi(n[0])
	nMin, err4 := strconv.Atoi(n[1])
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return false
	}
	return vMaj > nMaj || vMaj == nMaj && vMin >= nMin
}

func kindText(kind string) string {
	switch kind {
	case "INTEGRATED_GPU":
		return "built-in GPU"
	case "DISCRETE_GPU":
		return "separate GPU"
	case "VIRTUAL_GPU":
		return "virtual GPU"
	case "CPU":
		return "software, runs on the processor"
	}
	return "other device"
}

// probeVulkan runs vulkaninfo --summary in app id's sandbox, without
// network, so it sees the devices and drivers Wine sees there. The
// caller holds the app's lock.
func probeVulkan(id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := &capped{max: 256 << 10}
	code, err := runOffline(ctx, id, &winesandbox.Tool{Path: vulkaninfoPath}, []string{"--summary"}, out)
	if ctx.Err() != nil {
		return nil, errors.New("vulkaninfo did not answer within 30 seconds")
	}
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return out.buf.Bytes(), fmt.Errorf("vulkaninfo ended with code %d", code)
	}
	return out.buf.Bytes(), nil
}

// ntsync is how far the kernel offers ntsync, the device Wine 11 uses
// for fast thread sync.
type ntsync int

const (
	ntsyncNone      ntsync = iota // not in this kernel
	ntsyncNotLoaded               // a module that is not loaded
	ntsyncOn                      // /dev/ntsync is there
)

func ntsyncState() ntsync {
	if _, err := os.Stat(filepath.Join(sysRoot, "dev", "ntsync")); err == nil {
		return ntsyncOn
	}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		rel := unix.ByteSliceToString(u.Release[:])
		mods, _ := filepath.Glob(filepath.Join(sysRoot, "run", "booted-system", "kernel-modules", "lib", "modules", rel, "kernel", "drivers", "misc", "ntsync.ko*"))
		if len(mods) > 0 {
			return ntsyncNotLoaded
		}
	}
	return ntsyncNone
}

// reportGraphics prints the graphics part of the doctor's report.
func reportGraphics(id string, stdout io.Writer) {
	fmt.Fprintln(stdout, "Graphics:")
	cards, err := gpus()
	if err != nil {
		fmt.Fprintf(stdout, "  could not list the GPUs: %v\n", err)
	}
	out, verr := probeVulkan(id)
	devs := parseVulkan(out)
	for _, line := range graphicsLines(cards, devs, verr, ntsyncState()) {
		fmt.Fprintln(stdout, "  "+line)
	}
}

// graphicsLines turns the facts into the report's lines.
func graphicsLines(cards []gpu, devs []vkDevice, verr error, nt ntsync) []string {
	var lines []string
	for _, g := range cards {
		l := fmt.Sprintf("GPU %s: %s", g.card, vendorName(g.vendor))
		if g.driver != "" {
			l += ", driver " + g.driver
		} else {
			l += ", no driver bound"
		}
		if g.bootVGA {
			l += ", shows the start screen"
		}
		lines = append(lines, l)
	}
	if len(cards) > 1 {
		lines = append(lines, fmt.Sprintf("This device has %d GPUs; which one an app uses is not set per app yet.", len(cards)))
	}

	var fast []vkDevice
	seen := map[string]bool{}
	for _, d := range devs {
		l := fmt.Sprintf("Vulkan in the app's sandbox: %s, Vulkan %s, %s", d.name, d.api, kindText(d.kind))
		if d.driver != "" {
			l += ", driver " + d.driver
		}
		lines = append(lines, l)
		if d.kind != "CPU" {
			seen[d.vendor] = true
			if atLeast(d.api, vulkanNeed) {
				fast = append(fast, d)
			}
		}
	}
	switch {
	case len(devs) == 0 && verr != nil:
		lines = append(lines, fmt.Sprintf("Vulkan in the app's sandbox: none seen (%v).", verr))
	case len(devs) == 0:
		lines = append(lines, "Vulkan in the app's sandbox: none seen.")
	}
	if len(devs) > 0 {
		for _, g := range cards {
			if g.driver != "" && !seen[g.vendor] {
				lines = append(lines, fmt.Sprintf("GPU %s (%s, driver %s) shows no Vulkan in the app's sandbox.", g.card, vendorName(g.vendor), g.driver))
			}
		}
	}
	switch {
	case len(fast) > 0:
		lines = append(lines, fmt.Sprintf("Direct3D through DXVK and VKD3D-Proton needs Vulkan %s: %s has it.", vulkanNeed, fast[0].name))
	case len(devs) > 0 && hasKind(devs, "CPU") && len(seen) == 0:
		lines = append(lines, "Only software Vulkan: Direct3D programs run, but slowly, on the processor.")
	default:
		lines = append(lines, fmt.Sprintf("No GPU with Vulkan %s in the app's sandbox: Direct3D programs through DXVK and VKD3D-Proton fail or run on the processor.", vulkanNeed))
	}

	switch nt {
	case ntsyncOn:
		lines = append(lines, "ntsync (fast thread sync): the kernel has it on, but the app's sandbox does not pass it in yet.")
	case ntsyncNotLoaded:
		lines = append(lines, "ntsync (fast thread sync): this kernel has it as a module that is not loaded; the app does not get it.")
	default:
		lines = append(lines, "ntsync (fast thread sync): not in this kernel; the app does not get it.")
	}
	return lines
}

func hasKind(devs []vkDevice, kind string) bool {
	for _, d := range devs {
		if d.kind == kind {
			return true
		}
	}
	return false
}

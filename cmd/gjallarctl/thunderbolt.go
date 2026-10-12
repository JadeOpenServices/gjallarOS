package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// Thunderbolt and USB4 devices tunnel PCIe, so one that is let in can reach
// memory directly unless the IOMMU confines it. boltd keeps them out until
// someone authorizes them; this is the session side that asks.
const (
	boltName    = "org.freedesktop.bolt"
	boltPath    = dbus.ObjectPath("/org/freedesktop/bolt")
	boltManager = "org.freedesktop.bolt1.Manager"
	boltDevice  = "org.freedesktop.bolt1.Device"
	boltDomain  = "org.freedesktop.bolt1.Domain"
)

type tbDevice struct {
	Path   dbus.ObjectPath
	UID    string
	Name   string
	Vendor string
	Type   string
	Status string
	Stored bool
	Domain string
}

// Bolt UIDs are UUIDs; anything else never reaches boltd from here.
var tbUIDPattern = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

func runThunderbolt(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: gjallarctl thunderbolt {status|review|allow-once|always-allow|forget} [--uid UID]")
		return 2
	}
	switch args[0] {
	case "status":
		return runThunderboltStatus(stdout, stderr)
	case "review":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "FAIL: review takes no arguments")
			return 2
		}
		return runThunderboltReview(stderr)
	case "allow-once", "always-allow", "forget":
	default:
		fmt.Fprintf(stderr, "FAIL: unknown thunderbolt command %q\n", args[0])
		return 2
	}
	f := flag.NewFlagSet("thunderbolt "+args[0], flag.ContinueOnError)
	f.SetOutput(stderr)
	uid := f.String("uid", "", "device UID from gjallarctl thunderbolt status")
	if err := f.Parse(args[1:]); err != nil {
		return 2
	}
	if f.NArg() != 0 || !tbUIDPattern.MatchString(*uid) {
		fmt.Fprintln(stderr, "FAIL: --uid UID is required")
		return 2
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 1
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tbAct(ctx, conn, args[0], *uid); err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 1
	}
	return 0
}

// tbAct carries out one decision. boltd asks polkit, so a caller that is not
// root needs the admin password; the review runs this through sudo instead.
func tbAct(ctx context.Context, conn *dbus.Conn, action, uid string) error {
	manager := conn.Object(boltName, boltPath)
	if action == "forget" {
		return manager.CallWithContext(ctx, boltManager+".ForgetDevice", 0, uid).Err
	}
	var path dbus.ObjectPath
	if err := manager.CallWithContext(ctx, boltManager+".DeviceByUid", 0, uid).Store(&path); err != nil {
		return err
	}
	d, err := tbReadDevice(ctx, conn, path)
	if err != nil {
		return err
	}
	if d.Status != "connected" {
		return fmt.Errorf("device %s is %s, not waiting for authorization", uid, d.Status)
	}
	if action == "always-allow" {
		// Stored with the default policy: boltd lets it in on every later
		// connect, as GNOME does when it enrolls a device.
		return manager.CallWithContext(ctx, boltManager+".EnrollDevice", 0, uid, "default", "none").Err
	}
	return conn.Object(boltName, path).CallWithContext(ctx, boltDevice+".Authorize", 0, "none").Err
}

func runThunderboltStatus(stdout, stderr io.Writer) int {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 1
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Asking would start boltd; it is only running when there is a
	// Thunderbolt controller.
	if !tbBoltRunning(ctx, conn) {
		fmt.Fprintln(stdout, "boltd is not running: no Thunderbolt controller is active.")
		return 0
	}
	devices, err := tbListDevices(ctx, conn)
	if err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 1
	}
	domains := tbListDomains(ctx, conn)
	if len(domains) > 0 {
		dma := oddcDMAFlag(ctx)
		for _, d := range domains {
			fmt.Fprintf(stdout, "domain %s: security %s, DMA protection %s (kernel %s, ODDC %s)\n",
				d.id, d.security, tbDMAState(d.iommu, dma), onOff(d.iommu), dma)
			if warn := tbDMAContradiction(d.iommu, dma, d.id); warn != "" {
				fmt.Fprintln(stderr, warn)
			}
		}
	}
	if len(devices) == 0 {
		fmt.Fprintln(stdout, "no devices")
	}
	for _, d := range devices {
		remembered := ""
		if d.Stored {
			remembered = ", always allowed"
		}
		fmt.Fprintf(stdout, "%s  %s (%s%s)  %s\n", d.UID, tbDisplayName(d), d.Status, remembered, d.Type)
	}
	return 0
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func tbBoltRunning(ctx context.Context, conn *dbus.Conn) bool {
	var has bool
	err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, boltName).Store(&has)
	return err == nil && has
}

func tbListDevices(ctx context.Context, conn *dbus.Conn) ([]tbDevice, error) {
	var paths []dbus.ObjectPath
	if err := conn.Object(boltName, boltPath).CallWithContext(ctx, boltManager+".ListDevices", 0).Store(&paths); err != nil {
		return nil, err
	}
	var devices []tbDevice
	for _, p := range paths {
		// A device that left between the list and this read is skipped.
		if d, err := tbReadDevice(ctx, conn, p); err == nil {
			devices = append(devices, d)
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].UID < devices[j].UID })
	return devices, nil
}

func tbReadDevice(ctx context.Context, conn *dbus.Conn, path dbus.ObjectPath) (tbDevice, error) {
	var props map[string]dbus.Variant
	if err := conn.Object(boltName, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, boltDevice).Store(&props); err != nil {
		return tbDevice{}, err
	}
	d := tbDevice{Path: path}
	str := func(key string) string {
		s, _ := props[key].Value().(string)
		return s
	}
	d.UID, d.Name, d.Vendor = str("Uid"), str("Name"), str("Vendor")
	d.Type, d.Status, d.Domain = str("Type"), str("Status"), str("Domain")
	d.Stored, _ = props["Stored"].Value().(bool)
	return d, nil
}

type tbDomainInfo struct {
	id, security string
	iommu        bool
}

func tbListDomains(ctx context.Context, conn *dbus.Conn) []tbDomainInfo {
	var paths []dbus.ObjectPath
	if err := conn.Object(boltName, boltPath).CallWithContext(ctx, boltManager+".ListDomains", 0).Store(&paths); err != nil {
		return nil
	}
	var domains []tbDomainInfo
	for _, p := range paths {
		var props map[string]dbus.Variant
		if err := conn.Object(boltName, p).CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, boltDomain).Store(&props); err != nil {
			continue
		}
		d := tbDomainInfo{}
		d.id, _ = props["Uid"].Value().(string)
		d.security, _ = props["SecurityLevel"].Value().(string)
		d.iommu, _ = props["IOMMU"].Value().(bool)
		domains = append(domains, d)
	}
	return domains
}

// tbKernelDMA reports whether the kernel has DMA protection on the domain a
// device hangs off. A domain it cannot find counts as unprotected.
func tbKernelDMA(domains []tbDomainInfo, uid string) bool {
	for _, d := range domains {
		if d.id == uid {
			return d.iommu
		}
	}
	return false
}

// tbDMA is what the card says about DMA protection. Only tbDMAOn is
// protected; tbDMAUnconfirmed is handled like tbDMAOff, but the card does not
// claim a gap nobody has found.
type tbDMA int

const (
	tbDMAUnconfirmed tbDMA = iota
	tbDMAOn
	tbDMAOff
)

func (s tbDMA) String() string {
	switch s {
	case tbDMAOn:
		return "on"
	case tbDMAOff:
		return "off"
	}
	return "not confirmed"
}

// tbDMAState combines the kernel with ODDC's word on the model, and the
// worse one wins: ODDC knows when firmware leaves a gap the kernel cannot
// see. A model ODDC has not vouched for is unconfirmed, never protected.
func tbDMAState(kernel bool, oddc tbFlag) tbDMA {
	switch {
	case !kernel || oddc == tbFlagNo:
		return tbDMAOff
	case oddc == tbFlagYes:
		return tbDMAOn
	}
	return tbDMAUnconfirmed
}

type tbFlag int

const (
	tbFlagUnknown tbFlag = iota
	tbFlagYes
	tbFlagNo
)

func (f tbFlag) String() string {
	switch f {
	case tbFlagYes:
		return "on"
	case tbFlagNo:
		return "off"
	}
	return "unknown"
}

// oddcDMAFlag asks ODDC whether this model's Thunderbolt ports are DMA
// protected. Any failure, and a flag ODDC does not set, is unknown.
func oddcDMAFlag(ctx context.Context) tbFlag {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "oddc", "security", "--json").Output()
	if err != nil {
		return tbFlagUnknown
	}
	return parseODDCDMAFlag(out)
}

func parseODDCDMAFlag(out []byte) tbFlag {
	var report struct {
		Flags map[string]json.RawMessage `json:"flags"`
	}
	if json.Unmarshal(out, &report) != nil {
		return tbFlagUnknown
	}
	var on *bool
	if json.Unmarshal(report.Flags["thunderboltDmaProtected"], &on) != nil || on == nil {
		return tbFlagUnknown
	}
	if *on {
		return tbFlagYes
	}
	return tbFlagNo
}

// tbDMAContradiction is the journal line for ODDC vouching for a domain whose
// kernel protection is off: the card says unprotected, and ODDC's data needs
// a look.
func tbDMAContradiction(kernel bool, oddc tbFlag, domain string) string {
	if oddc != tbFlagYes || kernel {
		return ""
	}
	return fmt.Sprintf("WARN: ODDC says Thunderbolt DMA is protected, but the kernel has it off on domain %s; treating it as unprotected. Please report this to ODDC.", domain)
}

// tbWaiting reports whether boltd holds the device back for a decision.
// Hosts are the machine's own controllers and are always let in.
func tbWaiting(d tbDevice) bool {
	return d.Type == "peripheral" && d.Status == "connected"
}

// tbAnswerEnded reports the device whose earlier answer no longer holds
// after sig: boltd dropped it, or its status moved (let in, unplugged,
// plugged in again). Signals arrive in order, so a quick unplug and replug
// between two scans still ends the answer.
func tbAnswerEnded(sig *dbus.Signal) (dbus.ObjectPath, bool) {
	switch sig.Name {
	case boltManager + ".DeviceRemoved":
		if len(sig.Body) == 1 {
			path, ok := sig.Body[0].(dbus.ObjectPath)
			return path, ok
		}
	case "org.freedesktop.DBus.Properties.PropertiesChanged":
		if len(sig.Body) < 2 || sig.Body[0] != boltDevice {
			return "", false
		}
		if changed, ok := sig.Body[1].(map[string]dbus.Variant); ok {
			if _, moved := changed["Status"]; moved {
				return sig.Path, true
			}
		}
	}
	return "", false
}

func tbDisplayName(d tbDevice) string {
	switch {
	case d.Vendor != "" && d.Name != "":
		return d.Vendor + " " + d.Name
	case d.Name != "":
		return d.Name
	}
	return "Unknown device"
}

// tbReviewNotice announces a blocked device. Its strings come from the
// device and are escaped.
func tbReviewNotice(d tbDevice, dma tbDMA) desktopNotice {
	body := html.EscapeString(tbDisplayName(d)) + "\n"
	switch dma {
	case tbDMAOn:
		body += "DMA protection is on."
	case tbDMAOff:
		body += "No DMA protection: it can read memory once let in. Allow only your own devices."
	default:
		body += "DMA protection is not confirmed for this model, so it may read memory once let in. Allow only your own devices."
	}
	return desktopNotice{
		App:     "Thunderbolt",
		Icon:    "thunderbolt",
		Summary: "Thunderbolt device blocked",
		Body:    body,
		Actions: []noticeAction{{"allow-once", "Allow once"}, {"always-allow", "Always allow"}, {"keep", "Keep blocked"}},
	}
}

type tbOutcome struct {
	uid   string
	retry bool
}

// runThunderboltReview asks about every Thunderbolt device boltd holds back.
// It only listens: boltd is never started from here, and nothing is read
// until it runs.
func runThunderboltReview(stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintf(stderr, "FAIL: %v\n", err)
		return 1
	}
	defer conn.Close()
	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	matches := [][]dbus.MatchOption{
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, boltName)},
		{dbus.WithMatchSender(boltName), dbus.WithMatchInterface(boltManager)},
		{dbus.WithMatchSender(boltName), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")},
	}
	for _, m := range matches {
		if err := conn.AddMatchSignal(m...); err != nil {
			fmt.Fprintf(stderr, "FAIL: %v\n", err)
			return 1
		}
	}

	open := map[string]context.CancelFunc{}
	// An answer holds until the device's status moves or it goes away.
	answered := map[string]bool{}
	uidAt := map[dbus.ObjectPath]string{}
	outcomes := make(chan tbOutcome)
	rescan := make(chan struct{}, 1)
	wake := func() {
		select {
		case rescan <- struct{}{}:
		default:
		}
	}
	defer func() {
		for uid, cancel := range open {
			cancel()
			delete(open, uid)
			<-outcomes
		}
	}()

	scan := func() {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if !tbBoltRunning(callCtx, conn) {
			for _, close := range open {
				close()
			}
			clear(answered)
			return
		}
		devices, err := tbListDevices(callCtx, conn)
		if err != nil {
			fmt.Fprintf(stderr, "WARN: Thunderbolt review: %v\n", err)
			return
		}
		domains := tbListDomains(callCtx, conn)
		// ODDC is asked once per scan, and only when a card is due.
		dma, asked := tbFlagUnknown, false
		waiting := map[string]bool{}
		for _, d := range devices {
			uidAt[d.Path] = d.UID
			waiting[d.UID] = tbWaiting(d)
			if !tbWaiting(d) || answered[d.UID] || open[d.UID] != nil {
				continue
			}
			if !asked {
				dma, asked = oddcDMAFlag(callCtx), true
			}
			kernel := tbKernelDMA(domains, d.Domain)
			if warn := tbDMAContradiction(kernel, dma, d.Domain); warn != "" {
				fmt.Fprintln(stderr, warn)
			}
			state := tbDMAState(kernel, dma)
			promptCtx, cancel := context.WithCancel(ctx)
			open[d.UID] = cancel
			go func() {
				outcomes <- tbOutcome{d.UID, tbReviewDevice(promptCtx, executable, d, state)}
			}()
		}
		// A card closes once its device is let in elsewhere or unplugged.
		for uid, close := range open {
			if !waiting[uid] {
				close()
			}
		}
	}

	scan()
	for {
		select {
		case <-ctx.Done():
			return 0
		case sig, ok := <-signals:
			if !ok {
				fmt.Fprintln(stderr, "FAIL: system bus closed")
				return 1
			}
			if path, ended := tbAnswerEnded(sig); ended {
				delete(answered, uidAt[path])
			}
			scan()
		case <-rescan:
			scan()
		case o := <-outcomes:
			if cancel := open[o.uid]; cancel != nil {
				cancel()
				delete(open, o.uid)
			}
			if o.retry {
				time.AfterFunc(2*time.Second, wake)
			} else {
				answered[o.uid] = true
			}
			scan()
		}
	}
}

// tbReviewDevice shows the notice for one device and carries out the answer.
// It reports whether the device should be offered again.
func tbReviewDevice(ctx context.Context, executable string, d tbDevice, dma tbDMA) bool {
	picked, err := showNotice(ctx, tbReviewNotice(d, dma))
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	switch picked {
	case "allow-once", "always-allow":
	default:
		// Dismissed or kept blocked: boltd already holds it back.
		return false
	}
	output, err := usbAuthorize(ctx, tbDisplayName(d), []string{executable, "thunderbolt", picked, "--uid", d.UID})
	if ctx.Err() != nil {
		return false
	}
	if err == nil {
		return false
	}
	_ = exec.Command("zenity", "--error", "--title=Thunderbolt decision failed", "--text="+html.EscapeString(string(output))).Run()
	return !errors.Is(err, context.Canceled)
}

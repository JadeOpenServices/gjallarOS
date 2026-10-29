package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
)

// Check states on the gjallarHorn page. "off" is a protection the user
// turned off in settings: shown in grey, never as a warning.
const (
	checkOK      = "ok"
	checkWeak    = "weak"
	checkBad     = "bad"
	checkOff     = "off"
	checkUnknown = "unknown"
)

// securityCheck is one row of the gjallarHorn page (Plane 216). Fix is the
// button label; `gjallarctl security fix ID` runs it, so the page never
// passes a command of its own.
type securityCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
	// Switch is "on" or "off" for a protection the user may turn off in
	// settings; empty when it cannot be turned off or is not known.
	Switch string `json:"switch,omitempty"`
	// Waiting is a switch change the settings service holds back, such as
	// a security setting asked for away from home.
	Waiting *securityWaiting `json:"waiting,omitempty"`
}

// securityWaiting names the held change by its check: `gjallarctl security
// apply|cancel ID` find it again, so the page passes no change id.
type securityWaiting struct {
	Switch string `json:"switch"` // what the switch is after the change
	Ready  bool   `json:"ready"`
	At     string `json:"at,omitempty"` // when it can be applied, 15:04
}

// securitySwitches are the protections a user may turn off, for example
// to run their own instead, with the settings each one sets.
var securitySwitches = map[string][]string{
	"usb":      {"usbguardEnable", "usbTrustEnforce"},
	"firewall": {"firewallEnable"},
	"scan":     {"virusScanEnable"},
}

func switchState(f securityFacts, id string) string {
	on := true
	for _, key := range securitySwitches[id] {
		switch f.settings[key] {
		case "false":
			on = false
		case "true":
		default:
			return ""
		}
	}
	if on {
		return "on"
	}
	return "off"
}

// waitingChange is the last held change that sets one of the switch's
// keys, with the switch state it leads to.
func waitingChange(f securityFacts, id string) (settings.Pending, string, bool) {
	keys := securitySwitches[id]
	for i := len(f.pending) - 1; i >= 0; i-- {
		change := f.pending[i]
		values := map[string]string{}
		for _, k := range change.Keys {
			values[k.Key] = strings.TrimSpace(string(k.New))
		}
		touched, on := false, true
		for _, key := range keys {
			value, ok := values[key]
			if ok {
				touched = true
			} else {
				value = f.settings[key]
			}
			if value != "true" {
				on = false
			}
		}
		if touched {
			if on {
				return change, "on", true
			}
			return change, "off", true
		}
	}
	return settings.Pending{}, "", false
}

// withWaiting shows a held switch change on its check: the switch shows
// the choice, and the fix gives way to apply or cancel.
func withWaiting(c securityCheck, f securityFacts) securityCheck {
	change, target, ok := waitingChange(f, c.ID)
	if !ok {
		return c
	}
	verb := "Turning it on"
	if target == "off" {
		verb = "Turning it off"
		if c.State != checkOK {
			c.State = checkOff
		}
	}
	w := &securityWaiting{Switch: target, Ready: !change.ReadyAt.After(settingsNow())}
	if w.Ready {
		c.Detail += " " + verb + " is ready to apply."
	} else {
		w.At = change.ReadyAt.Local().Format("15:04")
		c.Detail += " " + verb + " waits until " + w.At + "."
	}
	c.Switch, c.Fix, c.Waiting = target, "", w
	return c
}

// systemProfile changes with every switch to a new system.
var systemProfile = "/nix/var/nix/profiles/system"

// staleSystem is how old the running system may get before the page asks
// for an update.
const staleSystem = 14 * 24 * time.Hour

// securityFacts is what the checks are made of. Every source is one the
// desktop user may read and only root can write, or a root service's
// answer: the page needs no root of its own.
type securityFacts struct {
	luks     luksRecord
	luksErr  error
	diskKind diskcrypto.TPMKind
	// chipPossible: Secure Boot setup is offered for this model.
	chipPossible bool

	secureBoot    bool
	secureBootErr error

	usb    broker.Status
	usbErr error

	firewall string // systemctl is-active firewall.service

	scanSigs    time.Time // when the virus signatures were last written
	scanSigsErr error

	built     flakesource.BuildSource
	builtErr  error
	systemAge time.Duration
	ageErr    error

	// settings holds the keys below as JSON text; missing means unknown.
	settings map[string]string
	// pending are the changes the settings service holds for this session.
	pending []settings.Pending

	findings    []securityFinding
	findingsErr error
}

// securitySettingKeys are the settings the checks read to tell a
// protection the user turned off from one that failed.
var securitySettingKeys = []string{"usbguardEnable", "usbTrustEnforce", "firewallEnable", "virusScanEnable", "secureBootEnable", "developerMode"}

func securityChecks(f securityFacts) []securityCheck {
	checks := []securityCheck{
		diskCheck(f),
		secureBootCheck(f),
		usbCheck(f),
		firewallCheck(f),
		scanCheck(f),
		updatesCheck(f),
		developerCheck(f),
	}
	for i := range checks {
		if _, ok := securitySwitches[checks[i].ID]; ok {
			checks[i].Switch = switchState(f, checks[i].ID)
			checks[i] = withWaiting(checks[i], f)
		}
	}
	return append(checks, hardwareChecks(f)...)
}

func diskCheck(f securityFacts) securityCheck {
	c := securityCheck{ID: "disk", Title: "Disk encryption"}
	switch {
	case errors.Is(f.luksErr, fs.ErrNotExist):
		c.State, c.Detail = checkUnknown, "This system keeps no record of its disk protection."
		return c
	case f.luksErr != nil:
		c.State, c.Detail = checkUnknown, "The disk protection record cannot be read."
		return c
	}
	state := diskProtectionState(f.luks, f.diskKind, func() bool { return f.chipPossible })
	text := diskProtectionTexts(state, f.luks)
	switch state {
	case diskProtectionDone:
		c.State = checkOK
		switch {
		case f.luks.TPM2 > 0 && f.luks.PIN:
			c.Detail = "Encrypted. The security chip unlocks it after your startup PIN."
		case f.luks.TPM2 > 0:
			c.Detail = "Encrypted. The security chip unlocks it."
		default:
			c.Detail = "Encrypted with a passphrase. A recovery key is set up."
		}
	case diskProtectionUnencrypted:
		c.State, c.Detail = checkBad, text.body
	default:
		c.State, c.Detail = checkWeak, text.summary+". "+text.body
		if text.command != nil {
			c.Fix = "Set up"
			if state == diskProtectionPIN {
				c.Fix = "Add PIN"
			}
		}
	}
	return c
}

func secureBootCheck(f securityFacts) securityCheck {
	c := securityCheck{ID: "secure-boot", Title: "Secure Boot"}
	switch {
	case f.secureBootErr != nil:
		c.State, c.Detail = checkUnknown, "The Secure Boot state cannot be read."
	case f.secureBoot:
		c.State, c.Detail = checkOK, "On. The firmware checks the startup files."
	case f.settings["secureBootEnable"] == "false" && !f.chipPossible:
		c.State, c.Detail = checkOff, "Off. Changed startup files go unnoticed."
	case f.chipPossible:
		c.State, c.Detail, c.Fix = checkWeak, "Off. Changed startup files go unnoticed.", "Turn on"
	default:
		c.State, c.Detail = checkWeak, "Off. GjallarOS has no checked Secure Boot setup for this model yet."
	}
	return c
}

func usbCheck(f securityFacts) securityCheck {
	c := securityCheck{ID: "usb", Title: "USB devices"}
	switch {
	case f.settings["usbguardEnable"] == "false":
		c.State, c.Detail = checkOff, "Off. Every USB device works when plugged in."
	case f.usbErr != nil:
		c.State, c.Detail = checkUnknown, "The USB check service does not answer."
	case !f.usb.Enforcing && f.settings["usbTrustEnforce"] == "false":
		c.State, c.Detail = checkOff, "Blocking is off. New USB devices work without asking."
	case !f.usb.Enforcing:
		c.State, c.Detail, c.Fix = checkWeak, "New USB devices work without asking.", "Turn on"
	default:
		c.State, c.Detail = checkOK, "New USB devices wait for your approval."
		if n := f.usb.TrustedDevices; n == 1 {
			c.Detail += " 1 device is allowed."
		} else if n > 1 {
			c.Detail += fmt.Sprintf(" %d devices are allowed.", n)
		}
	}
	return c
}

func firewallCheck(f securityFacts) securityCheck {
	c := securityCheck{ID: "firewall", Title: "Firewall"}
	if f.settings["firewallEnable"] == "false" && f.firewall != "active" {
		c.State, c.Detail = checkOff, "Off. GjallarOS does not filter connections from the network."
		return c
	}
	if f.firewall == "active" {
		c.State, c.Detail = checkOK, "On. Connections from the network are blocked unless allowed."
		return c
	}
	c.State, c.Detail = checkBad, "The firewall is not running. Restart the computer; if it stays off, report it."
	return c
}

func updatesCheck(f securityFacts) securityCheck {
	c := securityCheck{ID: "updates", Title: "Updates"}
	if f.ageErr != nil {
		c.State, c.Detail = checkUnknown, "When the system was last built is not known."
		return c
	}
	days := int(f.systemAge.Hours() / 24)
	switch days {
	case 0:
		c.Detail = "System built today."
	case 1:
		c.Detail = "System built yesterday."
	default:
		c.Detail = fmt.Sprintf("System built %d days ago.", days)
	}
	c.State = checkOK
	if f.systemAge > staleSystem {
		c.State, c.Fix = checkWeak, "Update"
	}
	if f.builtErr == nil && f.built.ODDC != "" && f.built.ODDCPinned != "" && f.built.ODDC != f.built.ODDCPinned {
		c.State, c.Fix = checkWeak, "Update"
		c.Detail += " The hardware data is not the version this release checked."
	}
	if f.builtErr == nil && strings.HasSuffix(f.built.Revision, "-dirty") {
		c.Detail += " Built with local changes."
	}
	return c
}

func developerCheck(f securityFacts) securityCheck {
	c := securityCheck{ID: "developer", Title: "Developer mode"}
	switch f.settings["developerMode"] {
	case "true":
		c.State, c.Detail, c.Fix = checkWeak, "On. Files from your own folder change the system.", "Turn off"
	case "false":
		c.State, c.Detail = checkOK, "Off. The system is built only from the official release."
	default:
		c.State, c.Detail = checkUnknown, "The settings service does not answer."
	}
	return c
}

// hardwareChecks are the per-model facts of `oddc security --json` and
// GjallarOS's own findings. ODDC answers for the model; what it cannot
// confirm is shown as not confirmed, never as safe (ODDC 97).
func hardwareChecks(f securityFacts) []securityCheck {
	checks := []securityCheck{}
	if f.findingsErr != nil {
		checks = append(checks, securityCheck{ID: "hardware", Title: "This model", State: checkUnknown,
			Detail: "Not confirmed. The hardware facts for this model cannot be read."})
	}
	open := 0
	for _, finding := range f.findings {
		c := securityCheck{ID: "finding:" + finding.ID, Title: finding.Name, State: checkOK, Detail: finding.Summary}
		if finding.Status == "open" {
			c.State = checkWeak
			open++
		}
		checks = append(checks, c)
	}
	if f.findingsErr == nil && open == 0 {
		checks = append([]securityCheck{{ID: "hardware", Title: "This model", State: checkOK,
			Detail: "No open security issue is known for this model."}}, checks...)
	}
	return checks
}

func gatherSecurityFacts(ctx context.Context, stderr io.Writer) securityFacts {
	var f securityFacts
	f.luks, f.luksErr = readLUKSRecord(luksRecordPath)
	f.diskKind = diskcrypto.ModelTPMKind()
	f.chipPossible = chipUnlockPossible()
	f.secureBoot, f.secureBootErr = secureboot.FirmwareSecureBoot()

	usbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	response, err := broker.Call(usbCtx, broker.DefaultSocket, broker.Request{Action: broker.ActionStatus})
	cancel()
	switch {
	case err != nil:
		f.usbErr = err
	case response.Status == nil:
		f.usbErr = fmt.Errorf("the USB check service sent no status")
	default:
		f.usb = *response.Status
	}

	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", "firewall.service").Output()
	f.firewall = strings.TrimSpace(string(out))
	f.scanSigs, f.scanSigsErr = signatureTime(clamavDBDir)

	if data, err := os.ReadFile(buildSourcePath); err != nil {
		f.builtErr = err
	} else {
		f.builtErr = json.Unmarshal(data, &f.built)
	}
	if info, err := os.Lstat(systemProfile); err != nil {
		f.ageErr = err
	} else {
		f.systemAge = time.Since(info.ModTime())
	}

	f.settings = map[string]string{}
	if bus, ok := openSettings(stderr); ok {
		setCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		for _, name := range securitySettingKeys {
			if key, err := settingsKey(setCtx, bus, name); err == nil {
				f.settings[name] = string(key.Value)
			}
		}
		if list, err := settingsPendingList(setCtx, bus); err == nil {
			f.pending = list
		}
		cancel()
		bus.Close()
	}

	gate, err := readUSBKeyboardGate()
	if err != nil {
		fmt.Fprintf(stderr, "usb keyboard gate: %v\n", err)
	}
	f.findings, f.findingsErr = oddcFindings(ctx)
	f.findings = append(gjallarOSFindings(gate), f.findings...)
	return f
}

// runSecurityStatus prints the gjallarHorn checks, as JSON for the page.
func runSecurityStatus(args []string, stdout, stderr io.Writer) int {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 1 || len(args) == 1 && !asJSON {
		fmt.Fprintln(stderr, securityUsage)
		return 2
	}
	checks := securityChecks(gatherSecurityFacts(context.Background(), stderr))
	if asJSON {
		data, err := json.Marshal(map[string]any{"checks": checks})
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	for _, c := range checks {
		fmt.Fprintf(stdout, "%-8s %-16s %s\n", c.State, c.Title, c.Detail)
	}
	return 0
}

// runSecurityFix runs the fix behind a check's button. The disk fix is the
// login reminder's own setup; the others run in a terminal, where the
// password prompt and the output stay readable.
func runSecurityFix(args []string, stdout, stderr io.Writer) int {
	if len(args) == 2 && args[1] == "--here" {
		return securityFixHere(args[0], stdout, stderr)
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, securityUsage)
		return 2
	}
	switch args[0] {
	case "disk":
		return runLUKSRemind([]string{"--open"}, stderr)
	case "secure-boot":
		return runInTerminal("GjallarOS Secure Boot", []string{"secure-boot", "setup"}, stderr)
	case "usb", "updates", "developer":
		return runInTerminal("GjallarOS security", []string{"security", "fix", args[0], "--here"}, stderr)
	}
	fmt.Fprintf(stderr, "ERROR: %q has no fix\n", args[0])
	return 2
}

// runSecuritySwitch turns one protection on or off in settings. Both ways
// go through the settings service, which asks for the password.
func runSecuritySwitch(args []string, stdout, stderr io.Writer) int {
	here := len(args) == 3 && args[2] == "--here"
	if len(args) != 2 && !here || args[1] != "on" && args[1] != "off" {
		fmt.Fprintln(stderr, securityUsage)
		return 2
	}
	keys, ok := securitySwitches[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "ERROR: %q cannot be switched\n", args[0])
		return 2
	}
	if !here {
		return runInTerminal("GjallarOS security", []string{"security", "switch", args[0], args[1], "--here"}, stderr)
	}
	if args[0] == "usb" && args[1] == "off" {
		if code := usbDisarmForSwitch(stderr); code != 0 {
			return code
		}
	}
	values := map[string]any{}
	for _, key := range keys {
		values[key] = args[1] == "on"
	}
	return askSystemSettings(values, stdout, stderr)
}

// runSecurityWaiting applies or drops the change held for a switch. Apply
// runs in a terminal for the password and the rebuild.
func runSecurityWaiting(action string, args []string, stdout, stderr io.Writer) int {
	here := len(args) == 2 && args[1] == "--here"
	if len(args) != 1 && !here {
		fmt.Fprintln(stderr, securityUsage)
		return 2
	}
	if _, ok := securitySwitches[args[0]]; !ok {
		fmt.Fprintf(stderr, "ERROR: %q cannot be switched\n", args[0])
		return 2
	}
	if !here {
		return runInTerminal("GjallarOS security", []string{"security", action, args[0], "--here"}, stderr)
	}
	bus, ok := openSettings(stderr)
	if !ok {
		return 1
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()
	f := securityFacts{settings: map[string]string{}}
	for _, name := range securitySwitches[args[0]] {
		if key, err := settingsKey(ctx, bus, name); err == nil {
			f.settings[name] = string(key.Value)
		}
	}
	list, err := settingsPendingList(ctx, bus)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	f.pending = list
	change, target, ok := waitingChange(f, args[0])
	if !ok {
		fmt.Fprintf(stderr, "ERROR: no change waits for %s\n", args[0])
		return 1
	}
	if action == "apply" {
		return settingsApplyCommand(ctx, bus, []string{change.ID}, stdout, stderr)
	}
	if err := settingsCancel(ctx, bus, []string{change.ID}, stdout); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	// Keeping USB blocking on after a disarm: arm it again now.
	if args[0] == "usb" && target == "off" && switchState(f, "usb") == "on" {
		return usbRearmIfDisarmed(stderr)
	}
	return 0
}

// usbArmed, usbDisarmAsRoot and usbArmAsRoot are replaced in tests.
var (
	usbArmed = func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := broker.Call(ctx, broker.DefaultSocket, broker.Request{Action: broker.ActionStatus})
		if err != nil {
			return false, err
		}
		if response.Status == nil {
			return false, errors.New("the USB check service sent no status")
		}
		return response.Status.Armed, nil
	}
	usbDisarmAsRoot = func() error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		// -k: ask now and leave no timestamp behind.
		cmd := exec.Command("sudo", "-k", self, "usb", "disarm")
		// The broker's JSON reply stays out of the terminal; failures
		// still reach stderr.
		cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
		return cmd.Run()
	}
	usbArmAsRoot = func() error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command("sudo", "-k", self, "usb", "arm")
		cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
		return cmd.Run()
	}
)

// usbRearmIfDisarmed turns blocking back on after a disarm, when settings
// already have it on and only the broker's next start would arm it.
func usbRearmIfDisarmed(stderr io.Writer) int {
	armed, err := usbArmed()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: cannot read the USB blocking state: %v\n", err)
		return 1
	}
	if armed {
		return 0
	}
	fmt.Fprintln(stderr, "Turning USB blocking back on. Enter your password.")
	if err := usbArmAsRoot(); err != nil {
		fmt.Fprintf(stderr, "ERROR: USB blocking stays off until the next restart: %v\n", err)
		return 1
	}
	fmt.Fprintln(stderr, "USB blocking is on. New USB devices wait for your approval.")
	return 0
}

// usbDisarmForSwitch disarms USB blocking before the off switch, which the
// settings service refuses while blocking is armed (loophole 23). Disarming
// still takes the disk recovery key, so the switch is no shortcut around it.
func usbDisarmForSwitch(stderr io.Writer) int {
	armed, err := usbArmed()
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: cannot read the USB blocking state: %v\n", err)
		return 1
	}
	if !armed {
		return 0
	}
	fmt.Fprintln(stderr, "USB blocking is armed. To turn it off, enter your password and then the disk recovery key.")
	if err := usbDisarmAsRoot(); err != nil {
		fmt.Fprintf(stderr, "ERROR: USB blocking stays on: %v\n", err)
		return 1
	}
	return 0
}

func securityFixHere(id string, stdout, stderr io.Writer) int {
	switch id {
	case "usb":
		bus, ok := openSettings(stderr)
		if !ok {
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
		f := securityFacts{settings: map[string]string{}}
		for _, name := range securitySwitches["usb"] {
			if key, err := settingsKey(ctx, bus, name); err == nil {
				f.settings[name] = string(key.Value)
			}
		}
		cancel()
		bus.Close()
		if switchState(f, "usb") == "on" {
			return usbRearmIfDisarmed(stderr)
		}
		return askSystemSettings(map[string]any{"usbguardEnable": true, "usbTrustEnforce": true}, stdout, stderr)
	case "developer":
		return askSystemSettings(map[string]any{"developerMode": false}, stdout, stderr)
	case "updates":
		return run([]string{"rebuild", "--update"}, stdout, stderr)
	}
	fmt.Fprintf(stderr, "ERROR: %q has no fix\n", id)
	return 2
}

const securityUsage = "Usage: gjallarctl security notice|oddc-notice|status [--json]|scan PATH [--json]|fix ID|switch ID on|off|apply ID|cancel ID"

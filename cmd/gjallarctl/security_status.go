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

	built     flakesource.BuildSource
	builtErr  error
	systemAge time.Duration
	ageErr    error

	// settings holds the keys below as JSON text; missing means unknown.
	settings map[string]string

	findings    []securityFinding
	findingsErr error
}

// securitySettingKeys are the settings the checks read to tell a
// protection the user turned off from one that failed.
var securitySettingKeys = []string{"usbguardEnable", "usbTrustEnforce", "secureBootEnable", "developerMode"}

func securityChecks(f securityFacts) []securityCheck {
	checks := []securityCheck{
		diskCheck(f),
		secureBootCheck(f),
		usbCheck(f),
		firewallCheck(f),
		updatesCheck(f),
		developerCheck(f),
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

func securityFixHere(id string, stdout, stderr io.Writer) int {
	switch id {
	case "usb":
		return askSystemSettings(map[string]any{"usbguardEnable": true, "usbTrustEnforce": true}, stdout, stderr)
	case "developer":
		return askSystemSettings(map[string]any{"developerMode": false}, stdout, stderr)
	case "updates":
		return run([]string{"rebuild", "--update"}, stdout, stderr)
	}
	fmt.Fprintf(stderr, "ERROR: %q has no fix\n", id)
	return 2
}

const securityUsage = "Usage: gjallarctl security notice|oddc-notice|status [--json]|fix ID"

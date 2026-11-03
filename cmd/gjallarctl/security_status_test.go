package main

import (
	"bytes"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/flakesource"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
)

func goodSecurityFacts() securityFacts {
	return securityFacts{
		luks:       luksRecord{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true, PIN: true},
		secureBoot: true,
		usb:        broker.Status{Enforcing: true, TrustedDevices: 2},
		firewall:   "active",
		built:      flakesource.BuildSource{Revision: "abc", ODDC: "x", ODDCPinned: "x"},
		systemAge:  time.Hour,
		scanSigs:   time.Now().Add(-time.Hour),
		settings:   map[string]string{"usbguardEnable": "true", "usbTrustEnforce": "true", "firewallEnable": "true", "virusScanEnable": "true", "secureBootEnable": "true", "developerMode": "false"},
	}
}

func checkByID(t *testing.T, checks []securityCheck, id string) securityCheck {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", id, checks)
	return securityCheck{}
}

func TestSecurityChecksAllGood(t *testing.T) {
	checks := securityChecks(goodSecurityFacts())
	for _, c := range checks {
		if c.State != checkOK || c.Fix != "" {
			t.Errorf("%s = %s fix %q, want ok without fix", c.ID, c.State, c.Fix)
		}
	}
	if got := checkByID(t, checks, "usb").Detail; got != "New USB devices wait for your approval. 2 devices are allowed." {
		t.Errorf("usb detail = %q", got)
	}
}

func TestSecurityChecksTurnedOffAreOff(t *testing.T) {
	f := goodSecurityFacts()
	f.usb.Enforcing = false
	f.settings["usbTrustEnforce"] = "false"
	if c := checkByID(t, securityChecks(f), "usb"); c.State != checkOff || c.Fix != "" {
		t.Errorf("usb with enforce off = %s fix %q, want off", c.State, c.Fix)
	}
	f.settings["usbguardEnable"] = "false"
	f.usbErr = errors.New("no socket")
	if c := checkByID(t, securityChecks(f), "usb"); c.State != checkOff {
		t.Errorf("usb with usbguard off = %s, want off", c.State)
	}
	f.secureBoot = false
	f.settings["secureBootEnable"] = "false"
	if c := checkByID(t, securityChecks(f), "secure-boot"); c.State != checkOff {
		t.Errorf("secure boot turned off = %s, want off", c.State)
	}
	f.firewall = "inactive"
	f.settings["firewallEnable"] = "false"
	if c := checkByID(t, securityChecks(f), "firewall"); c.State != checkOff || c.Switch != "off" {
		t.Errorf("firewall turned off = %s switch %q, want off", c.State, c.Switch)
	}
	// Still running until the rebuild: the check says what runs now.
	f.firewall = "active"
	if c := checkByID(t, securityChecks(f), "firewall"); c.State != checkOK {
		t.Errorf("firewall off but still running = %s, want ok", c.State)
	}
}

func TestSecurityChecksFailuresAreNotOff(t *testing.T) {
	f := goodSecurityFacts()
	f.usb.Enforcing = false
	if c := checkByID(t, securityChecks(f), "usb"); c.State != checkWeak || c.Fix == "" {
		t.Errorf("usb not enforcing by default = %s fix %q, want weak with fix", c.State, c.Fix)
	}
	f.usbErr = errors.New("no socket")
	if c := checkByID(t, securityChecks(f), "usb"); c.State != checkUnknown {
		t.Errorf("usb service gone = %s, want unknown", c.State)
	}
	f.firewall = "inactive"
	if c := checkByID(t, securityChecks(f), "firewall"); c.State != checkBad {
		t.Errorf("firewall down = %s, want bad", c.State)
	}
	f.secureBoot = false
	f.chipPossible = true
	if c := checkByID(t, securityChecks(f), "secure-boot"); c.State != checkWeak || c.Fix == "" {
		t.Errorf("secure boot off, setup possible = %s fix %q", c.State, c.Fix)
	}
	f.secureBootErr = errors.New("no efivar")
	if c := checkByID(t, securityChecks(f), "secure-boot"); c.State != checkUnknown {
		t.Errorf("secure boot unreadable = %s, want unknown", c.State)
	}
}

func TestSecurityChecksDisk(t *testing.T) {
	f := goodSecurityFacts()
	f.luksErr = fs.ErrNotExist
	if c := checkByID(t, securityChecks(f), "disk"); c.State != checkUnknown {
		t.Errorf("no record = %s, want unknown", c.State)
	}
	f.luksErr = nil
	f.luks = luksRecord{}
	if c := checkByID(t, securityChecks(f), "disk"); c.State != checkBad {
		t.Errorf("unencrypted = %s, want bad", c.State)
	}
	f.luks = luksRecord{Encrypted: true, TPM2: 1, Recovery: 0}
	if c := checkByID(t, securityChecks(f), "disk"); c.State != checkWeak || c.Fix == "" {
		t.Errorf("no recovery key = %s fix %q, want weak with fix", c.State, c.Fix)
	}
}

func TestSecurityChecksUpdates(t *testing.T) {
	f := goodSecurityFacts()
	f.systemAge = 20 * 24 * time.Hour
	c := checkByID(t, securityChecks(f), "updates")
	if c.State != checkWeak || c.Fix != "Update" || c.Detail != "System built 20 days ago." {
		t.Errorf("old system = %+v", c)
	}
	f = goodSecurityFacts()
	f.built.ODDC = "y"
	if c := checkByID(t, securityChecks(f), "updates"); c.State != checkWeak {
		t.Errorf("ODDC off its pin = %s, want weak", c.State)
	}
	f = goodSecurityFacts()
	f.ageErr = fs.ErrNotExist
	if c := checkByID(t, securityChecks(f), "updates"); c.State != checkUnknown {
		t.Errorf("no profile = %s, want unknown", c.State)
	}
}

func TestSecurityChecksDeveloper(t *testing.T) {
	f := goodSecurityFacts()
	f.settings["developerMode"] = "true"
	if c := checkByID(t, securityChecks(f), "developer"); c.State != checkWeak || c.Fix != "Turn off" {
		t.Errorf("developer on = %+v", c)
	}
	delete(f.settings, "developerMode")
	if c := checkByID(t, securityChecks(f), "developer"); c.State != checkUnknown {
		t.Errorf("no settings answer = %s, want unknown", c.State)
	}
	f.settingsMissing = true
	if c := checkByID(t, securityChecks(f), "developer"); c.State != checkWeak || c.Fix != "" {
		t.Errorf("no settings service = %+v, want weak without a fix", c)
	}
}

func TestSecurityChecksHardware(t *testing.T) {
	f := goodSecurityFacts()
	f.findingsErr = errors.New("oddc missing")
	c := checkByID(t, securityChecks(f), "hardware")
	if c.State != checkUnknown {
		t.Errorf("ODDC unreadable = %s, want unknown (not confirmed)", c.State)
	}
	f = goodSecurityFacts()
	f.findings = []securityFinding{{ID: "SYNTHETIC-1", Name: "Synthetic finding", Status: "open", Summary: "s"}}
	checks := securityChecks(f)
	if c := checkByID(t, checks, "finding:SYNTHETIC-1"); c.State != checkWeak {
		t.Errorf("open finding = %s, want weak", c.State)
	}
	for _, c := range checks {
		if c.ID == "hardware" {
			t.Errorf("all-clear row shown next to an open finding")
		}
	}
}

func TestSecurityFixUnknownID(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runSecurityFix([]string{"rm -rf"}, &out, &errOut); code != 2 {
		t.Errorf("unknown id exit = %d, want 2", code)
	}
	if code := runSecurityFix([]string{"firewall", "--here"}, &out, &errOut); code != 2 {
		t.Errorf("firewall has no fix, exit = %d, want 2", code)
	}
}

func TestSecurityChecksSwitch(t *testing.T) {
	f := goodSecurityFacts()
	if c := checkByID(t, securityChecks(f), "usb"); c.Switch != "on" {
		t.Errorf("usb switch = %q, want on", c.Switch)
	}
	f.settings["usbTrustEnforce"] = "false"
	if c := checkByID(t, securityChecks(f), "usb"); c.Switch != "off" {
		t.Errorf("usb switch = %q, want off", c.Switch)
	}
	delete(f.settings, "usbguardEnable")
	if c := checkByID(t, securityChecks(f), "usb"); c.Switch != "" {
		t.Errorf("usb switch without settings = %q, want none", c.Switch)
	}
	if c := checkByID(t, securityChecks(f), "firewall"); c.Switch != "on" {
		t.Errorf("firewall switch = %q, want on", c.Switch)
	}
	var out, errOut bytes.Buffer
	if code := runSecuritySwitch([]string{"disk", "off"}, &out, &errOut); code != 2 {
		t.Errorf("disk switch exit = %d, want 2", code)
	}
	if code := runSecuritySwitch([]string{"usb", "maybe"}, &out, &errOut); code != 2 {
		t.Errorf("bad state exit = %d, want 2", code)
	}
}

func TestUSBDisarmForSwitch(t *testing.T) {
	armed, armedErr, disarmErr, disarmed := false, error(nil), error(nil), 0
	oldArmed, oldDisarm := usbArmed, usbDisarmAsRoot
	t.Cleanup(func() { usbArmed, usbDisarmAsRoot = oldArmed, oldDisarm })
	usbArmed = func() (bool, error) { return armed, armedErr }
	usbDisarmAsRoot = func() error { disarmed++; return disarmErr }

	var errOut bytes.Buffer
	if code := usbDisarmForSwitch(&errOut); code != 0 || disarmed != 0 {
		t.Errorf("not armed: exit %d, disarm runs %d, want 0 and 0", code, disarmed)
	}
	armed = true
	if code := usbDisarmForSwitch(&errOut); code != 0 || disarmed != 1 {
		t.Errorf("armed: exit %d, disarm runs %d, want 0 and 1", code, disarmed)
	}
	disarmErr = errors.New("wrong key")
	if code := usbDisarmForSwitch(&errOut); code != 1 {
		t.Errorf("failed disarm exit = %d, want 1", code)
	}
	armedErr = errors.New("no socket")
	if code := usbDisarmForSwitch(&errOut); code != 1 || disarmed != 2 {
		t.Errorf("unknown state: exit %d, disarm runs %d, want 1 and 2", code, disarmed)
	}
}

func TestSecurityChecksWaiting(t *testing.T) {
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.Local)
	oldNow := settingsNow
	t.Cleanup(func() { settingsNow = oldNow })
	settingsNow = func() time.Time { return now }

	f := goodSecurityFacts()
	f.usb.Enforcing = false
	f.pending = []settings.Pending{{
		Change:  settings.Change{ID: "synthetic", Keys: []settings.KeyChange{{Key: "usbTrustEnforce", New: []byte("false")}}},
		ReadyAt: now.Add(30 * time.Minute),
	}}
	c := checkByID(t, securityChecks(f), "usb")
	if c.State != checkOff || c.Fix != "" || c.Switch != "off" || c.Waiting == nil || c.Waiting.Ready || c.Waiting.At != "18:30" {
		t.Errorf("held off change = %+v waiting %+v", c, c.Waiting)
	}
	f.pending[0].ReadyAt = now.Add(-time.Minute)
	if c := checkByID(t, securityChecks(f), "usb"); c.Waiting == nil || !c.Waiting.Ready || c.Waiting.At != "" {
		t.Errorf("ready change = %+v", c.Waiting)
	}
	f.settings["usbTrustEnforce"] = "false"
	f.pending[0].Keys[0].New = []byte("true")
	if c := checkByID(t, securityChecks(f), "usb"); c.Switch != "on" || c.State != checkOff || c.Waiting == nil {
		t.Errorf("held on change = %+v", c)
	}
	f.pending[0].Keys[0].Key = "developerMode"
	if c := checkByID(t, securityChecks(f), "usb"); c.Waiting != nil {
		t.Errorf("unrelated change shown on usb: %+v", c.Waiting)
	}
	var out, errOut bytes.Buffer
	if code := runSecurityWaiting("apply", []string{"disk"}, &out, &errOut); code != 2 {
		t.Errorf("disk apply exit = %d, want 2", code)
	}
	if code := runSecurityWaiting("cancel", nil, &out, &errOut); code != 2 {
		t.Errorf("no id exit = %d, want 2", code)
	}
}

func TestUSBRearm(t *testing.T) {
	armed, armedErr, armErr, arms := true, error(nil), error(nil), 0
	oldArmed, oldArm := usbArmed, usbArmAsRoot
	t.Cleanup(func() { usbArmed, usbArmAsRoot = oldArmed, oldArm })
	usbArmed = func() (bool, error) { return armed, armedErr }
	usbArmAsRoot = func() error { arms++; return armErr }

	var errOut bytes.Buffer
	if code := usbRearmIfDisarmed(&errOut); code != 0 || arms != 0 {
		t.Errorf("armed: exit %d, arm runs %d, want 0 and 0", code, arms)
	}
	armed = false
	if code := usbRearmIfDisarmed(&errOut); code != 0 || arms != 1 {
		t.Errorf("disarmed: exit %d, arm runs %d, want 0 and 1", code, arms)
	}
	armErr = errors.New("wrong password")
	if code := usbRearmIfDisarmed(&errOut); code != 1 {
		t.Errorf("failed arm exit = %d, want 1", code)
	}
	armedErr = errors.New("no socket")
	if code := usbRearmIfDisarmed(&errOut); code != 1 || arms != 2 {
		t.Errorf("unknown state: exit %d, arm runs %d, want 1 and 2", code, arms)
	}
}

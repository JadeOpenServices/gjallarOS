// Package installreport builds the install report a user may save after a
// failed install or on a machine no ODDC model matches (Plane 292). The
// report is a fixed list of fields: machine and firmware facts that hold
// nothing personal, plus the installer's own error text after redaction.
// Nothing is sent anywhere; the user reads the exact text, then decides
// whether to save it to a file.
package installreport

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	portable "github.com/JadeOpenServices/oddc/pkg/oddc"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
)

// Contact is where a saved report goes. Shown as text only.
const Contact = "report@openjade.de"

// Format is the report layout version.
const Format = 1

// Reason says why the report was offered.
type Reason string

const (
	ReasonFailed  Reason = "the install failed"
	ReasonUnknown Reason = "no ODDC model matches this machine"
)

// Firmware is the boot firmware state. Every field is a plain on/off or
// version fact.
type Firmware struct {
	UEFI       bool
	SecureBoot string // "on", "off", "missing" or "" when not read
	SetupMode  string
	TPM        int // TPM major version, 0 when none
}

// Report holds the allowlisted fields. Error is redacted when the report
// is built; nothing else here carries free text a user typed.
type Report struct {
	Reason    Reason
	Date      time.Time
	Commit    string
	Channel   string
	Kernel    string
	Firmware  Firmware
	ODDCModel string
	// ODDCChecked is false when the install stopped before the ODDC
	// lookup; ODDCModel "" then says nothing about the machine.
	ODDCChecked bool
	Machine     portable.MachineIdentity
	Devices     map[string][]string
	Error       string
}

const (
	maxField   = 96
	maxError   = 16 << 10
	errorHead  = 4 << 10
	maxDevices = 512
	// MaxSize caps the whole report.
	MaxSize = 64 << 10
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	idPattern     = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z:._-]{0,31}$`)
	busPattern    = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	modelPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._/-]{0,95}$`)
)

// Render returns the report text exactly as it is shown and saved.
func (r Report) Render() []byte {
	var b strings.Builder
	line := func(label, value string) {
		fmt.Fprintf(&b, "%-18s %s\n", label+":", value)
	}

	fmt.Fprintf(&b, "gjallarOS install report (format %d)\n\n", Format)
	line("Why", string(r.Reason))
	line("Date (UTC)", r.Date.UTC().Format("2006-01-02"))
	line("gjallarOS commit", only(r.Commit, commitPattern))
	line("Release channel", field(r.Channel))
	line("Live kernel", field(r.Kernel))
	line("Firmware", r.Firmware.text())
	if r.ODDCChecked {
		line("ODDC model", orNone(only(r.ODDCModel, modelPattern)))
	} else {
		line("ODDC model", "not checked (the install stopped before the ODDC step)")
	}

	b.WriteString("\nMachine\n")
	for _, f := range []struct{ label, value string }{
		{"Form factor", r.Machine.FormFactor},
		{"Vendor", r.Machine.SysVendor},
		{"Product", r.Machine.ProductName},
		{"Product version", r.Machine.ProductVersion},
		{"Board vendor", r.Machine.BoardVendor},
		{"Board", r.Machine.BoardName},
		{"Board version", r.Machine.BoardVersion},
	} {
		line("  "+f.label, orNone(field(f.value)))
	}

	b.WriteString("\nDevices (bus, vendor:device ID)\n")
	devices := r.devices()
	if len(devices) == 0 {
		b.WriteString("  none read\n")
	}
	for _, d := range devices {
		fmt.Fprintf(&b, "  %s\n", d)
	}

	if r.Reason == ReasonFailed {
		b.WriteString("\nError\n")
		text := strings.TrimSpace(r.Error)
		if text == "" {
			text = "none recorded"
		}
		for _, l := range strings.Split(text, "\n") {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}

	b.WriteString("\nThe installer leaves out passwords, PINs, keys, serial numbers, network\n" +
		"names, MAC and IP addresses, user names and host names. In the error\n" +
		"text, values that look like one of those are replaced with <...>; this\n" +
		"can miss one, so read the report before you send it.\n")

	out := []byte(b.String())
	if len(out) > MaxSize {
		out = append(out[:MaxSize-len(cutNote)], cutNote...)
	}
	return out
}

const cutNote = "\n[report cut at its size limit]\n"

func (f Firmware) text() string {
	if !f.UEFI {
		return "legacy BIOS"
	}
	parts := []string{"UEFI"}
	if f.SecureBoot == string(secureboot.FlagMissing) {
		// The firmware has no Secure Boot variable at all, as firmware
		// built without Secure Boot. Setup Mode is then missing too.
		parts = append(parts, "no Secure Boot in the firmware")
	} else {
		parts = append(parts, "Secure Boot "+orUnknown(f.SecureBoot))
		parts = append(parts, "Setup Mode "+orUnknown(f.SetupMode))
	}
	if f.TPM > 0 {
		parts = append(parts, fmt.Sprintf("TPM %d", f.TPM))
	} else {
		parts = append(parts, "no TPM")
	}
	return strings.Join(parts, ", ")
}

// devices lists bus and ID pairs, sorted, IDs that are not plain device IDs
// left out.
func (r Report) devices() []string {
	var list []string
	for bus, ids := range r.Devices {
		if !busPattern.MatchString(bus) {
			continue
		}
		for _, id := range ids {
			if idPattern.MatchString(id) {
				list = append(list, bus+" "+id)
			}
		}
	}
	sort.Strings(list)
	list = compact(list)
	if len(list) > maxDevices {
		list = list[:maxDevices]
	}
	return list
}

func compact(list []string) []string {
	out := list[:0]
	for i, v := range list {
		if i == 0 || v != list[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// field makes a sysfs or catalog value safe to show: printable, one line,
// capped.
func field(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) && r != ' ' {
			return -1
		}
		return r
	}, value))
	if len(value) > maxField {
		value = value[:maxField]
	}
	return value
}

func only(value string, pattern *regexp.Regexp) string {
	if !pattern.MatchString(value) {
		return ""
	}
	return value
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// cutError keeps the start and the end of a long error, where the cause
// and the last command output usually are.
func cutError(text string) string {
	if len(text) <= maxError {
		return text
	}
	tail := maxError - errorHead
	return text[:errorHead] +
		fmt.Sprintf("\n[... %d bytes cut ...]\n", len(text)-maxError) +
		text[len(text)-tail:]
}

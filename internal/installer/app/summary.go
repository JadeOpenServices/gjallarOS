package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

// installTarget says where the answers go, for the summary.
type installTarget struct {
	// disk is the disk a fresh install erases; "" for the running system.
	disk          string
	redeploy      bool
	encryptedRoot bool
	writeOnly     bool // --no-rebuild
}

// confirmInstall shows every answer once and asks one question (Q-O). It
// replaces the write and the rebuild questions: yes writes the answers and
// installs, no leaves the machine as it was. With --no-rebuild it only
// writes them.
func confirmInstall(ctx context.Context, ui prompt.UI, u config.User, t installTarget) (bool, error) {
	writeSummary(ui.Out, u, t)
	question := "Install now?"
	if t.writeOnly {
		question = "Write these answers now? Nothing is installed until the next rebuild."
	}
	return ui.Confirm(ctx, question, false)
}

func writeSummary(out io.Writer, u config.User, t installTarget) {
	keyboard := u.KeyboardLayout
	if u.KeyboardVariant != "" {
		keyboard += " (" + u.KeyboardVariant + ")"
	}
	rows := [][2]string{
		{"Region", fmt.Sprintf("%s, %s, keyboard %s", u.Timezone, u.Locale, keyboard)},
		{"Account", fmt.Sprintf("%s on %s, shell %s", u.Username, u.Hostname, u.Shell)},
		{"Management", summaryManagement(u)},
		{"Printing", summaryPrinting(u)},
		{"Secure Boot", summarySecureBoot(u)},
		{"Disk unlock", summaryUnlock(u, t.encryptedRoot)},
		{"Recovery", summaryRecovery(u)},
		{"USB devices", summaryUSB(u)},
		{"Disk", summaryDisk(t)},
	}
	if u.ReleaseChannel != "" {
		rows = append(rows, [2]string{"Updates", u.ReleaseChannel + " channel"})
	}
	fmt.Fprintln(out, "\nSummary")
	for _, row := range rows {
		fmt.Fprintf(out, "  %-12s %s\n", row[0]+":", row[1])
	}
}

func summaryManagement(u config.User) string {
	if !u.Management.Enable {
		return "not managed"
	}
	return "JODS at " + u.Management.Endpoint
}

func summaryPrinting(u config.User) string {
	switch {
	case !u.PrintingEnable:
		return "off"
	case u.NetworkPrintingEnable:
		return "this computer and printers on the local network"
	default:
		return "printers and scanners plugged into this computer"
	}
}

func summarySecureBoot(u config.User) string {
	switch {
	case !u.SecureBootEnable:
		return "off"
	case u.SecureBootShim:
		return "on, through shim"
	default:
		return "on, gjallarOS keys"
	}
}

func summaryUnlock(u config.User, encrypted bool) string {
	switch {
	case !encrypted:
		return "the root is not encrypted"
	case u.LUKSTPM2Enable && u.LUKSTPM2PIN:
		return "TPM2 and a PIN"
	case u.LUKSTPM2Enable:
		return "TPM2"
	default:
		return "disk passphrase"
	}
}

func summaryRecovery(u config.User) string {
	switch {
	case !u.RecoveryEnable:
		return strings.ToLower(recoveryNone)
	case u.RecoveryPartitionEnable:
		return "boot entry and recovery partition"
	default:
		return "boot entry only"
	}
}

func summaryUSB(u config.User) string {
	var s string
	switch {
	case !u.USBGuardEnable:
		return "off"
	case u.USBTrustEnforce:
		s = "review and block unknown devices"
	default:
		s = "review new devices"
	}
	if u.USBTrustLapseDays > 0 {
		s += fmt.Sprintf(", approvals expire after %d days without use", u.USBTrustLapseDays)
	}
	return s
}

func summaryDisk(t installTarget) string {
	switch {
	case t.disk != "":
		return t.disk + ", erased"
	case t.redeploy:
		return "this system, clean redeploy; your files stay"
	default:
		return "this system; your files stay"
	}
}

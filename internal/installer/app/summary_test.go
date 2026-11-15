package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func summaryUser() config.User {
	return config.User{
		Hostname: "gjallarOS", Username: "alex", Shell: "zsh",
		Timezone: "Etc/UTC", Locale: "en_US.UTF-8", KeyboardLayout: "us", KeyboardVariant: "intl",
		PrintingEnable:   true,
		SecureBootEnable: true, LUKSTPM2Enable: true,
		RecoveryEnable: true, RecoveryPartitionEnable: true,
		USBGuardEnable: true, USBTrustLapseDays: DefaultUSBTrustLapseDays,
		ReleaseChannel: "stable",
	}
}

// Q-O: every answer once, then one question that writes and installs.
func TestConfirmInstallShowsEveryAnswerOnce(t *testing.T) {
	var output bytes.Buffer
	ok, err := confirmInstall(context.Background(), prompt.New(strings.NewReader("y\n"), &output), summaryUser(),
		installTarget{disk: "/dev/vda", encryptedRoot: true})
	if err != nil || !ok {
		t.Fatalf("ok=%t err=%v", ok, err)
	}
	for _, want := range []string{
		"Region:      Etc/UTC, en_US.UTF-8, keyboard us (intl)",
		"Account:     alex on gjallarOS",
		"Management:  not managed",
		"Printing:    printers and scanners plugged into this computer",
		"Secure Boot: on, gjallarOS keys",
		"Disk unlock: TPM2",
		"Recovery:    boot entry and recovery partition",
		"USB devices: review new devices, approvals expire after 30 days without use",
		"Disk:        /dev/vda, erased",
		"Updates:     stable channel",
		"Install now?",
	} {
		if strings.Count(output.String(), want) != 1 {
			t.Fatalf("want %q once in:\n%s", want, output.String())
		}
	}
}

func TestConfirmInstallEnterAnswersNo(t *testing.T) {
	var output bytes.Buffer
	ok, err := confirmInstall(context.Background(), prompt.New(strings.NewReader("\n"), &output), summaryUser(), installTarget{encryptedRoot: true})
	if err != nil || ok {
		t.Fatalf("Enter must mean no: ok=%t err=%v", ok, err)
	}
	if !strings.Contains(output.String(), "this system; your files stay") {
		t.Fatalf("output:\n%s", output.String())
	}
}

func TestConfirmInstallNoRebuildOnlyWrites(t *testing.T) {
	var output bytes.Buffer
	u := summaryUser()
	u.SecureBootEnable, u.LUKSTPM2Enable, u.RecoveryEnable, u.USBGuardEnable = false, false, false, false
	if _, err := confirmInstall(context.Background(), prompt.New(strings.NewReader("y\n"), &output), u, installTarget{writeOnly: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Write these answers now? Nothing is installed", "Secure Boot: off", "Disk unlock: the root is not encrypted", "Recovery:    none", "USB devices: off"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("want %q in:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "Install now?") {
		t.Fatalf("--no-rebuild asked to install:\n%s", output.String())
	}
}

package secureboot

import "testing"

func TestCompletedSecureBootDoesNotRebootFirmware(t *testing.T) {
	if NeedsFirmwareReboot(ContinuationNone) {
		t.Fatal("completed Secure Boot transaction requested a firmware reboot")
	}
	if !NeedsFirmwareReboot(ContinuationEnroll) || !NeedsFirmwareReboot(ContinuationEnable) {
		t.Fatal("incomplete Secure Boot transaction did not request its firmware handoff")
	}
}

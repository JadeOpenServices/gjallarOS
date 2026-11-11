package diskcrypto

import "testing"

func TestDefaultStartupUnlock(t *testing.T) {
	cases := []struct {
		kind      TPMKind
		pinBefore bool
		want      string
	}{
		{TPMFirmware, false, StartupUnlockAutomatic},
		{TPMFirmware, true, StartupUnlockPIN},
		{TPMSeparate, false, StartupUnlockPIN},
		{TPMUnknown, false, StartupUnlockPIN},
	}
	for _, c := range cases {
		if got := DefaultStartupUnlock(c.kind, c.pinBefore); got != c.want {
			t.Errorf("%v pinBefore=%v: %q, want %q", c.kind, c.pinBefore, got, c.want)
		}
	}
	if choices := StartupUnlockChoices(); len(choices) != 2 || choices[0] != StartupUnlockAutomatic {
		t.Errorf("choices %q", choices)
	}
}

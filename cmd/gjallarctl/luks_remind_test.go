package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

func TestLUKSRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyslots")
	for _, want := range []luksRecord{
		{},
		{Encrypted: true, TPM2: 1, Recovery: 1},
		{Encrypted: true, Passphrase: 2},
		{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true},
		{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true, PIN: true},
	} {
		if err := writeLUKSRecord(path, want); err != nil {
			t.Fatal(err)
		}
		got, err := readLUKSRecord(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("read %+v, wrote %+v", got, want)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, want 0644", info.Mode().Perm())
	}
}

func TestReadLUKSRecordRejectsBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"missing key": "encrypted=yes\ntpm2=1\nrecovery=1\n",
		"negative":    "encrypted=yes\ntpm2=-1\nrecovery=1\npassphrase=0\n",
		"not a count": "encrypted=yes\ntpm2=one\nrecovery=1\npassphrase=0\n",
		"bad pin":     "encrypted=yes\ntpm2=1\nrecovery=1\npassphrase=0\npin=2\n",
	} {
		path := filepath.Join(dir, "keyslots")
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readLUKSRecord(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestDiskProtectionState(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	asked := func(t *testing.T) func() bool {
		return func() bool { t.Fatal("chip check asked although the chip unlocks"); return false }
	}
	cases := []struct {
		name   string
		record luksRecord
		chip   func() bool
		want   string
	}{
		{"unencrypted", luksRecord{}, no, diskProtectionUnencrypted},
		{"chip, key, no passphrase", luksRecord{Encrypted: true, TPM2: 1, Recovery: 1}, asked(t), diskProtectionDone},
		{"chip, passphrase left", luksRecord{Encrypted: true, TPM2: 1, Recovery: 1, Passphrase: 1}, asked(t), diskProtectionSetup},
		{"chip, no key", luksRecord{Encrypted: true, TPM2: 1}, asked(t), diskProtectionSetup},
		{"Secure Boot off on a known model", luksRecord{Encrypted: true, Recovery: 1, Passphrase: 1}, yes, diskProtectionSecureBoot},
		{"generic model with key", luksRecord{Encrypted: true, Recovery: 1, Passphrase: 1}, no, diskProtectionDone},
		{"generic model without key", luksRecord{Encrypted: true, Passphrase: 1}, no, diskProtectionRecoveryKey},
	}
	for _, c := range cases {
		if got := diskProtectionState(c.record, diskcrypto.TPMFirmware, c.chip); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDiskProtectionStateSuggestsPIN(t *testing.T) {
	chip := func() bool { t.Fatal("chip check asked although the chip unlocks"); return false }
	noPIN := luksRecord{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true}
	withPIN := luksRecord{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true, PIN: true}
	oldRecord := luksRecord{Encrypted: true, TPM2: 1, Recovery: 1}
	cases := []struct {
		name   string
		record luksRecord
		kind   diskcrypto.TPMKind
		want   string
	}{
		{"separate chip without PIN", noPIN, diskcrypto.TPMSeparate, diskProtectionPIN},
		{"unconfirmed chip without PIN", noPIN, diskcrypto.TPMUnknown, diskProtectionPIN},
		{"firmware TPM without PIN", noPIN, diskcrypto.TPMFirmware, diskProtectionDone},
		{"separate chip with PIN", withPIN, diskcrypto.TPMSeparate, diskProtectionDone},
		{"record without pin line", oldRecord, diskcrypto.TPMUnknown, diskProtectionDone},
		{"setup gap comes first", luksRecord{Encrypted: true, TPM2: 1, PINKnown: true}, diskcrypto.TPMUnknown, diskProtectionSetup},
	}
	for _, c := range cases {
		if got := diskProtectionState(c.record, c.kind, chip); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDiskProtectionTextsNameACommandWhereSetupExists(t *testing.T) {
	r := luksRecord{Encrypted: true, TPM2: 1, Passphrase: 1}
	for _, state := range []string{diskProtectionSetup, diskProtectionSecureBoot, diskProtectionRecoveryKey, diskProtectionPIN} {
		text := diskProtectionTexts(state, r)
		if text.summary == "" || text.body == "" || text.risk == "" || text.command == nil {
			t.Errorf("%s: incomplete %+v", state, text)
		}
	}
	if text := diskProtectionTexts(diskProtectionUnencrypted, luksRecord{}); text.command != nil || text.risk == "" {
		t.Errorf("unencrypted: %+v", text)
	}
}

// The notice card shows three lines of about 30 characters; longer bodies
// end in "…" and hide what setup does.
func TestDiskProtectionBodiesFitTheCard(t *testing.T) {
	records := []luksRecord{
		{Encrypted: true, TPM2: 1, Passphrase: 1},
		{Encrypted: true, TPM2: 1},
		{Encrypted: true, TPM2: 1, Recovery: 1, Passphrase: 1},
	}
	states := []string{diskProtectionSecureBoot, diskProtectionRecoveryKey, diskProtectionUnencrypted, diskProtectionPIN}
	for _, r := range records {
		if body := diskProtectionTexts(diskProtectionSetup, r).body; len(body) > 80 {
			t.Errorf("setup %+v: %d characters", r, len(body))
		}
	}
	for _, state := range states {
		if body := diskProtectionTexts(state, luksRecord{}).body; len(body) > 80 {
			t.Errorf("%s: %d characters", state, len(body))
		}
	}
}

func TestPrintLUKSStatusTPMHints(t *testing.T) {
	cases := []struct {
		name          string
		slots         diskcrypto.Keyslots
		kind          diskcrypto.TPMKind
		pinHint, srk  bool
		chipTypeWords string
	}{
		{"unconfirmed chip, no PIN", diskcrypto.Keyslots{TPM2: []int{0}, Recovery: []int{1}}, diskcrypto.TPMUnknown, true, false, "not confirmed"},
		{"separate chip with PIN", diskcrypto.Keyslots{TPM2: []int{0}, TPM2PIN: true, Recovery: []int{1}}, diskcrypto.TPMSeparate, false, false, "separate chip"},
		{"firmware TPM, no PIN", diskcrypto.Keyslots{TPM2: []int{0}, Recovery: []int{1}}, diskcrypto.TPMFirmware, false, false, "inside the processor"},
		{"old token", diskcrypto.Keyslots{TPM2: []int{0}, TPM2PIN: true, TPM2NoSRK: true, Recovery: []int{1}}, diskcrypto.TPMFirmware, false, true, "inside the processor"},
	}
	for _, c := range cases {
		var out strings.Builder
		printLUKSStatus(&out, "/dev/vda2", c.slots, c.kind)
		got := out.String()
		if !strings.Contains(got, "Chip type:     ") || !strings.Contains(got, c.chipTypeWords) {
			t.Errorf("%s: chip type missing:\n%s", c.name, got)
		}
		if strings.Contains(got, "gjallarctl luks pin") != c.pinHint {
			t.Errorf("%s: PIN hint %t, want %t:\n%s", c.name, !c.pinHint, c.pinHint, got)
		}
		if strings.Contains(got, "gjallarctl tpm2 reenroll") != c.srk {
			t.Errorf("%s: reenroll hint %t, want %t:\n%s", c.name, !c.srk, c.srk, got)
		}
	}
}

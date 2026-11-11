package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto"
)

func TestLUKSRecordBootFactsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyslots")
	for _, want := range []luksRecord{
		{Encrypted: true, TPM2: 1, Recovery: 1, ChipUnlockKnown: true, ChipUnlock: true},
		{Encrypted: true, TPM2: 1, Recovery: 1, ChipUnlockKnown: true},
		{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true, ChipUnlockKnown: true, PCR4Known: true},
		{Encrypted: true, TPM2: 1, Recovery: 1, PCR4Known: true, PCR4Locked: true},
		{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true, BoundThisBoot: true},
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
}

func TestReadLUKSRecordRejectsBrokenBootFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyslots")
	for name, data := range map[string]string{
		"chipunlock word": "encrypted=yes\ntpm2=1\nrecovery=1\npassphrase=0\nchipunlock=yes\n",
		"chipunlock 2":    "encrypted=yes\ntpm2=1\nrecovery=1\npassphrase=0\nchipunlock=2\n",
		"pcr4 negative":   "encrypted=yes\ntpm2=1\nrecovery=1\npassphrase=0\npcr4=-1\n",
		"bound 2":         "encrypted=yes\ntpm2=1\nrecovery=1\npassphrase=0\nbound=2\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readLUKSRecord(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestBootFactStates(t *testing.T) {
	chip := func() chipPath { t.Fatal("chip check asked although the chip unlocks"); return chipNone }
	base := luksRecord{Encrypted: true, TPM2: 1, Recovery: 1}
	typed := base
	typed.ChipUnlockKnown = true
	chipOpened := typed
	chipOpened.ChipUnlock = true
	fallback := typed
	fallback.PCR4Known = true
	locked := fallback
	locked.PCR4Locked = true
	gap := typed
	gap.Recovery = 0
	cases := []struct {
		name   string
		record luksRecord
		want   string
	}{
		{"no boot facts", base, diskProtectionDone},
		{"chip opened the disk", chipOpened, diskProtectionDone},
		{"recovery key, PCR 4 state unknown", typed, diskProtectionReenroll},
		{"recovery key, PCR 4 locked", locked, diskProtectionReenroll},
		{"recovery key after firmware fallback", fallback, diskProtectionRestart},
		{"setup gap comes first", gap, diskProtectionSetup},
	}
	for _, c := range cases {
		if got := diskProtectionState(c.record, diskcrypto.TPMFirmware, chip); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestBootFactTexts(t *testing.T) {
	re := diskProtectionTexts(diskProtectionReenroll, luksRecord{})
	if re.summary == "" || len(re.body) > 80 || len(re.command) != 2 || re.command[0] != "tpm2" || re.command[1] != "reenroll" {
		t.Errorf("reenroll: %+v", re)
	}
	rp := diskProtectionTexts(diskProtectionReenroll, luksRecord{PINKnown: true, PIN: true})
	if rp.body == re.body || len(rp.body) > 80 || !strings.Contains(rp.body, "PIN") {
		t.Errorf("reenroll with PIN: %+v", rp)
	}
	rs := diskProtectionTexts(diskProtectionRestart, luksRecord{})
	if rs.summary == "" || rs.body == "" || len(rs.body) > 80 || rs.command != nil {
		t.Errorf("restart: %+v", rs)
	}
}

func TestWithBootFacts(t *testing.T) {
	old := luksRecord{Encrypted: true, TPM2: 1, Recovery: 1, ChipUnlockKnown: true, PCR4Known: true, PCR4Locked: true}
	fresh := luksRecord{Encrypted: true, TPM2: 1, Recovery: 1, PINKnown: true}
	kept := withBootFacts(fresh, old, true)
	if !kept.ChipUnlockKnown || kept.ChipUnlock || !kept.PCR4Known || !kept.PCR4Locked || !kept.PINKnown {
		t.Errorf("same chip keyslot: %+v", kept)
	}
	bound := fresh
	bound.BoundThisBoot = true
	if dropped := withBootFacts(fresh, old, false); dropped != bound {
		t.Errorf("new chip keyslot: %+v, want %+v", dropped, bound)
	}
	// Once bound, a later keyslot change this boot keeps the mark.
	if again := withBootFacts(fresh, bound, true); again != bound {
		t.Errorf("bound record lost the mark: %+v", again)
	}
	if got := diskProtectionState(bound, diskcrypto.TPMFirmware, func() chipPath { return chipNone }); got != diskProtectionDone {
		t.Errorf("bound record: %s, want done", got)
	}
}

func TestSameChipKeyslots(t *testing.T) {
	a := diskcrypto.Keyslots{TPM2: []int{0}, TPM2Token: map[int]string{0: "0"}, Recovery: []int{1}}
	b := diskcrypto.Keyslots{TPM2: []int{0}, TPM2Token: map[int]string{0: "0"}, Recovery: []int{2}}
	if !sameChipKeyslots(a, b) {
		t.Error("recovery key change counted as chip change")
	}
	c := b
	c.TPM2PIN = true
	if sameChipKeyslots(a, c) {
		t.Error("PIN change not counted")
	}
	d := b
	d.TPM2 = []int{3}
	if sameChipKeyslots(a, d) {
		t.Error("new chip keyslot not counted")
	}
}

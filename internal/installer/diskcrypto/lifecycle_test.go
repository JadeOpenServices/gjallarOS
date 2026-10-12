package diskcrypto

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Headers from cryptsetup luksDump --dump-json-metadata on the e2e-fw13
// VM: before and after systemd-cryptenroll --recovery-key, then a fresh
// install with a startup PIN and the same disk after gjallarctl luks pin
// --remove.
func TestParseKeyslots(t *testing.T) {
	for _, tc := range []struct {
		file string
		want Keyslots
	}{
		{"testdata/luks-tpm2-passphrase.json", Keyslots{All: []int{0, 2}, TPM2: []int{2}, TPM2Token: map[int]string{2: "1"}, Passphrase: []int{0}}},
		{"testdata/luks-tpm2-recovery-passphrase.json", Keyslots{All: []int{0, 1, 2}, TPM2: []int{2}, TPM2Token: map[int]string{2: "1"}, Recovery: []int{1}, Passphrase: []int{0}}},
		{"testdata/luks-tpm2pin-recovery.json", Keyslots{All: []int{0, 2}, TPM2: []int{0}, TPM2PIN: true, TPM2Token: map[int]string{0: "0"}, Recovery: []int{2}}},
		{"testdata/luks-tpm2-recovery.json", Keyslots{All: []int{0, 2}, TPM2: []int{0}, TPM2Token: map[int]string{0: "0"}, Recovery: []int{2}}},
	} {
		raw, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseKeyslots(raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.file, got, tc.want)
		}
	}
}

func TestParseKeyslotsRejectsGarbage(t *testing.T) {
	if _, err := ParseKeyslots([]byte("not json")); err == nil {
		t.Fatal("garbage parsed")
	}
}

// A key systemd-cryptenroll printed on the e2e-fw13 VM; its keyslot has
// since been wiped.
const realRecoveryKey = "cturdvgl-nkfbfrkd-dffduedi-nhnnilvj-vglbibth-ltlhggnn-hndckuel-ltennfjt"

func TestRecoveryKeyBlocks(t *testing.T) {
	blocks, err := RecoveryKeyBlocks(realRecoveryKey + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(blocks, "-") != realRecoveryKey {
		t.Fatalf("blocks %v do not rebuild the key", blocks)
	}
	short := realRecoveryKey[:strings.LastIndex(realRecoveryKey, "-")]
	if _, err := RecoveryKeyBlocks(short); err == nil {
		t.Error("7 blocks accepted")
	}
	if _, err := RecoveryKeyBlocks("a" + realRecoveryKey[1:]); err == nil {
		t.Error("non-modhex character accepted")
	}
	if _, err := RecoveryKeyBlocks(""); err == nil {
		t.Error("empty key accepted")
	}
}

func TestSameBlock(t *testing.T) {
	want := realRecoveryKey[:8]
	for _, typed := range []string{want, " " + want + " "} {
		if !SameBlock(want, typed) {
			t.Errorf("%q rejected", typed)
		}
	}
	// The boot prompt refuses these (fw13 VM, 2026-10-11).
	for _, typed := range []string{"", want[:7], want + want[:1], realRecoveryKey[9:17], strings.ToUpper(want), want[:4] + " " + want[4:]} {
		if SameBlock(want, typed) {
			t.Errorf("%q accepted", typed)
		}
	}
}

func TestChallenge(t *testing.T) {
	seen := map[int]int{}
	for range 2000 {
		picks, err := Challenge()
		if err != nil {
			t.Fatal(err)
		}
		if len(picks) != ChallengeBlocks {
			t.Fatalf("got %d blocks, want %d", len(picks), ChallengeBlocks)
		}
		for i, p := range picks {
			if p < 0 || p >= RecoveryBlocks || i > 0 && p <= picks[i-1] {
				t.Fatalf("picks %v not distinct, ascending and in range", picks)
			}
			seen[p]++
		}
	}
	// Each block is picked 3/8 of the time, 750 of 2000 on average.
	for i := range RecoveryBlocks {
		if seen[i] < 600 || seen[i] > 900 {
			t.Errorf("block %d picked %d times of 2000", i, seen[i])
		}
	}
}

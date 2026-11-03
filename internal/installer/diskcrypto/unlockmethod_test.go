package diskcrypto

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// SYNTHETIC journal lines in the journalctl -o json shape systemd 260
// writes for SD_MESSAGE_TPM_NVPCR_EXTEND. The UUIDs are made up.
const (
	ourUUID   = "0e1f2a3b-4c5d-4e6f-8a9b-0c1d2e3f4a5b"
	otherUUID = "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a"
)

func journalLine(measuring string) string {
	return `{"MEASURING":"` + measuring + `","__CURSOR":"s=x"}` + "\n"
}

func TestParseBootUnlockMethod(t *testing.T) {
	cases := []struct {
		name    string
		journal string
		want    string
	}{
		{"no record", "", UnlockUnknown},
		{"chip", journalLine("cryptsetup-keyslot:root:" + ourUUID + ":tpm2:1"), UnlockChip},
		{"recovery key", journalLine("cryptsetup-keyslot:root:" + ourUUID + "::2"), UnlockTyped},
		{"fido2", journalLine("cryptsetup-keyslot:root:" + ourUUID + ":fido2:3"), UnlockTyped},
		{"upper case uuid", journalLine("cryptsetup-keyslot:root:0E1F2A3B-4C5D-4E6F-8A9B-0C1D2E3F4A5B:tpm2:1"), UnlockChip},
		{"other volume", journalLine("cryptsetup-keyslot:data:" + otherUUID + "::0"), UnlockUnknown},
		{"other prefix", journalLine("verity:root:" + ourUUID + ":tpm2:1"), UnlockUnknown},
		{"no keyslot", journalLine("cryptsetup-keyslot:root:" + ourUUID + ":tpm2:"), UnlockUnknown},
		{"too many parts", journalLine("cryptsetup-keyslot:ro:ot:" + ourUUID + ":tpm2:1"), UnlockUnknown},
		{"byte array field", `{"MEASURING":[99,114,121]}` + "\n", UnlockUnknown},
		{"no field", `{"MESSAGE":"x"}` + "\n", UnlockUnknown},
		{"last wins", journalLine("cryptsetup-keyslot:root:"+ourUUID+"::2") + journalLine("cryptsetup-keyslot:root:"+ourUUID+":tpm2:1"), UnlockChip},
	}
	for _, c := range cases {
		got, err := ParseBootUnlockMethod([]byte(c.journal), ourUUID)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if _, err := ParseBootUnlockMethod([]byte("not json\n"), ourUUID); err == nil {
		t.Error("broken journal line: no error")
	}
}

func TestPCRLockPolicyLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pcrlock.json")
	if _, err := PCRLockPolicyLocks(path, 4); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing policy: %v, want not exist", err)
	}
	if err := os.WriteFile(path, []byte(`{"pcrValues":[{"pcr":0,"values":["aa"]},{"pcr":7,"values":["bb"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if locked, err := PCRLockPolicyLocks(path, 4); err != nil || locked {
		t.Fatalf("PCR 4 gap: %t %v", locked, err)
	}
	if locked, err := PCRLockPolicyLocks(path, 7); err != nil || !locked {
		t.Fatalf("PCR 7: %t %v", locked, err)
	}
}

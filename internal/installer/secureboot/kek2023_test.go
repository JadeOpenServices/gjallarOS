package secureboot

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Microsoft's owner GUID 77fa9abd-0359-4d32-bd60-28f4e78f784b as stored.
var microsoftOwner = []byte{
	0xbd, 0x9a, 0xfa, 0x77, 0x59, 0x03, 0x32, 0x4d,
	0xbd, 0x60, 0x28, 0xf4, 0xe7, 0x8f, 0x78, 0x4b,
}

// x509SignatureList wraps a certificate in an EFI_SIGNATURE_LIST.
func x509SignatureList(der []byte) []byte {
	list := []byte(x509SignatureType)
	list = binary.LittleEndian.AppendUint32(list, uint32(28+16+len(der)))
	list = binary.LittleEndian.AppendUint32(list, 0)
	list = binary.LittleEndian.AppendUint32(list, uint32(16+len(der)))
	list = append(list, microsoftOwner...)
	return append(list, der...)
}

func microsoftKEK2011(t *testing.T) []byte {
	t.Helper()
	der, err := os.ReadFile("testdata/microsoft-kek-ca-2011.der")
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func kekOps(kekDefault []byte, calls *[]string) enrollmentOps {
	inspection := 0
	return enrollmentOps{
		inspect: func(context.Context) (Inspection, error) {
			inspection++
			if inspection == 1 {
				return Inspection{State: StatePendingEnrollment, SetupMode: true}, nil
			}
			return Inspection{State: StateGjallarManaged}, nil
		},
		present: func(name string) (bool, error) { return name != "PK", nil },
		payload: func(name string) ([]byte, bool, error) {
			if name == "KEKDefault" {
				return kekDefault, kekDefault != nil, nil
			}
			return []byte("stable-" + name), true, nil
		},
		stageCustomKEK: func(der []byte) error {
			sum := sha256.Sum256(der)
			*calls = append(*calls, "stage "+hex.EncodeToString(sum[:]))
			return nil
		},
		glob: func(pattern string) ([]string, error) {
			return []string{"/efi/" + pattern}, nil
		},
		command: func(_ context.Context, name string, args ...string) error {
			*calls = append(*calls, name+" "+strings.Join(args, " "))
			return nil
		},
		verifyArtifacts: func(context.Context) error { return nil },
		recordOwnership: func(context.Context, string) error { return nil },
	}
}

func sbctlCall(t *testing.T, calls []string) string {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, "sbctl ") {
			return call
		}
	}
	t.Fatalf("sbctl was not run: %v", calls)
	return ""
}

func TestMicrosoftKEKCertificatesMatchPublishedThumbprints(t *testing.T) {
	for _, c := range []struct {
		der            []byte
		sha1, sha256Of string
	}{
		{microsoftKEK2023, "459ab6fb5e284d272d5e3e6abc8ed663829d632b", microsoftKEK2023SHA256},
		{microsoftKEK2011(t), "31590bfd89c9d74ed087dfac66334b3931254b30", microsoftKEK2011SHA256},
	} {
		if got := sha1.Sum(c.der); hex.EncodeToString(got[:]) != c.sha1 {
			t.Fatalf("thumbprint = %x, want %s", got, c.sha1)
		}
		if got := sha256.Sum256(c.der); hex.EncodeToString(got[:]) != c.sha256Of {
			t.Fatalf("sha256 = %x, want %s", got, c.sha256Of)
		}
	}
}

func TestEnrollFirmwareAddsMicrosoftKEK2023NextToKEK2011(t *testing.T) {
	var calls []string
	ops := kekOps(x509SignatureList(microsoftKEK2011(t)), &calls)

	if _, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops); err != nil {
		t.Fatal(err)
	}
	stage := slices.Index(calls, "stage "+microsoftKEK2023SHA256)
	sbctl := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "sbctl ") })
	if stage < 0 || sbctl < stage {
		t.Fatalf("KEK 2023 was not staged before sbctl: %v", calls)
	}
	if !strings.Contains(sbctlCall(t, calls), " --custom") {
		t.Fatalf("sbctl did not read the custom KEK: %v", calls)
	}
}

func TestEnrollFirmwareSkipsKEK2023WhenFirmwareHasIt(t *testing.T) {
	var calls []string
	builtin := append(x509SignatureList(microsoftKEK2011(t)), x509SignatureList(microsoftKEK2023)...)
	ops := kekOps(builtin, &calls)

	if _, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "stage ") || strings.Contains(sbctlCall(t, calls), "--custom") {
		t.Fatalf("KEK 2023 was added twice: %v", calls)
	}
}

func TestEnrollFirmwareAddsNoMicrosoftKEKWhenKEKIsNotPreserved(t *testing.T) {
	var calls []string
	snapshot := enrollmentTestSnapshot()
	snapshot.PreserveFirmwareBuiltin = []string{"db"}
	ops := kekOps(x509SignatureList(microsoftKEK2011(t)), &calls)

	if _, err := enrollFirmware(context.Background(), snapshot, ops); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "stage ") || strings.Contains(sbctlCall(t, calls), "--custom") {
		t.Fatalf("Microsoft KEK added without a preserved built-in KEK: %v", calls)
	}
}

func TestEnrollFirmwareStopsWhenKEK2023CannotBeStaged(t *testing.T) {
	var calls []string
	ops := kekOps(x509SignatureList(microsoftKEK2011(t)), &calls)
	ops.stageCustomKEK = func([]byte) error { return errors.New("read-only") }

	if _, err := enrollFirmware(context.Background(), enrollmentTestSnapshot(), ops); err == nil {
		t.Fatal("enrollment went on without KEK 2023")
	}
	if len(calls) != 0 {
		t.Fatalf("firmware was changed: %v", calls)
	}
}

func TestStageCustomKEKWritesOnlyKEK2023(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom")
	for range 2 {
		if err := stageCustomKEK(dir, microsoftKEK2023); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "KEK", microsoftKEK2023File))
	if err != nil || string(got) != string(microsoftKEK2023) {
		t.Fatalf("staged file = %d bytes, %v", len(got), err)
	}
}

func TestStageCustomKEKRefusesOtherCertificates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom")
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "other.pem"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := stageCustomKEK(dir, microsoftKEK2023)
	if err == nil || !strings.Contains(err.Error(), "other.pem") {
		t.Fatalf("got %v, want a refusal naming the extra file", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "KEK", microsoftKEK2023File)); !os.IsNotExist(err) {
		t.Fatalf("KEK 2023 was staged next to a foreign certificate: %v", err)
	}
}

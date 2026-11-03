package secureboot

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SYNTHETIC certificate, made up for the test.
func synthCert(t *testing.T, name string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// hashSignatureList is an EFI_SIGNATURE_LIST of n SYNTHETIC SHA-256 hashes.
func hashSignatureList(n int) []byte {
	list := []byte(sha256SignatureType)
	list = binary.LittleEndian.AppendUint32(list, uint32(28+n*48))
	list = binary.LittleEndian.AppendUint32(list, 0)
	list = binary.LittleEndian.AppendUint32(list, 48)
	for i := range n {
		list = append(list, microsoftOwner...)
		list = append(list, make([]byte, 31)...)
		list = append(list, byte(i))
	}
	return list
}

// EMULATED firmware: efivarfs files with their 4-byte attribute header.
type fakeFirmware struct {
	t    *testing.T
	root firmwareFiles
}

func newFakeFirmware(t *testing.T) fakeFirmware {
	dir := t.TempDir()
	f := fakeFirmware{t, firmwareFiles{
		efivars:    filepath.Join(dir, "efivars"),
		mokvars:    filepath.Join(dir, "mok-variables"),
		attributes: filepath.Join(dir, "firmware-attributes"),
	}}
	for _, d := range []string{f.root.efivars, f.root.mokvars, f.root.attributes} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f fakeFirmware) set(name, guid string, payload []byte) {
	f.t.Helper()
	raw := append([]byte{0x06, 0, 0, 0}, payload...)
	if err := os.WriteFile(filepath.Join(f.root.efivars, name+"-"+guid), raw, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f fakeFirmware) password(dir, role, enabled string) {
	f.t.Helper()
	auth := filepath.Join(f.root.attributes, "synthetic-lmi", "authentication", dir)
	if err := os.MkdirAll(auth, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if role != "" {
		os.WriteFile(filepath.Join(auth, "role"), []byte(role+"\n"), 0o644)
	}
	os.WriteFile(filepath.Join(auth, "is_enabled"), []byte(enabled+"\n"), 0o644)
}

func (f fakeFirmware) read() FirmwareTrust {
	f.t.Helper()
	trust, err := f.root.read()
	if err != nil {
		f.t.Fatal(err)
	}
	return trust
}

func findingsText(t FirmwareTrust) string {
	var lines []string
	for _, f := range t.Findings() {
		mark := "-"
		if f.Critical {
			mark = "!"
		}
		lines = append(lines, mark+" "+f.Text)
	}
	return strings.Join(lines, "\n")
}

// The locked laptop's state (Plane 293): maker PK, Microsoft 2011 KEK and
// UEFI CA only, admin password set. The PK and the UEFI CA are SYNTHETIC
// stand-ins; the KEK is Microsoft's real 2011 certificate.
func TestFirmwareTrustLockedLaptop(t *testing.T) {
	f := newFakeFirmware(t)
	pk, pkKey := synthCert(t, "SYNTHETIC Maker PK", nil, nil)
	makerKEK, makerKEKKey := synthCert(t, "SYNTHETIC Maker KEK", pk, pkKey)
	makerDB, _ := synthCert(t, "SYNTHETIC Maker db key", makerKEK, makerKEKKey)
	f.set("SecureBoot", efiGlobalGUID, []byte{1})
	f.set("SetupMode", efiGlobalGUID, []byte{0})
	f.set("PK", efiGlobalGUID, x509SignatureList(pk.Raw))
	f.set("KEK", efiGlobalGUID, append(x509SignatureList(microsoftKEK2011(t)), x509SignatureList(makerKEK.Raw)...))
	f.set("db", efiImageSecGUID, append(x509SignatureList(makerDB.Raw), hashSignatureList(2)...))
	f.set("dbx", efiImageSecGUID, hashSignatureList(5))
	f.password("Admin", "bios-admin", "1")
	f.password("Power-on", "power-on", "0")

	trust := f.read()
	if trust.SecureBoot != FlagOn || trust.SetupMode != FlagOff || trust.AuditMode != FlagMissing {
		t.Errorf("flags: %+v", trust)
	}
	if trust.AdminPassword != PasswordSet || !trust.MayBeLocked() {
		t.Errorf("password %q", trust.AdminPassword)
	}
	if c := trust.KEK.Certs; len(c) != 2 || c[0].Kind != CertMicrosoft || c[0].Name != "Microsoft KEK CA 2011" || c[1].Kind != CertPlatform {
		t.Errorf("KEK: %+v", c)
	}
	if c := trust.DB.Certs; len(c) != 1 || c[0].Kind != CertPlatform || trust.DB.Hashes != 2 {
		t.Errorf("db: %+v", trust.DB)
	}
	if trust.DBX.Hashes != 5 {
		t.Errorf("dbx: %+v", trust.DBX)
	}
	got := findingsText(trust)
	for _, want := range []string{
		"- The KEK holds only Microsoft's 2011 key",
		"- db lacks Microsoft UEFI CA 2023",
		"- db also trusts 2 single files by hash.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "!") || strings.Contains(got, "Maker db key") {
		t.Errorf("unexpected finding:\n%s", got)
	}
	lines := strings.Join(trust.Lines(), "\n")
	if !strings.Contains(lines, "KEK: Microsoft KEK CA 2011; SYNTHETIC Maker KEK (signed by a PK or KEK key)") || !strings.Contains(lines, "SYNTHETIC Maker db key (signed by a PK or KEK key); 2 file hashes") ||
		!strings.Contains(lines, "Revocation list (dbx): 5 entries") || !strings.Contains(lines, "Firmware setup password: set") {
		t.Errorf("lines:\n%s", lines)
	}
}

func TestFirmwareTrustNamesKEK2023AndFlagsRisks(t *testing.T) {
	f := newFakeFirmware(t)
	pk, _ := synthCert(t, "SYNTHETIC DO NOT TRUST - AMI Test PK", nil, nil)
	stranger, _ := synthCert(t, "SYNTHETIC \x1b[2Jstranger", nil, nil)
	mok, _ := synthCert(t, "SYNTHETIC MOK key", nil, nil)
	f.set("SecureBoot", efiGlobalGUID, []byte{1})
	f.set("SetupMode", efiGlobalGUID, []byte{0})
	f.set("PK", efiGlobalGUID, x509SignatureList(pk.Raw))
	f.set("KEK", efiGlobalGUID, append(x509SignatureList(microsoftKEK2011(t)), x509SignatureList(microsoftKEK2023)...))
	f.set("db", efiImageSecGUID, x509SignatureList(stranger.Raw))
	f.set("MokSBStateRT", shimLockGUID, []byte{1})
	if err := os.WriteFile(filepath.Join(f.root.mokvars, "MokListRT"), x509SignatureList(mok.Raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root.mokvars, "SbatLevelRT"), []byte("sbat,1,2024010900\nshim,4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	trust := f.read()
	if trust.KEK.Certs[1].Name != "Microsoft KEK CA 2023" || !trust.PK.Certs[0].TestKey {
		t.Errorf("certs: %+v %+v", trust.KEK.Certs, trust.PK.Certs)
	}
	if trust.AdminPassword != PasswordUnknown || !trust.MayBeLocked() {
		t.Errorf("no attributes interface: %q", trust.AdminPassword)
	}
	if trust.ShimSBATLevel != "sbat,1,2024010900" {
		t.Errorf("sbat %q", trust.ShimSBATLevel)
	}
	got := findingsText(trust)
	for _, want := range []string{
		"! The Platform Key is a published firmware test key (SYNTHETIC DO NOT TRUST - AMI Test PK)",
		"! Shim's signature check is switched off",
		"- db also trusts SYNTHETIC [2Jstranger (SHA-256 ",
		"- Shim also trusts SYNTHETIC MOK key",
		"- The revocation list (dbx) is empty",
		"- db lacks Microsoft UEFI CA 2023",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "2011 key") || strings.ContainsRune(got, 0x1b) {
		t.Errorf("unexpected:\n%s", got)
	}
	if !trust.Findings()[0].Critical || !trust.Findings()[1].Critical {
		t.Errorf("critical ones not first:\n%s", got)
	}
}

func TestFirmwareTrustSetupModeAndMalformed(t *testing.T) {
	f := newFakeFirmware(t)
	f.set("SecureBoot", efiGlobalGUID, []byte{0})
	f.set("SetupMode", efiGlobalGUID, []byte{1})
	f.set("db", efiImageSecGUID, []byte("SYNTHETIC not a signature list at all"))
	f.password("Admin", "", "1")

	trust := f.read()
	if trust.MayBeLocked() {
		t.Error("Setup Mode needs no firmware setup")
	}
	if trust.AdminPassword != PasswordSet {
		t.Errorf("Admin dir without role: %q", trust.AdminPassword)
	}
	got := findingsText(trust)
	if !strings.Contains(got, "! The firmware's db list does not read as a signature list.") ||
		!strings.Contains(got, "- The firmware is in Setup Mode") || strings.Contains(got, "dbx") {
		t.Errorf("findings:\n%s", got)
	}

	f.password("Admin", "", "0")
	if trust := f.read(); trust.AdminPassword != PasswordNotSet {
		t.Errorf("password %q", trust.AdminPassword)
	}

	f.set("SetupMode", efiGlobalGUID, []byte{7})
	if _, err := f.root.read(); err == nil {
		t.Error("flag value 7 accepted")
	}
}

func TestFirmwareTrustRefusesHugeVariable(t *testing.T) {
	f := newFakeFirmware(t)
	f.set("db", efiImageSecGUID, make([]byte, maxVariable+8))
	if _, err := f.root.read(); err == nil {
		t.Error("oversized db read")
	}
}

func TestParseSignatureListBounds(t *testing.T) {
	good := x509SignatureList(microsoftKEK2023)
	for name, raw := range map[string][]byte{
		"short":           good[:20],
		"size past end":   binary.LittleEndian.AppendUint32(append([]byte{}, good[:16]...), uint32(len(good)+1)),
		"zero sig size":   append(append(append([]byte{}, good[:24]...), 0, 0, 0, 0), good[28:]...),
		"truncated entry": good[:len(good)-1],
	} {
		if l := parseSignatureList(raw, true); !l.Malformed {
			t.Errorf("%s: parsed %+v", name, l)
		}
	}
	if l := parseSignatureList(good, true); l.Malformed || len(l.Certs) != 1 {
		t.Errorf("good list: %+v", l)
	}
}

// EMULATED shim boot: the kernel's copy of shim's key list and check state.
func TestShimTrustsKey(t *testing.T) {
	ours, _ := synthCert(t, "SYNTHETIC GjallarOS db", nil, nil)
	other, _ := synthCert(t, "SYNTHETIC distro CA", nil, nil)
	write := func(f fakeFirmware, name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.root.mokvars, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		mok     []byte
		sbState []byte
		want    bool
	}{
		{"no shim this boot", nil, nil, false},
		{"our key in MOK", append(x509SignatureList(other.Raw), x509SignatureList(ours.Raw)...), nil, true},
		{"shim check on", x509SignatureList(ours.Raw), []byte{0}, true},
		{"only another key", x509SignatureList(other.Raw), nil, false},
		{"shim check off in MokManager", x509SignatureList(ours.Raw), []byte{1}, false},
		{"odd check state", x509SignatureList(ours.Raw), []byte{0, 0}, false},
		{"malformed list", append(x509SignatureList(ours.Raw), 1, 2, 3), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFirmware(t)
			if tc.mok != nil {
				write(f, "MokListRT", tc.mok)
			}
			if tc.sbState != nil {
				write(f, "MokSBStateRT", tc.sbState)
			}
			got, err := f.root.shimTrustsKey(ours.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("shimTrustsKey = %v, want %v", got, tc.want)
			}
		})
	}
}

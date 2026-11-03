package secureboot

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// digestList is an EFI_SIGNATURE_LIST of SYNTHETIC SHA-256 digests of one
// kind: plain file hashes, or X.509 TBS hashes with their 16-byte time.
func digestList(t *testing.T, kind string, sums ...string) []byte {
	t.Helper()
	size := 16 + sha256.Size
	if kind == x509SHA256SignatureType {
		size += 16
	}
	list := []byte(kind)
	list = binary.LittleEndian.AppendUint32(list, uint32(28+len(sums)*size))
	list = binary.LittleEndian.AppendUint32(list, 0)
	list = binary.LittleEndian.AppendUint32(list, uint32(size))
	for _, s := range sums {
		raw, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, microsoftOwner...)
		list = append(list, raw...)
		if kind == x509SHA256SignatureType {
			list = append(list, make([]byte, 16)...)
		}
	}
	return list
}

// shimCase is a SYNTHETIC shim with its signing chain and a firmware that
// trusts it: everything ChoosePath needs, made up for the test.
type shimCase struct {
	facts   ShimFacts
	ca      []byte
	chain   []byte
	ours    []byte
	oursTBS []byte
}

func newShimCase(t *testing.T) shimCase {
	ca, caKey := synthCert(t, "SYNTHETIC UEFI CA", nil, nil)
	signer, _ := synthCert(t, "SYNTHETIC shim signer", ca, caKey)
	vendor, _ := synthCert(t, "SYNTHETIC distro CA", nil, nil)
	ours, _ := synthCert(t, "SYNTHETIC GjallarOS db", nil, nil)
	return shimCase{
		facts: ShimFacts{
			Schema:       1,
			SignerCAs:    []string{sum(ca.Raw)},
			ChainCerts:   []string{sum(ca.Raw), sum(signer.Raw)},
			ChainTBS:     []string{sum(ca.RawTBSCertificate), sum(signer.RawTBSCertificate)},
			VendorCA:     sum(vendor.Raw),
			VendorCATBS:  sum(vendor.RawTBSCertificate),
			Authenticode: map[string]string{"shimx64.efi": sum([]byte("SYNTHETIC shim")), "mmx64.efi": sum([]byte("SYNTHETIC MokManager"))},
			SBAT:         []string{"sbat,1,SBAT Version,sbat,1,https://example.invalid/sbat", "shim,4,EXAMPLE,shim,1,https://example.invalid"},
		},
		ca:      ca.Raw,
		chain:   signer.RawTBSCertificate,
		ours:    ours.Raw,
		oursTBS: ours.RawTBSCertificate,
	}
}

func (c shimCase) trust(dbx []byte) FirmwareTrust {
	return FirmwareTrust{
		SetupMode: FlagOff,
		DB:        parseSignatureList(x509SignatureList(c.ca), true),
		DBX:       parseSignatureList(dbx, dbx != nil),
	}
}

func TestChoosePath(t *testing.T) {
	c := newShimCase(t)
	shimFile := c.facts.Authenticode["shimx64.efi"]
	for _, tc := range []struct {
		name   string
		trust  func() FirmwareTrust
		ours   []byte
		path   Path
		reason string
	}{
		{"maker keys trust shim", func() FirmwareTrust { return c.trust(nil) }, c.ours, PathShim, "trusts the signer"},
		{"unrelated dbx entries", func() FirmwareTrust { return c.trust(digestList(t, sha256SignatureType, sum([]byte("other")))) }, c.ours, PathShim, "trusts the signer"},
		{"setup mode", func() FirmwareTrust { tr := c.trust(nil); tr.SetupMode = FlagOn; return tr }, nil, PathOwnKeys, "Setup Mode"},
		{"db lacks the CA", func() FirmwareTrust {
			tr := c.trust(nil)
			tr.DB = parseSignatureList(hashSignatureList(1), true)
			return tr
		}, nil, PathOwnKeys, "does not hold the certificate"},
		{"dbx kind unknown", func() FirmwareTrust { tr := c.trust(nil); tr.DBX.Present, tr.DBX.Other = true, 1; return tr }, nil, PathOwnKeys, "cannot compare"},
		{"dbx holds the CA", func() FirmwareTrust { return c.trust(x509SignatureList(c.ca)) }, nil, PathOwnKeys, "signature"},
		{"dbx holds the signer TBS", func() FirmwareTrust { return c.trust(digestList(t, x509SHA256SignatureType, sum(c.chain))) }, nil, PathOwnKeys, "signature"},
		{"dbx holds the vendor TBS", func() FirmwareTrust {
			return c.trust(digestList(t, x509SHA256SignatureType, c.facts.VendorCATBS))
		}, nil, PathOwnKeys, "MokManager"},
		{"dbx holds shim itself", func() FirmwareTrust { return c.trust(digestList(t, sha256SignatureType, shimFile)) }, nil, PathOwnKeys, "shimx64.efi"},
		{"dbx holds our key", func() FirmwareTrust { return c.trust(x509SignatureList(c.ours)) }, c.ours, PathOwnKeys, "GjallarOS key"},
		{"dbx holds our key TBS", func() FirmwareTrust { return c.trust(digestList(t, x509SHA256SignatureType, sum(c.oursTBS))) }, c.ours, PathOwnKeys, "GjallarOS key"},
		{"our key unknown yet", func() FirmwareTrust { return c.trust(x509SignatureList(c.ours)) }, nil, PathShim, "trusts the signer"},
		{"SBAT level above shim", func() FirmwareTrust {
			tr := c.trust(nil)
			tr.ShimSBAT = []string{"sbat,1,2024010900", "shim,5"}
			return tr
		}, nil, PathOwnKeys, "shim generation 5"},
		{"SBAT level met", func() FirmwareTrust {
			tr := c.trust(nil)
			tr.ShimSBAT = []string{"sbat,1,2024010900", "shim,4", "grub,4"}
			return tr
		}, nil, PathShim, "trusts the signer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChoosePath(tc.trust(), c.facts, tc.ours)
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != tc.path || !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("ChoosePath = %+v, want %s (%q)", got, tc.path, tc.reason)
			}
		})
	}
}

func TestChoosePathRefusesMalformedLists(t *testing.T) {
	c := newShimCase(t)
	broken := append(x509SignatureList(c.ca), 1, 2, 3)
	tr := c.trust(nil)
	tr.DB = parseSignatureList(broken, true)
	if _, err := ChoosePath(tr, c.facts, nil); err == nil {
		t.Error("malformed db chose a path")
	}
	tr = c.trust(broken)
	if _, err := ChoosePath(tr, c.facts, nil); err == nil {
		t.Error("malformed dbx chose a path")
	}
}

func TestSBATRevokes(t *testing.T) {
	ours := []string{"sbat,1,SBAT Version", "shim,4"}
	for _, tc := range []struct {
		level []string
		want  string
	}{
		{[]string{"sbat,1,2024010900", "shim,4"}, ""},
		{[]string{"sbat,1,2024010900", "grub,9"}, ""},
		{[]string{"shim,5"}, "generation 5"},
		{[]string{"unreadable"}, "not text"},
		{[]string{"shim"}, "without a generation"},
		{[]string{"shim,x"}, "without a generation"},
		{[]string{"shim,-1"}, "without a generation"},
	} {
		got := sbatRevokes(tc.level, ours)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("sbatRevokes(%q) = %q, want %q", tc.level, got, tc.want)
		}
	}
	if got := sbatRevokes(nil, []string{"shim"}); !strings.Contains(got, "does not parse") {
		t.Errorf("broken own SBAT: %q", got)
	}
}

func TestParseShimFacts(t *testing.T) {
	good := newShimCase(t).facts
	encode := func(change func(map[string]any)) []byte {
		raw, _ := json.Marshal(good)
		var m map[string]any
		json.Unmarshal(raw, &m)
		if change != nil {
			change(m)
		}
		out, _ := json.Marshal(m)
		return out
	}
	if _, err := parseShimFacts(encode(nil)); err != nil {
		t.Fatalf("good facts: %v", err)
	}
	for name, raw := range map[string][]byte{
		"unknown field":   encode(func(m map[string]any) { m["extra"] = 1 }),
		"schema 2":        encode(func(m map[string]any) { m["schema"] = 2 }),
		"upper-case hex":  encode(func(m map[string]any) { m["vendorCA"] = strings.ToUpper(good.VendorCA) }),
		"short sum":       encode(func(m map[string]any) { m["chainTBS"] = []string{"abcd"} }),
		"no signer CA":    encode(func(m map[string]any) { m["signerCAs"] = []string{} }),
		"no vendor CA":    encode(func(m map[string]any) { m["vendorCA"] = "" }),
		"no authenticode": encode(func(m map[string]any) { m["authenticode"] = map[string]string{} }),
		"no SBAT":         encode(func(m map[string]any) { m["sbat"] = []string{} }),
		"SBAT no gen":     encode(func(m map[string]any) { m["sbat"] = []string{"shim"} }),
		"trailing data":   append(encode(nil), []byte(" {}")...),
	} {
		if _, err := parseShimFacts(raw); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestShimVarPermissionFallsBackFailClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0000 file")
	}
	ours, _ := synthCert(t, "SYNTHETIC GjallarOS db", nil, nil)
	list := x509SignatureList(ours.Raw)
	f := newFakeFirmware(t)
	locked := filepath.Join(f.root.mokvars, "MokListRT")
	if err := os.WriteFile(locked, list, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.root.shimVar("MokListRT"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("no efivar copy: err = %v, want permission error", err)
	}
	f.set("MokListRT", shimLockGUID, list)
	raw, present, err := f.root.shimVar("MokListRT")
	if err != nil || !present || string(raw) != string(list) {
		t.Fatalf("efivar copy: present=%v err=%v", present, err)
	}
}

func TestMokHolds(t *testing.T) {
	ours, _ := synthCert(t, "SYNTHETIC GjallarOS db", nil, nil)
	other, _ := synthCert(t, "SYNTHETIC distro CA", nil, nil)
	f := newFakeFirmware(t)
	if got, err := mokHolds(f.root, "MokNew", ours.Raw); got || err != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	// A request lives in efivarfs only; a file with the same name in the
	// kernel's mirror is not a request.
	os.WriteFile(filepath.Join(f.root.mokvars, "MokNew"), x509SignatureList(ours.Raw), 0o644)
	if got, _ := mokHolds(f.root, "MokNew", ours.Raw); got {
		t.Fatal("MokNew read from the mirror")
	}
	f.set("MokNew", shimLockGUID, append(x509SignatureList(other.Raw), x509SignatureList(ours.Raw)...))
	if got, err := mokHolds(f.root, "MokNew", ours.Raw); !got || err != nil {
		t.Fatalf("queued: %v %v", got, err)
	}
	os.WriteFile(filepath.Join(f.root.mokvars, "MokListRT"), x509SignatureList(other.Raw), 0o644)
	if got, _ := mokHolds(f.root, "MokListRT", ours.Raw); got {
		t.Fatal("other key counted as ours")
	}
	os.WriteFile(filepath.Join(f.root.mokvars, "MokListRT"), append(x509SignatureList(ours.Raw), 9), 0o644)
	if _, err := mokHolds(f.root, "MokListRT", ours.Raw); err == nil {
		t.Fatal("malformed list accepted")
	}
}

func TestMokHoldsRootOnlyRequest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0000 file")
	}
	ours, _ := synthCert(t, "SYNTHETIC GjallarOS db", nil, nil)
	f := newFakeFirmware(t)
	path := filepath.Join(f.root.efivars, "MokNew-"+shimLockGUID)
	raw := append([]byte{0x07, 0, 0, 0}, x509SignatureList(ours.Raw)...)
	// mokutil leaves the request mode 0600 for root.
	if err := os.WriteFile(path, raw, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := mokHolds(f.root, "MokNew", ours.Raw); got || err == nil {
		t.Fatalf("no root reader: %v %v, want an error", got, err)
	}
	var asked string
	f.root.rootRead = func(p string) ([]byte, error) {
		asked = p
		return raw, nil
	}
	if got, err := mokHolds(f.root, "MokNew", ours.Raw); !got || err != nil {
		t.Fatalf("root reader: %v %v", got, err)
	}
	if asked != path {
		t.Fatalf("root read %q, want %q", asked, path)
	}
	f.root.rootRead = func(string) ([]byte, error) { return nil, errors.New("sudo refused") }
	if got, err := mokHolds(f.root, "MokNew", ours.Raw); got || err == nil {
		t.Fatalf("refused root read: %v %v, want an error", got, err)
	}
	f.root.rootRead = func(string) ([]byte, error) { return make([]byte, maxVariable+1), nil }
	if _, err := mokHolds(f.root, "MokNew", ours.Raw); err == nil {
		t.Fatal("oversized root read accepted")
	}
}

func TestEFISpaceFor(t *testing.T) {
	dir := t.TempDir()
	if err := efiSpaceFor(dir, 1024); err != nil {
		t.Fatalf("small write: %v", err)
	}
	if err := efiSpaceFor(dir, 1<<62); err == nil {
		t.Fatal("write larger than any disk allowed")
	}
	if err := efiSpaceFor(filepath.Join(dir, "missing"), 1); err == nil {
		t.Fatal("missing store allowed")
	}
}

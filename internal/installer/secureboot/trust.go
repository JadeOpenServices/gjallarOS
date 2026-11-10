package secureboot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// The firmware trust check reads what the firmware trusts right now and
// names it. It decides nothing from a fixed list: known certificates only
// get a readable name, matched by fingerprint, never by the name they carry.
// Everything it reads is readable without root, so it runs before any
// password prompt (Plane 293, wiki 951 "Firmware trust check").

// EFI variable GUIDs, as they appear in efivarfs file names.
const (
	efiGlobalGUID   = "8be4df61-93ca-11d2-aa0d-00e098032b8c"
	efiImageSecGUID = "d719b2cb-3d3a-4596-a3bc-dad00e67656f"
	shimLockGUID    = "605dab50-e046-4300-abb6-3dd810dd8b23"
)

// Signature types besides x509SignatureType, as stored.
var (
	// EFI_CERT_SHA256_GUID c1c41626-504c-4092-aca9-41f936934328
	sha256SignatureType = string([]byte{
		0x26, 0x16, 0xc4, 0xc1, 0x4c, 0x50, 0x92, 0x40,
		0xac, 0xa9, 0x41, 0xf9, 0x36, 0x93, 0x43, 0x28,
	})
	// EFI_CERT_X509_SHA256_GUID 3bd2a492-96c0-4079-b420-fcf98ef103ed
	x509SHA256SignatureType = string([]byte{
		0x92, 0xa4, 0xd2, 0x3b, 0xc0, 0x96, 0x79, 0x40,
		0xb4, 0x20, 0xfc, 0xf9, 0x8e, 0xf1, 0x03, 0xed,
	})
)

// Known certificates by SHA-256 of the DER. Source: the certificates sbctl
// 0.18 ships (read from its binary, 2026-11-02); each one's SHA-1 matches
// the thumbprint Microsoft publishes for it.
const (
	certMSKEK2011    = microsoftKEK2011SHA256                                             // SHA-1 31590bfd89c9d74ed087dfac66334b3931254b30
	certMSKEK2023    = microsoftKEK2023SHA256                                             // SHA-1 459ab6fb5e284d272d5e3e6abc8ed663829d632b
	certMSUEFICA2011 = "48e99b991f57fc52f76149599bff0a58c47154229b9f8d603ac40d3500248507" // SHA-1 46def63b5ce61cf8ba0de2e6639c1019d0ed14f3
	certMSUEFICA2023 = "f6124e34125bee3fe6d79a574eaa7b91c0e7bd9d929c1a321178efd611dad901" // SHA-1 b5eeb4a6706048073f0ed296e7f580a790b59eaa
	certMSOptROM2023 = "e5be3e64c6e66a281457ecdece0d6d0787577aad2a3a0144262c10c14ba8d8f1" // SHA-1 3fb39e2b8bd183bf9e4594e72183ca60afcd4277
	certMSOSPCA2011  = "e8e95f0733a55e8bad7be0a1413ee23c51fcea64b3c8fa6a786935fddcc71961" // SHA-1 580a6f4cc4e4b669b9ebdc1b2b3e087b80d0678d
	certMSOSCA2023   = "076f1fea90ac29155ebf77c17682f75f1fdd1be196da302dc8461e350a9ae330" // SHA-1 45a0fa32604773c82433c3b7d59e7466b3ac0c67
)

// Names shown for them. The two OS loader CAs only sign Microsoft's own
// loaders; the UEFI CAs sign other loaders such as the distro shims.
var knownCertNames = map[string]string{
	certMSKEK2011:    "Microsoft KEK CA 2011",
	certMSKEK2023:    "Microsoft KEK CA 2023",
	certMSUEFICA2011: "Microsoft UEFI CA 2011 (signs other loaders, e.g. Linux shims)",
	certMSUEFICA2023: "Microsoft UEFI CA 2023 (signs other loaders, e.g. Linux shims)",
	certMSOptROM2023: "Microsoft Option ROM CA 2023 (signs add-in card firmware)",
	certMSOSPCA2011:  "Microsoft OS loader CA 2011 (Microsoft's own loaders only)",
	certMSOSCA2023:   "Microsoft OS loader CA 2023 (Microsoft's own loaders only)",
}

// CertKind says where a trusted certificate comes from.
type CertKind string

const (
	CertMicrosoft CertKind = "microsoft"
	// CertPlatform is the Platform Key, a KEK it signed, or a db key a
	// PK or KEK key signed: the maker's keys, or GjallarOS's own once it
	// owns the firmware.
	CertPlatform CertKind = "platform"
	CertOther    CertKind = "other"
)

// TrustedCert is one certificate in PK, KEK, db or the shim's key list.
type TrustedCert struct {
	Kind   CertKind
	Name   string // the known name, else the certificate's own subject
	SHA256 string
	// TestKey marks a Platform Key that is a published firmware test key:
	// whoever has such a key can sign anything this firmware boots.
	TestKey bool
	der     []byte
}

// SignatureList is what one of the variables holds.
type SignatureList struct {
	Present bool
	Certs   []TrustedCert
	// Hashes counts file and certificate hashes (SHA-256 and X.509 SHA-256
	// entries); Other counts every other entry type.
	Hashes, Other int
	// FileSums are the SHA-256 entries (Authenticode hashes of single
	// files), TBSSums the X.509 SHA-256 entries (hashes of a certificate's
	// signed part), both as lower-case hex.
	FileSums, TBSSums []string
	// Malformed is set when the variable does not parse; it is then
	// treated as holding unknown entries.
	Malformed bool
}

// PasswordState is what the kernel tells about the firmware setup password.
type PasswordState string

const (
	PasswordUnknown PasswordState = "unknown"
	PasswordNotSet  PasswordState = "not set"
	PasswordSet     PasswordState = "set"
)

// TriState is a firmware flag that may be missing.
type TriState string

const (
	FlagOn      TriState = "on"
	FlagOff     TriState = "off"
	FlagMissing TriState = "missing"
)

// FirmwareTrust is one read of the firmware's trust state.
type FirmwareTrust struct {
	SecureBoot, SetupMode, AuditMode, DeployedMode TriState
	PK, KEK, DB, DBX                               SignatureList
	DBXDefault                                     SignatureList
	// Shim state; all empty when no shim started this boot.
	MokList           SignatureList
	ShimValidationOff bool
	ShimSBATLevel     string
	// ShimSBAT holds every line of shim's SBAT level ("shim,4"), for the
	// revocation check; empty when no shim started this boot.
	ShimSBAT      []string
	AdminPassword PasswordState
}

// TrustFinding is one line the user should know about.
type TrustFinding struct {
	Critical bool
	Text     string
}

type firmwareFiles struct {
	efivars, mokvars, attributes string
	// rootRead reads a file only root may read; nil when there is no way
	// to ask for root.
	rootRead func(path string) ([]byte, error)
}

var liveFirmware = firmwareFiles{
	efivars:    "/sys/firmware/efi/efivars",
	mokvars:    "/sys/firmware/efi/mok-variables",
	attributes: "/sys/class/firmware-attributes",
	rootRead:   sudoRead,
}

// ReadFirmwareTrust reads the running firmware.
func ReadFirmwareTrust() (FirmwareTrust, error) {
	return liveFirmware.read()
}

// ReadModeFlags reads only SecureBoot and SetupMode. Both are world
// readable, so unlike ReadFirmwareTrust this never asks for root.
func ReadModeFlags() (secureBoot, setupMode TriState, err error) {
	return liveFirmware.modeFlags()
}

func (f firmwareFiles) modeFlags() (TriState, TriState, error) {
	secureBoot, err := f.flag("SecureBoot", efiGlobalGUID)
	if err != nil {
		return "", "", err
	}
	setupMode, err := f.flag("SetupMode", efiGlobalGUID)
	if err != nil {
		return "", "", err
	}
	return secureBoot, setupMode, nil
}

// maxVariable caps one variable; real dbx lists are about 15 KiB.
const maxVariable = 1 << 20

func (f firmwareFiles) read() (FirmwareTrust, error) {
	var t FirmwareTrust
	var err error
	flags := []struct {
		name string
		dst  *TriState
	}{
		{"SecureBoot", &t.SecureBoot},
		{"SetupMode", &t.SetupMode},
		{"AuditMode", &t.AuditMode},
		{"DeployedMode", &t.DeployedMode},
	}
	for _, flag := range flags {
		if *flag.dst, err = f.flag(flag.name, efiGlobalGUID); err != nil {
			return FirmwareTrust{}, err
		}
	}
	lists := []struct {
		name, guid string
		dst        *SignatureList
	}{
		{"PK", efiGlobalGUID, &t.PK},
		{"KEK", efiGlobalGUID, &t.KEK},
		{"db", efiImageSecGUID, &t.DB},
		{"dbx", efiImageSecGUID, &t.DBX},
		{"dbxDefault", efiGlobalGUID, &t.DBXDefault},
	}
	for _, list := range lists {
		raw, present, err := f.efivar(list.name, list.guid)
		if err != nil {
			return FirmwareTrust{}, err
		}
		*list.dst = parseSignatureList(raw, present)
	}
	// The platform chain: PK, what PK signed in KEK, and in db what a
	// PK or KEK key signed (whoever holds a KEK key may change db anyway).
	pk := certificates(t.PK)
	classify(&t.PK, pk)
	classify(&t.KEK, pk)
	signers := append(pk, certificates(t.KEK)...)
	classify(&t.DB, signers)

	mok, present, err := f.shimVar("MokListRT")
	if err != nil {
		return FirmwareTrust{}, err
	}
	t.MokList = parseSignatureList(mok, present)
	classify(&t.MokList, signers)
	state, present, err := f.shimVar("MokSBStateRT")
	if err != nil {
		return FirmwareTrust{}, err
	}
	t.ShimValidationOff = present && len(state) > 0 && state[0] != 0
	sbat, present, err := f.shimVar("SbatLevelRT")
	if err != nil {
		return FirmwareTrust{}, err
	}
	if present {
		t.ShimSBATLevel = sbatLevel(sbat)
		t.ShimSBAT = sbatLines(sbat)
	}

	if t.AdminPassword, err = readAdminPassword(f.attributes); err != nil {
		return FirmwareTrust{}, err
	}
	return t, nil
}

func (f firmwareFiles) flag(name, guid string) (TriState, error) {
	raw, present, err := f.efivar(name, guid)
	switch {
	case err != nil:
		return "", err
	case !present:
		return FlagMissing, nil
	case len(raw) != 1 || raw[0] > 1:
		return "", fmt.Errorf("EFI variable %s has an unexpected value", name)
	case raw[0] == 1:
		return FlagOn, nil
	}
	return FlagOff, nil
}

// efivar reads one variable without its 4-byte attribute header.
func (f firmwareFiles) efivar(name, guid string) ([]byte, bool, error) {
	raw, present, err := readCapped(filepath.Join(f.efivars, name+"-"+guid))
	if err != nil || !present {
		return nil, present, err
	}
	if len(raw) < 4 {
		return nil, false, fmt.Errorf("EFI variable %s is shorter than its attribute header", name)
	}
	return raw[4:], true, nil
}

// rootEfivar reads one root-only variable through rootRead, without its
// 4-byte attribute header.
func (f firmwareFiles) rootEfivar(name, guid string) ([]byte, bool, error) {
	if f.rootRead == nil {
		return nil, false, fmt.Errorf("EFI variable %s is readable by root only", name)
	}
	raw, err := f.rootRead(filepath.Join(f.efivars, name+"-"+guid))
	if err != nil {
		return nil, false, fmt.Errorf("read EFI variable %s as root: %w", name, err)
	}
	if len(raw) > maxVariable {
		return nil, false, fmt.Errorf("EFI variable %s is larger than %d bytes", name, maxVariable)
	}
	if len(raw) < 4 {
		return nil, false, fmt.Errorf("EFI variable %s is shorter than its attribute header", name)
	}
	return raw[4:], true, nil
}

// sudoRead reads path as root through sudo, capped like readCapped.
func sudoRead(path string) ([]byte, error) {
	cmd := commandContext(context.Background(), "sudo", privilegedTool("cat"), "--", path)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stdout, maxVariable+1))
	if len(raw) > maxVariable {
		cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if len(raw) > maxVariable {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxVariable)
	}
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return raw, nil
}

// shimVar reads a variable shim leaves for the running system: from the
// kernel's copy of shim's table first (big key lists only exist there),
// else from the EFI variable shim also sets.
func (f firmwareFiles) shimVar(name string) ([]byte, bool, error) {
	raw, present, err := readCapped(filepath.Join(f.mokvars, name))
	if errors.Is(err, fs.ErrPermission) {
		// The kernel keeps its mirror root-only; shim also sets the same
		// variable, which any user may read. Without it the answer stays
		// the permission error, never "not there".
		raw, present, efiErr := f.efivar(name, shimLockGUID)
		if efiErr != nil || !present {
			return nil, false, err
		}
		return raw, true, nil
	}
	if err != nil || present {
		return raw, present, err
	}
	return f.efivar(name, shimLockGUID)
}

func readCapped(path string) ([]byte, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxVariable+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > maxVariable {
		return nil, false, fmt.Errorf("%s is larger than %d bytes", path, maxVariable)
	}
	return raw, true, nil
}

// parseSignatureList reads a sequence of EFI_SIGNATURE_LISTs.
func parseSignatureList(raw []byte, present bool) SignatureList {
	list := SignatureList{Present: present}
	if !present || len(raw) == 0 {
		return list
	}
	for len(raw) > 0 {
		if len(raw) < 28 {
			return SignatureList{Present: true, Malformed: true}
		}
		listSize := int(binary.LittleEndian.Uint32(raw[16:20]))
		headerSize := int(binary.LittleEndian.Uint32(raw[20:24]))
		signatureSize := int(binary.LittleEndian.Uint32(raw[24:28]))
		if listSize < 28 || listSize > len(raw) || headerSize < 0 || signatureSize <= 16 ||
			28+headerSize > listSize || (listSize-28-headerSize)%signatureSize != 0 {
			return SignatureList{Present: true, Malformed: true}
		}
		kind := string(raw[:16])
		for body := raw[28+headerSize : listSize]; len(body) > 0; body = body[signatureSize:] {
			data := body[16:signatureSize]
			switch kind {
			case x509SignatureType:
				sum := sha256.Sum256(data)
				list.Certs = append(list.Certs, TrustedCert{
					Kind:   CertOther,
					SHA256: hex.EncodeToString(sum[:]),
					der:    bytes.Clone(data),
				})
			case sha256SignatureType, x509SHA256SignatureType:
				// An X.509 SHA-256 entry is the hash followed by a
				// 16-byte revocation time.
				if len(data) < sha256.Size {
					return SignatureList{Present: true, Malformed: true}
				}
				sum := hex.EncodeToString(data[:sha256.Size])
				if kind == sha256SignatureType {
					list.FileSums = append(list.FileSums, sum)
				} else {
					list.TBSSums = append(list.TBSSums, sum)
				}
				list.Hashes++
			default:
				list.Other++
			}
		}
		raw = raw[listSize:]
	}
	return list
}

// certificates returns the parsed certificates of a list.
func certificates(list SignatureList) []*x509.Certificate {
	var out []*x509.Certificate
	for _, c := range list.Certs {
		if cert, err := x509.ParseCertificate(c.der); err == nil {
			out = append(out, cert)
		}
	}
	return out
}

// classify names each certificate of a list: known by fingerprint, one of
// the signers or signed by one, else other.
func classify(list *SignatureList, pk []*x509.Certificate) {
	for i := range list.Certs {
		c := &list.Certs[i]
		if name, ok := knownCertNames[c.SHA256]; ok {
			c.Kind, c.Name = CertMicrosoft, name
			continue
		}
		cert, err := x509.ParseCertificate(c.der)
		if err != nil {
			c.Kind, c.Name = CertOther, "certificate that does not parse"
			continue
		}
		c.Name = certName(cert)
		c.TestKey = isTestKey(cert)
		for _, p := range pk {
			// Platform Keys often lack the CA flag, so the signature is
			// checked against the key itself.
			if bytes.Equal(p.Raw, cert.Raw) ||
				p.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil {
				c.Kind = CertPlatform
				break
			}
		}
	}
}

// certName is the certificate's subject, cut to printable text.
func certName(cert *x509.Certificate) string {
	name := cert.Subject.CommonName
	if name == "" {
		name = cert.Subject.String()
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, name)
	if len(name) > 100 {
		name = name[:100]
	}
	if name == "" {
		return "certificate without a name"
	}
	return name
}

// isTestKey reports a firmware test key: the reference firmware kits mark
// them "DO NOT TRUST" or "DO NOT SHIP", and some laptops shipped with them.
func isTestKey(cert *x509.Certificate) bool {
	for _, s := range []string{cert.Subject.String(), cert.Issuer.String()} {
		s = strings.ToUpper(s)
		if strings.Contains(s, "DO NOT TRUST") || strings.Contains(s, "DO NOT SHIP") {
			return true
		}
	}
	return false
}

// sbatLevel is the first line of shim's SBAT level ("sbat,1,2024010900").
func sbatLevel(raw []byte) string {
	line, _, _ := bytes.Cut(raw, []byte("\n"))
	line = bytes.TrimRight(line, "\x00\r")
	if len(line) > 64 {
		line = line[:64]
	}
	for _, b := range line {
		if b < 0x20 || b > 0x7e {
			return "unreadable"
		}
	}
	return string(line)
}

// sbatLines splits shim's SBAT level into its lines; lines that are not
// printable text become "unreadable", and the check fails closed on them.
func sbatLines(raw []byte) []string {
	var out []string
	for _, line := range bytes.Split(bytes.TrimRight(raw, "\x00"), []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		ok := len(line) > 0 && len(line) <= 256
		for _, b := range line {
			ok = ok && b >= 0x20 && b <= 0x7e
		}
		if ok {
			out = append(out, string(line))
		} else if len(line) > 0 {
			out = append(out, "unreadable")
		}
	}
	return out
}

// readAdminPassword reads the firmware setup password state from the
// kernel's firmware-attributes interface (think-lmi, dell-wmi-sysman,
// hp-bioscfg). Without that interface the state is unknown, never "not set".
func readAdminPassword(root string) (PasswordState, error) {
	auths, err := filepath.Glob(filepath.Join(root, "*", "authentication", "*"))
	if err != nil {
		return PasswordUnknown, err
	}
	state := PasswordUnknown
	for _, auth := range auths {
		role, present, err := readCapped(filepath.Join(auth, "role"))
		if err != nil {
			return PasswordUnknown, err
		}
		admin := strings.TrimSpace(string(role)) == "bios-admin"
		if !present {
			admin = filepath.Base(auth) == "Admin"
		}
		if !admin {
			continue
		}
		enabled, present, err := readCapped(filepath.Join(auth, "is_enabled"))
		if err != nil {
			return PasswordUnknown, err
		}
		switch strings.TrimSpace(string(enabled)) {
		case "1":
			return PasswordSet, nil
		case "0":
			if present {
				state = PasswordNotSet
			}
		}
	}
	return state, nil
}

// Has reports whether the list holds the known certificate sum.
func (l SignatureList) Has(sum string) bool {
	for _, c := range l.Certs {
		if c.SHA256 == sum {
			return true
		}
	}
	return false
}

// MayBeLocked reports firmware where installing own keys needs the
// firmware setup, and the setup may have a password the user does not know.
func (t FirmwareTrust) MayBeLocked() bool {
	return t.SetupMode != FlagOn && t.AdminPassword != PasswordNotSet
}

// Findings lists what the user should know, critical ones first.
func (t FirmwareTrust) Findings() []TrustFinding {
	var critical, other []TrustFinding
	add := func(crit bool, text string) {
		if crit {
			critical = append(critical, TrustFinding{true, text})
		} else {
			other = append(other, TrustFinding{false, text})
		}
	}
	for _, c := range t.PK.Certs {
		if c.TestKey {
			add(true, "The Platform Key is a published firmware test key ("+c.Name+"). Anyone can sign startup files this firmware accepts; the maker has to replace it with a firmware update.")
		}
	}
	if t.ShimValidationOff {
		add(true, "Shim's signature check is switched off on this machine (set in MokManager); shim starts anything.")
	}
	for _, l := range []struct {
		name string
		list SignatureList
	}{{"PK", t.PK}, {"KEK", t.KEK}, {"db", t.DB}, {"dbx", t.DBX}} {
		if l.list.Malformed {
			add(true, "The firmware's "+l.name+" list does not read as a signature list.")
		}
	}
	if t.SecureBoot == FlagOn && t.SetupMode == FlagOff && !t.PK.Present {
		add(true, "Secure Boot is on but the firmware has no Platform Key.")
	}

	if t.SetupMode == FlagOn {
		add(false, "The firmware is in Setup Mode: keys can be installed now, without the firmware setup.")
	}
	if t.AuditMode == FlagOn {
		add(false, "The firmware is in Audit Mode: it records failed checks but starts the files anyway.")
	}
	switch {
	case t.KEK.Has(certMSKEK2023):
	case t.KEK.Has(certMSKEK2011):
		add(false, "The KEK holds only Microsoft's 2011 key, which expired in June 2026: newer revocation updates need a KEK update from the maker.")
	}
	if !t.DB.Has(certMSUEFICA2023) && (t.KEK.Has(certMSKEK2011) || t.KEK.Has(certMSKEK2023)) {
		add(false, "db lacks Microsoft UEFI CA 2023; Microsoft's signed db update can add it.")
	}
	if t.DB.Hashes > 0 || t.DB.Other > 0 {
		add(false, fmt.Sprintf("db also trusts %d single files by hash.", t.DB.Hashes+t.DB.Other))
	}
	for _, c := range t.DB.Certs {
		if c.Kind == CertOther {
			add(false, "db also trusts "+c.Name+" (SHA-256 "+c.SHA256[:16]+"...).")
		}
	}
	if t.SecureBoot == FlagOn && t.DBX.Hashes+len(t.DBX.Certs)+t.DBX.Other == 0 && !t.DBX.Malformed {
		add(false, "The revocation list (dbx) is empty: files known to be unsafe are not blocked.")
	}
	for _, c := range t.MokList.Certs {
		add(false, "Shim also trusts "+c.Name+" (added in MokManager).")
	}
	return append(critical, other...)
}

// Lines is the read in plain words, one fact per line.
func (t FirmwareTrust) Lines() []string {
	lines := []string{
		"Secure Boot: " + string(t.SecureBoot) + ", Setup Mode: " + string(t.SetupMode),
		"Firmware setup password: " + string(t.AdminPassword),
	}
	for _, l := range []struct {
		name string
		list SignatureList
	}{{"Platform Key", t.PK}, {"KEK", t.KEK}, {"db", t.DB}} {
		switch {
		case !l.list.Present:
			lines = append(lines, l.name+": empty")
		case l.list.Malformed:
			lines = append(lines, l.name+": unreadable")
		default:
			var names []string
			for _, c := range l.list.Certs {
				name := c.Name
				if c.Kind == CertPlatform && l.name != "Platform Key" {
					name += " (signed by a PK or KEK key)"
				}
				names = append(names, name)
			}
			if n := l.list.Hashes + l.list.Other; n > 0 {
				names = append(names, fmt.Sprintf("%d file hashes", n))
			}
			lines = append(lines, l.name+": "+strings.Join(names, "; "))
		}
	}
	dbx := "empty"
	if t.DBX.Malformed {
		dbx = "unreadable"
	} else if n := t.DBX.Hashes + len(t.DBX.Certs) + t.DBX.Other; n > 0 {
		dbx = fmt.Sprintf("%d entries", n)
		if n == 1 {
			dbx = "1 entry"
		}
	}
	lines = append(lines, "Revocation list (dbx): "+dbx)
	if t.ShimSBATLevel != "" {
		lines = append(lines, "Shim revocation level: "+t.ShimSBATLevel)
	}
	return lines
}

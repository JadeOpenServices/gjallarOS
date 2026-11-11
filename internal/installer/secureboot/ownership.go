package secureboot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const OwnershipPath = "/var/lib/gjallarOS/secure-boot/ownership.json"

type State string

const (
	StateOEMFactoryDerived State = "oem-factory-derived"
	StateGjallarManaged    State = "gjallar-managed"
	// StateGjallarShim: the firmware keeps its maker's keys and starts shim
	// through the UEFI CA; shim checks systemd-boot and the UKIs against
	// the gjallarOS db certificate in MOK (Plane 293).
	StateGjallarShim       State = "gjallar-shim"
	StatePendingEnrollment State = "gjallar-pending-enrollment"
	StateSetupModeUnknown  State = "setup-mode-unknown"
	StateForeignManaged    State = "foreign-managed"
	StateInconsistent      State = "inconsistent"
)

type Inspection struct {
	State      State
	SetupMode  bool
	SecureBoot bool
	Recorded   bool
	OwnerGUID  string
	// Stage is the stage of a valid ownership record ("pending-enrollment",
	// "pending-mok", "enrolled"); empty without one.
	Stage       string
	Description string
}

type ownershipRecord struct {
	Schema         int    `json:"schema"`
	Manager        string `json:"manager"`
	Stage          string `json:"stage"`
	SbctlOwnerGUID string `json:"sbctlOwnerGuid"`
	PKSHA256       string `json:"pkSha256"`
	KEKSHA256      string `json:"kekSha256"`
	DbSHA256       string `json:"dbSha256"`
	UpdatedAt      string `json:"updatedAt"`
}

type localKeys struct {
	ownerGUID string
	pkDER     []byte
	kekDER    []byte
	dbDER     []byte
	pkHash    string
	kekHash   string
	dbHash    string
}

func Inspect(ctx context.Context) (inspection Inspection, err error) {
	setupMode, err := efivarBool("SetupMode")
	if err != nil {
		return Inspection{}, fmt.Errorf("read UEFI SetupMode: %w", err)
	}

	secureBoot, err := efivarBool("SecureBoot")
	if err != nil {
		return Inspection{}, fmt.Errorf("read UEFI SecureBoot: %w", err)
	}

	record, recorded, err := readOwnership(ctx)
	if err != nil {
		return Inspection{}, err
	}

	keys, haveKeys, err := readLocalKeys(ctx)
	if err != nil {
		return Inspection{}, err
	}

	recordValid := false
	if recorded && haveKeys {
		recordValid =
			record.Schema == 1 &&
				record.Manager == "gjallarOS" &&
				record.SbctlOwnerGUID == keys.ownerGUID &&
				record.PKSHA256 == keys.pkHash &&
				record.KEKSHA256 == keys.kekHash &&
				record.DbSHA256 == keys.dbHash
	}
	if recordValid {
		defer func() {
			if err == nil {
				inspection.Stage = record.Stage
			}
		}()
	}

	if recorded && !recordValid {
		return Inspection{
			State:       StateInconsistent,
			SetupMode:   setupMode,
			SecureBoot:  secureBoot,
			Recorded:    true,
			Description: "gjallarOS ownership metadata exists but does not match the current local sbctl key set.",
		}, nil
	}

	if setupMode {
		if recordValid {
			return Inspection{
				State:       StatePendingEnrollment,
				SetupMode:   true,
				SecureBoot:  secureBoot,
				Recorded:    true,
				OwnerGUID:   keys.ownerGUID,
				Description: "Firmware is in Setup Mode and the pending gjallarOS ownership record matches the local key set.",
			}, nil
		}

		return Inspection{
			State:       StateSetupModeUnknown,
			SetupMode:   true,
			SecureBoot:  secureBoot,
			Recorded:    recorded,
			Description: "Firmware is already in Setup Mode without a verified gjallarOS pending-ownership record.",
		}, nil
	}

	pk, pkPresent, err := efivarPayload("PK")
	if err != nil {
		return Inspection{}, err
	}
	kek, _, err := efivarPayload("KEK")
	if err != nil {
		return Inspection{}, err
	}
	db, _, err := efivarPayload("db")
	if err != nil {
		return Inspection{}, err
	}

	// A legacy gjallarOS/sbctl installation may predate ownership.json.
	// If the exact local PK, KEK and db certificates are already enrolled,
	// the machine is using this local sbctl key hierarchy and can be adopted
	// into the explicit gjallarOS ownership record without rotating keys.
	if haveKeys &&
		pkPresent &&
		bytes.Contains(pk, keys.pkDER) &&
		bytes.Contains(kek, keys.kekDER) &&
		bytes.Contains(db, keys.dbDER) {
		return Inspection{
			State:       StateGjallarManaged,
			SetupMode:   false,
			SecureBoot:  secureBoot,
			Recorded:    recorded,
			OwnerGUID:   keys.ownerGUID,
			Description: "The active PK/KEK/db contain the current gjallarOS sbctl certificates.",
		}, nil
	}

	// Booted through shim: only this boot tells, because shim leaves its key
	// list for the system it started and nothing else does.
	if recordValid && secureBoot {
		viaShim, err := liveFirmware.shimTrustsKey(keys.dbDER)
		if err != nil {
			return Inspection{}, err
		}
		if viaShim {
			return Inspection{
				State:       StateGjallarShim,
				SetupMode:   false,
				SecureBoot:  true,
				Recorded:    true,
				OwnerGUID:   keys.ownerGUID,
				Description: "Secure Boot is on with the firmware maker's keys; shim started this boot and checks it against the current gjallarOS db certificate in MOK.",
			}, nil
		}
	}

	// A pending record is valid outside Setup Mode only while the firmware is
	// still rooted in its factory Platform Key.
	if recordValid {
		if !pkPresent {
			return Inspection{
				State:       StateInconsistent,
				SetupMode:   false,
				SecureBoot:  secureBoot,
				Recorded:    true,
				OwnerGUID:   keys.ownerGUID,
				Description: "gjallarOS ownership metadata is valid, but the active PK is unexpectedly absent outside Setup Mode.",
			}, nil
		}

		pkDefault, defaultPresent, err := efivarPayload("PKDefault")
		if err != nil {
			return Inspection{}, err
		}

		if defaultPresent && samePlatformKey(pk, pkDefault) {
			return Inspection{
				State:       StatePendingEnrollment,
				SetupMode:   false,
				SecureBoot:  secureBoot,
				Recorded:    true,
				OwnerGUID:   keys.ownerGUID,
				Description: "gjallarOS keys are prepared and recorded while the firmware still uses its factory Platform Key.",
			}, nil
		}

		return Inspection{
			State:       StateInconsistent,
			SetupMode:   false,
			SecureBoot:  secureBoot,
			Recorded:    true,
			OwnerGUID:   keys.ownerGUID,
			Description: "gjallarOS ownership metadata is valid, but the active Platform Key is neither the current gjallarOS PK nor the firmware PKDefault.",
		}, nil
	}

	// The PK is the root of the Secure Boot hierarchy. Firmware-provided
	// may legitimately update KEK/db/dbx over time, so factory-derived state
	// is deliberately based on the active PK matching PKDefault rather than
	// requiring byte-identical KEK/db databases.
	if pkPresent {
		pkDefault, defaultPresent, err := efivarPayload("PKDefault")
		if err != nil {
			return Inspection{}, err
		}
		if defaultPresent && samePlatformKey(pk, pkDefault) {
			return Inspection{
				State:       StateOEMFactoryDerived,
				SetupMode:   false,
				SecureBoot:  secureBoot,
				Recorded:    false,
				Description: "The active Platform Key certificate matches the firmware PKDefault; KEK/db updates do not change OEM-root classification.",
			}, nil
		}
	}

	return Inspection{
		State:       StateForeignManaged,
		SetupMode:   false,
		SecureBoot:  secureBoot,
		Recorded:    recorded,
		Description: "The active Secure Boot hierarchy is neither the firmware default PK nor the current gjallarOS key hierarchy.",
	}, nil
}

func verifyOwnershipRecord(
	record ownershipRecord,
	keys localKeys,
	expectedStage string,
) error {
	if record.Schema != 1 {
		return fmt.Errorf(
			"unsupported gjallarOS Secure Boot ownership schema %d",
			record.Schema,
		)
	}

	if record.Manager != "gjallarOS" {
		return fmt.Errorf(
			"unexpected Secure Boot manager %q",
			record.Manager,
		)
	}

	if expectedStage != "" && record.Stage != expectedStage {
		return fmt.Errorf(
			"ownership stage is %q; expected %q",
			record.Stage,
			expectedStage,
		)
	}

	if record.SbctlOwnerGUID != keys.ownerGUID {
		return fmt.Errorf(
			"ownership record GUID does not match the current sbctl GUID",
		)
	}

	for name, pair := range map[string][2]string{
		"PK":  {record.PKSHA256, keys.pkHash},
		"KEK": {record.KEKSHA256, keys.kekHash},
		"db":  {record.DbSHA256, keys.dbHash},
	} {
		if pair[0] != pair[1] {
			return fmt.Errorf(
				"local %s certificate does not match gjallarOS ownership metadata",
				name,
			)
		}
	}

	return nil
}

// VerifyOwnership checks that the running boot was verified against the
// current gjallarOS keys. viaShim is set on a system built to boot through
// shim; only there does a shim boot count. A system set to its own firmware
// keys that comes up through shim had its keys replaced, so it fails.
func VerifyOwnership(ctx context.Context, expectedStage string, viaShim bool) error {
	record, recorded, err := readOwnership(ctx)
	if err != nil {
		return fmt.Errorf("read gjallarOS Secure Boot ownership metadata: %w", err)
	}
	if !recorded {
		return fmt.Errorf("gjallarOS Secure Boot ownership metadata is missing")
	}

	keys, haveKeys, err := readLocalKeys(ctx)
	if err != nil {
		return fmt.Errorf("read local Secure Boot key set: %w", err)
	}
	if !haveKeys {
		return fmt.Errorf("local sbctl Secure Boot key set is incomplete")
	}

	if err := verifyOwnershipRecord(record, keys, expectedStage); err != nil {
		return err
	}

	inspection, err := Inspect(ctx)
	if err != nil {
		return fmt.Errorf("inspect active Secure Boot ownership: %w", err)
	}

	return acceptBoot(inspection, viaShim)
}

// acceptBoot is the verdict on how this boot was checked.
func acceptBoot(inspection Inspection, viaShim bool) error {
	switch {
	case inspection.State == StateGjallarManaged:
		return nil
	case inspection.State == StateGjallarShim && viaShim:
		return nil
	case inspection.State == StateGjallarShim:
		return fmt.Errorf(
			"this boot came through shim, but the system is set to its own firmware keys: %s",
			inspection.Description,
		)
	}
	return fmt.Errorf(
		"active firmware does not contain the current gjallarOS PK/KEK/db hierarchy: %s",
		inspection.Description,
	)
}

func RecordOwnership(ctx context.Context, stage string) error {
	keys, ok, err := readLocalKeys(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cannot record gjallarOS Secure Boot ownership: sbctl keys are incomplete")
	}

	rec := ownershipRecord{
		Schema:         1,
		Manager:        "gjallarOS",
		Stage:          stage,
		SbctlOwnerGUID: keys.ownerGUID,
		PKSHA256:       keys.pkHash,
		KEKSHA256:      keys.kekHash,
		DbSHA256:       keys.dbHash,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp("", "gjallar-secure-boot-ownership-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := command(ctx, "sudo", "install", "-d", "-m", "0700",
		filepath.Dir(OwnershipPath)); err != nil {
		return fmt.Errorf("create Secure Boot ownership directory: %w", err)
	}
	if err := command(ctx, "sudo", "install", "-m", "0600",
		tmpPath, OwnershipPath); err != nil {
		return fmt.Errorf("install Secure Boot ownership record: %w", err)
	}

	return nil
}

func readOwnership(ctx context.Context) (ownershipRecord, bool, error) {
	ok, err := sudoTestFile(ctx, OwnershipPath)
	if err != nil {
		return ownershipRecord{}, false, err
	}
	if !ok {
		return ownershipRecord{}, false, nil
	}

	data, err := sudoReadFile(ctx, OwnershipPath)
	if err != nil {
		return ownershipRecord{}, false, fmt.Errorf("read Secure Boot ownership record: %w", err)
	}

	var rec ownershipRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return ownershipRecord{}, false, fmt.Errorf("parse Secure Boot ownership record: %w", err)
	}
	return rec, true, nil
}

func readLocalKeys(ctx context.Context) (localKeys, bool, error) {
	paths := map[string]string{
		"PK":  "/var/lib/sbctl/keys/PK/PK.pem",
		"KEK": "/var/lib/sbctl/keys/KEK/KEK.pem",
		"db":  "/var/lib/sbctl/keys/db/db.pem",
	}

	for _, p := range paths {
		ok, err := sudoTestFile(ctx, p)
		if err != nil {
			return localKeys{}, false, err
		}
		if !ok {
			return localKeys{}, false, nil
		}
	}

	guidBytes, err := sudoReadFile(ctx, "/var/lib/sbctl/GUID")
	if err != nil {
		return localKeys{}, false, fmt.Errorf("read sbctl owner GUID: %w", err)
	}

	pkDER, pkHash, err := readPEMCertificate(ctx, paths["PK"])
	if err != nil {
		return localKeys{}, false, err
	}
	kekDER, kekHash, err := readPEMCertificate(ctx, paths["KEK"])
	if err != nil {
		return localKeys{}, false, err
	}
	dbDER, dbHash, err := readPEMCertificate(ctx, paths["db"])
	if err != nil {
		return localKeys{}, false, err
	}

	return localKeys{
		ownerGUID: strings.TrimSpace(string(guidBytes)),
		pkDER:     pkDER,
		kekDER:    kekDER,
		dbDER:     dbDER,
		pkHash:    pkHash,
		kekHash:   kekHash,
		dbHash:    dbHash,
	}, true, nil
}

func readPEMCertificate(ctx context.Context, path string) ([]byte, string, error) {
	data, err := sudoReadFile(ctx, path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, "", fmt.Errorf("%s does not contain an X.509 CERTIFICATE PEM block", path)
	}
	sum := sha256.Sum256(block.Bytes)
	return block.Bytes, hex.EncodeToString(sum[:]), nil
}

// FirmwareSecureBoot reports the UEFI SecureBoot variable: whether the
// firmware enforces Secure Boot on this boot. Firmware without the variable
// has no Secure Boot support and enforces nothing.
func FirmwareSecureBoot() (bool, error) {
	if _, present, err := efivarPayload("SecureBoot"); err == nil && !present {
		return false, nil
	}
	return efivarBool("SecureBoot")
}

// FirmwareHasSecureBoot reports whether the firmware offers Secure Boot at
// all. UEFI firmware with Secure Boot keeps the SecureBoot and SetupMode
// variables; with neither there is nothing to set up (plain OVMF, or a
// system not started through UEFI). One without the other is not this case
// and Inspect reports it.
func FirmwareHasSecureBoot() (bool, error) {
	for _, name := range []string{"SecureBoot", "SetupMode"} {
		_, present, err := efivarPayload(name)
		if err != nil || present {
			return present, err
		}
	}
	return false, nil
}

func efivarBool(name string) (bool, error) {
	payload, present, err := efivarPayload(name)
	if err != nil {
		return false, err
	}
	if !present || len(payload) < 1 {
		return false, fmt.Errorf("%s EFI variable is missing", name)
	}
	switch payload[0] {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("%s has unexpected value %d", name, payload[0])
	}
}

// samePlatformKey reports whether two EFI signature lists hold the same
// signatures. Signature owner GUIDs are ignored: they only label who added an
// entry, and firmware may store PKDefault under a different owner than the
// enrolled PK (seen on the Framework factory variables). Lists that do not
// parse are compared byte for byte.
func samePlatformKey(a, b []byte) bool {
	sa, okA := signatureEntries(a)
	sb, okB := signatureEntries(b)
	if !okA || !okB {
		return bytes.Equal(a, b)
	}
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// signatureEntries returns each signature of a sequence of
// EFI_SIGNATURE_LISTs as its type GUID followed by its data, without owner.
func signatureEntries(raw []byte) ([]string, bool) {
	var entries []string
	for len(raw) > 0 {
		if len(raw) < 28 {
			return nil, false
		}
		listSize := int(binary.LittleEndian.Uint32(raw[16:20]))
		headerSize := int(binary.LittleEndian.Uint32(raw[20:24]))
		signatureSize := int(binary.LittleEndian.Uint32(raw[24:28]))
		if listSize < 28 || listSize > len(raw) || headerSize < 0 || signatureSize <= 16 ||
			28+headerSize > listSize || (listSize-28-headerSize)%signatureSize != 0 {
			return nil, false
		}
		kind := string(raw[:16])
		for body := raw[28+headerSize : listSize]; len(body) > 0; body = body[signatureSize:] {
			entries = append(entries, kind+string(body[16:signatureSize]))
		}
		raw = raw[listSize:]
	}
	return entries, len(entries) > 0
}

// efivarsDir is where efivarfs is mounted; tests point it elsewhere.
var efivarsDir = "/sys/firmware/efi/efivars"

func efivarPayload(name string) ([]byte, bool, error) {
	matches, err := filepath.Glob(filepath.Join(efivarsDir, name+"-*"))
	if err != nil {
		return nil, false, err
	}
	if len(matches) == 0 {
		return nil, false, nil
	}
	if len(matches) != 1 {
		return nil, false, fmt.Errorf("expected one %s EFI variable, found %d", name, len(matches))
	}

	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", matches[0], err)
	}
	if len(raw) < 4 {
		return nil, false, fmt.Errorf("%s is shorter than the efivarfs attribute header", matches[0])
	}
	return raw[4:], true, nil
}

func sudoTestFile(ctx context.Context, path string) (bool, error) {
	cmd := commandContext(ctx, "sudo", "test", "-f", path)
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("sudo test -f %s: %w", path, err)
}

func sudoReadFile(ctx context.Context, path string) ([]byte, error) {
	cmd := commandContext(ctx, "sudo", "cat", "--", path)
	cmd.Stdin = os.Stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func command(ctx context.Context, name string, args ...string) error {
	cmd := commandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// shimTrustsKey tells whether the shim that started this boot checks loaders
// against the certificate der from its key list. A shim whose own check was
// switched off in MokManager starts anything, so it trusts nothing here.
func (f firmwareFiles) shimTrustsKey(der []byte) (bool, error) {
	state, present, err := f.shimVar("MokSBStateRT")
	if err != nil {
		return false, err
	}
	if present && (len(state) != 1 || state[0] != 0) {
		return false, nil
	}
	mok, present, err := f.shimVar("MokListRT")
	if err != nil || !present {
		return false, err
	}
	list := parseSignatureList(mok, true)
	if list.Malformed {
		return false, nil
	}
	for _, c := range list.Certs {
		if bytes.Equal(c.der, der) {
			return true, nil
		}
	}
	return false, nil
}

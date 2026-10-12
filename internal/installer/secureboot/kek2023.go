package secureboot

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Microsoft's KEK CA 2011 expired on 2026-06-24; new db and dbx updates are
// signed by its successor, "Microsoft Corporation KEK 2K CA 2023". Vendors
// ship that KEK as an update signed with their own Platform Key, which can
// never apply once GjallarOS owns the PK. A machine whose built-in KEK only
// knows the 2011 CA would stop taking revocation updates for good, so
// enrollment adds the 2023 CA next to the preserved built-in KEK.
//
// The certificate is the one sbctl 0.18 ships (microsoft/KEK); its SHA-1
// thumbprint 459ab6fb5e284d272d5e3e6abc8ed663829d632b is the one Microsoft
// publishes.
//
//go:embed microsoft-kek-2k-ca-2023.der
var microsoftKEK2023 []byte

const (
	microsoftKEK2023SHA256 = "3cd3f0309edae228767a976dd40d9f4affc4fbd5218f2e8cc3c9dd97e8ac6f9d"
	microsoftKEK2011SHA256 = "a1117f516a32cefcba3f2d1ace10a87972fd6bbe8fe0d0b996e09e65d802a503"

	// sbctl enroll-keys --custom adds every certificate under custom/KEK
	// and custom/db.
	sbctlCustomDir       = "/var/lib/sbctl/keys/custom"
	microsoftKEK2023File = "microsoft-kek-2k-ca-2023.der"
)

// EFI_CERT_X509_GUID a5c059a1-94e4-4aa7-87b5-ab155c2bf072 as stored.
var x509SignatureType = string([]byte{
	0xa1, 0x59, 0xc0, 0xa5, 0xe4, 0x94, 0xa7, 0x4a,
	0x87, 0xb5, 0xab, 0x15, 0x5c, 0x2b, 0xf0, 0x72,
})

// needsMicrosoftKEK2023 reports whether the built-in KEK that the policy
// preserves trusts Microsoft's 2011 KEK CA but not the 2023 one. Firmware
// without the 2011 CA never trusted Microsoft and gets nothing added.
func needsMicrosoftKEK2023(snapshot FirmwarePolicySnapshot, ops enrollmentOps) (bool, error) {
	if !slices.Contains(snapshot.PreserveFirmwareBuiltin, "KEK") {
		return false, nil
	}
	builtin, present, err := ops.payload("KEKDefault")
	if err != nil {
		return false, fmt.Errorf("read EFI variable KEKDefault: %w", err)
	}
	if !present {
		return false, nil
	}
	entries, ok := signatureEntries(builtin)
	if !ok {
		return false, nil
	}
	has2011 := false
	for _, entry := range entries {
		if entry[:16] != x509SignatureType {
			continue
		}
		sum := sha256.Sum256([]byte(entry[16:]))
		switch hex.EncodeToString(sum[:]) {
		case microsoftKEK2023SHA256:
			return false, nil
		case microsoftKEK2011SHA256:
			has2011 = true
		}
	}
	return has2011, nil
}

// stageMicrosoftKEK2023 puts the 2023 KEK CA where sbctl --custom reads it.
// Any other file in that folder would be enrolled too, so there must be none.
func stageMicrosoftKEK2023(der []byte) error {
	return stageCustomKEK(sbctlCustomDir, der)
}

func stageCustomKEK(customDir string, der []byte) error {
	kekDir := filepath.Join(customDir, "KEK")
	target := filepath.Join(kekDir, microsoftKEK2023File)
	err := filepath.WalkDir(customDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path == target {
			return nil
		}
		return fmt.Errorf(
			"%s holds %s, which sbctl would enroll as well; refusing Secure Boot enrollment",
			customDir,
			path,
		)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(kekDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(target, der, 0o600)
}

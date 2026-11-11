package secureboot

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Which Secure Boot path a machine takes (Plane 293, wiki 951, Jade Q189):
// the maker's keys with shim and our key in MOK wherever the firmware
// starts the pinned shim, our own firmware keys when Setup Mode is already
// on or the user asks for them.

// ShimFactsPath holds what the pinned shim needs from the firmware, written
// at build time by pkgs/gjallar-shim from the signed files themselves.
const ShimFactsPath = "/etc/gjallar/shim.json"

// ShimFacts are the build-time facts about the pinned shim. All sums are
// lower-case hex SHA-256: certificates over their DER, "TBS" over their
// signed part (what an X.509 SHA-256 dbx entry holds), Authenticode over
// the image as the firmware hashes it.
type ShimFacts struct {
	Schema int `json:"schema"`
	// SignerCAs are the roots shim's signatures chain to; db must hold one.
	SignerCAs []string `json:"signerCAs"`
	// ChainCerts and ChainTBS cover every certificate in every signature
	// chain: one of them in dbx makes the firmware refuse shim, even when
	// another signature is good.
	ChainCerts []string `json:"chainCerts"`
	ChainTBS   []string `json:"chainTBS"`
	// VendorCA is the distro key built into shim; it signs MokManager and
	// the fallback loader.
	VendorCA     string            `json:"vendorCA"`
	VendorCATBS  string            `json:"vendorCATBS"`
	Authenticode map[string]string `json:"authenticode"`
	SBAT         []string          `json:"sbat"`
}

// LoadShimFacts reads the facts file of this system.
func LoadShimFacts(path string) (ShimFacts, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ShimFacts{}, fmt.Errorf("read shim facts: %w", err)
	}
	return parseShimFacts(raw)
}

func parseShimFacts(raw []byte) (ShimFacts, error) {
	var facts ShimFacts
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&facts); err != nil {
		return ShimFacts{}, fmt.Errorf("parse shim facts: %w", err)
	}
	if decoder.More() {
		return ShimFacts{}, errors.New("parse shim facts: trailing data")
	}
	if facts.Schema != 1 {
		return ShimFacts{}, fmt.Errorf("shim facts schema %d is not supported", facts.Schema)
	}
	sums := map[string][]string{
		"signerCAs":   facts.SignerCAs,
		"chainCerts":  facts.ChainCerts,
		"chainTBS":    facts.ChainTBS,
		"vendorCA":    {facts.VendorCA},
		"vendorCATBS": {facts.VendorCATBS},
		// Listed even when empty: no file sums means no dbx check.
		"authenticode": nil,
	}
	for _, sum := range facts.Authenticode {
		sums["authenticode"] = append(sums["authenticode"], sum)
	}
	for name, list := range sums {
		if len(list) == 0 {
			return ShimFacts{}, fmt.Errorf("shim facts have no %s", name)
		}
		for _, sum := range list {
			if !isSHA256Hex(sum) {
				return ShimFacts{}, fmt.Errorf("shim facts %s holds %q, not a SHA-256", name, sum)
			}
		}
	}
	if len(facts.SBAT) == 0 {
		return ShimFacts{}, errors.New("shim facts have no SBAT lines")
	}
	if _, err := sbatGenerations(facts.SBAT); err != nil {
		return ShimFacts{}, fmt.Errorf("shim facts: %w", err)
	}
	return facts, nil
}

func isSHA256Hex(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Path is the chosen Secure Boot path.
type Path string

const (
	PathShim    Path = "shim"
	PathOwnKeys Path = "own-keys"
)

// PathChoice is the verdict and its reason, said to the user.
type PathChoice struct {
	Path   Path
	Reason string
}

// ChoosePath picks the default path for this firmware. ourKey is the DER of
// the gjallarOS db certificate that goes into MOK, nil when it does not
// exist yet. An error means neither path can be shown safe: a variable did
// not parse.
func ChoosePath(trust FirmwareTrust, facts ShimFacts, ourKey []byte) (PathChoice, error) {
	if trust.SetupMode == FlagOn {
		return PathChoice{PathOwnKeys, "the firmware is in Setup Mode, so it takes our own keys without its setup screens"}, nil
	}
	if trust.DB.Malformed {
		return PathChoice{}, errors.New("the firmware db does not parse")
	}
	if trust.DBX.Malformed {
		return PathChoice{}, errors.New("the firmware dbx does not parse")
	}
	if blocked := shimBlocked(trust, facts, ourKey); blocked != "" {
		return PathChoice{PathOwnKeys, blocked}, nil
	}
	return PathChoice{PathShim, "the firmware trusts the signer of the pinned shim and does not revoke it"}, nil
}

// shimBlocked names why the firmware would not start the pinned shim, its
// MokManager or a loader signed with ourKey; "" when nothing does. It follows
// the firmware's own rule: any certificate of any signature chain in dbx,
// by DER or by signed-part hash, rejects the image.
func shimBlocked(trust FirmwareTrust, facts ShimFacts, ourKey []byte) string {
	trusted := false
	for _, ca := range facts.SignerCAs {
		if trust.DB.Has(ca) {
			trusted = true
			break
		}
	}
	if !trusted {
		return "the firmware db does not hold the certificate that signed the pinned shim"
	}
	dbx := trust.DBX
	if dbx.Other > 0 {
		return "the firmware dbx holds entry types this check cannot compare"
	}
	revokedCert := func(sum string) bool { return dbx.Has(sum) }
	revokedTBS := func(sum string) bool { return slices.Contains(dbx.TBSSums, sum) }
	for _, sum := range facts.ChainCerts {
		if revokedCert(sum) {
			return "the firmware dbx revokes a certificate in the pinned shim's signature"
		}
	}
	for _, sum := range facts.ChainTBS {
		if revokedTBS(sum) {
			return "the firmware dbx revokes a certificate in the pinned shim's signature"
		}
	}
	if revokedCert(facts.VendorCA) || revokedTBS(facts.VendorCATBS) {
		return "the firmware dbx revokes the distro key that signs MokManager"
	}
	for name, sum := range facts.Authenticode {
		if slices.Contains(dbx.FileSums, sum) {
			return fmt.Sprintf("the firmware dbx revokes %s of the pinned shim", name)
		}
	}
	if len(ourKey) > 0 {
		sum := sha256.Sum256(ourKey)
		if revokedCert(hex.EncodeToString(sum[:])) {
			return "the firmware dbx revokes the gjallarOS key"
		}
		if cert, err := x509.ParseCertificate(ourKey); err == nil {
			tbs := sha256.Sum256(cert.RawTBSCertificate)
			if revokedTBS(hex.EncodeToString(tbs[:])) {
				return "the firmware dbx revokes the gjallarOS key"
			}
		}
	}
	if len(trust.ShimSBAT) > 0 {
		if why := sbatRevokes(trust.ShimSBAT, facts.SBAT); why != "" {
			return why
		}
	}
	return ""
}

// sbatRevokes compares shim's SBAT level, as the last shim boot left it,
// with the pinned shim's own SBAT section. A level line names a component
// and the lowest generation still allowed.
func sbatRevokes(level, ours []string) string {
	mine, err := sbatGenerations(ours)
	if err != nil {
		return "the pinned shim's SBAT section does not parse"
	}
	for _, line := range level {
		if line == "unreadable" {
			return "shim's SBAT revocation level holds a line that is not text"
		}
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			return "shim's SBAT revocation level holds a line without a generation"
		}
		gen, err := strconv.Atoi(fields[1])
		if err != nil || gen < 0 {
			return "shim's SBAT revocation level holds a line without a generation"
		}
		if have, ok := mine[fields[0]]; ok && have < gen {
			return fmt.Sprintf("shim's SBAT revocation level needs %s generation %d; the pinned shim has %d", fields[0], gen, have)
		}
	}
	return ""
}

func sbatGenerations(lines []string) (map[string]int, error) {
	out := make(map[string]int, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, ",")
		if len(fields) < 2 || fields[0] == "" {
			return nil, fmt.Errorf("SBAT line %q has no component and generation", line)
		}
		gen, err := strconv.Atoi(fields[1])
		if err != nil || gen < 0 {
			return nil, fmt.Errorf("SBAT line %q has no generation", line)
		}
		out[fields[0]] = gen
	}
	return out, nil
}

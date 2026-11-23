// Package bootnext finds the firmware entries that start an installed
// gjallarOS and sets BootNext to one of them (Q228 item 4). After an install
// from the stick, the next start goes to the new system even with the stick
// still in; the stick menu uses the same search for "Start installed
// gjallarOS".
//
// Both callers run as root. Everything here comes from efibootmgr and lsblk,
// never from a path a user can change, and the only write is the BootNext
// variable: it picks one firmware entry that already exists, which is no
// more than the firmware's own boot menu offers. An entry counts only when
// its whole device path is the gjallarOS loader on an ESP of a disk that also
// carries the GJALLAROS root partition, so a stick, a foreign disk or an
// entry with extra load options (shim reads those as a loader path) never
// matches.
package bootnext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Runner runs a program and returns what it printed on stdout.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Install is one firmware entry that starts an installed gjallarOS.
type Install struct {
	// Entry is the boot entry number, 4 hex digits as efibootmgr prints it.
	Entry string
	// Disk is the whole disk the entry's ESP is on.
	Disk string
}

const (
	partTypeESP = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	// rootPartLabel is the PARTLABEL freshdiskplan gives the root partition.
	rootPartLabel = "GJALLAROS"
	// maxOutput caps what is parsed from either tool.
	maxOutput = 1 << 20
)

// loaders are the paths boot-first.sh registers, lower case.
var loaders = []string{`\efi\systemd\systemd-bootx64.efi`, `\efi\nixos\shimx64.efi`}

var (
	entryLine = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})(\*?) ([^\t]*)\t(.*)$`)
	orderLine = regexp.MustCompile(`^BootOrder: ([0-9A-Fa-f]{4}(?:,[0-9A-Fa-f]{4})*)$`)
	nextLine  = regexp.MustCompile(`^BootNext: ([0-9A-Fa-f]{4})$`)
	hdPath    = regexp.MustCompile(`^hd\([0-9a-f]+,gpt,([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}),0x[0-9a-f]+,0x[0-9a-f]+\)/(.*)$`)
	entryNum  = regexp.MustCompile(`^[0-9A-Fa-f]{4}$`)
)

type blockDevice struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	PartUUID  string `json:"partuuid"`
	PartLabel string `json:"partlabel"`
	PartType  string `json:"parttype"`
	PKName    string `json:"pkname"`
}

// Find lists the active firmware entries that start an installed gjallarOS,
// in BootOrder first, the rest by number.
func Find(ctx context.Context, r Runner) ([]Install, error) {
	esps, err := gjallarESPs(ctx, r)
	if err != nil {
		return nil, err
	}
	if len(esps) == 0 {
		return nil, nil
	}
	raw, err := r.Output(ctx, "efibootmgr", "-v")
	if err != nil {
		return nil, fmt.Errorf("read boot entries: %w", err)
	}
	if len(raw) > maxOutput {
		return nil, errors.New("efibootmgr printed too much")
	}
	return parseEntries(raw, esps), nil
}

// gjallarESPs maps the PARTUUID of each ESP on a disk with a GJALLAROS root
// partition to that disk. A PARTUUID seen twice (a cloned disk) is left out:
// the firmware entry could start either.
func gjallarESPs(ctx context.Context, r Runner) (map[string]string, error) {
	raw, err := r.Output(ctx, "lsblk", "-J", "-l", "-p", "-o", "PATH,TYPE,PARTUUID,PARTLABEL,PARTTYPE,PKNAME")
	if err != nil {
		return nil, fmt.Errorf("list disks: %w", err)
	}
	if len(raw) > maxOutput {
		return nil, errors.New("lsblk printed too much")
	}
	var out struct {
		BlockDevices []blockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode lsblk: %w", err)
	}
	hasRoot := map[string]bool{}
	seen := map[string]int{}
	for _, d := range out.BlockDevices {
		if d.Type != "part" {
			continue
		}
		if d.PartLabel == rootPartLabel && d.PKName != "" {
			hasRoot[d.PKName] = true
		}
		if u := strings.ToLower(d.PartUUID); u != "" {
			seen[u]++
		}
	}
	esps := map[string]string{}
	for _, d := range out.BlockDevices {
		u := strings.ToLower(d.PartUUID)
		if d.Type != "part" || u == "" || seen[u] != 1 || strings.ToLower(d.PartType) != partTypeESP || !hasRoot[d.PKName] {
			continue
		}
		esps[u] = d.PKName
	}
	return esps, nil
}

func parseEntries(raw []byte, esps map[string]string) []Install {
	var order []string
	var found []Install
	for _, line := range strings.Split(string(bytes.TrimRight(raw, "\n")), "\n") {
		if m := orderLine.FindStringSubmatch(line); m != nil {
			order = strings.Split(strings.ToUpper(m[1]), ",")
			continue
		}
		m := entryLine.FindStringSubmatch(line)
		if m == nil || m[2] != "*" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(m[3]), "gjallaros recovery") {
			continue
		}
		disk, ok := matchPath(strings.ToLower(m[4]), esps)
		if !ok {
			continue
		}
		found = append(found, Install{Entry: strings.ToUpper(m[1]), Disk: disk})
	}
	rank := map[string]int{}
	for i, e := range order {
		if _, dup := rank[e]; !dup {
			rank[e] = i
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		ri, iok := rank[found[i].Entry]
		rj, jok := rank[found[j].Entry]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		ni, _ := strconv.ParseUint(found[i].Entry, 16, 16)
		nj, _ := strconv.ParseUint(found[j].Entry, 16, 16)
		return ni < nj
	})
	return found
}

// matchPath takes the whole device path: the ESP, then the loader, nothing
// after it.
func matchPath(path string, esps map[string]string) (string, bool) {
	m := hdPath.FindStringSubmatch(path)
	if m == nil {
		return "", false
	}
	disk, ok := esps[m[1]]
	if !ok {
		return "", false
	}
	for _, l := range loaders {
		if m[2] == "file("+l+")" || m[2] == l {
			return disk, true
		}
	}
	return "", false
}

// Set makes entry the next start and reads it back.
func Set(ctx context.Context, r Runner, entry string) error {
	if !entryNum.MatchString(entry) {
		return fmt.Errorf("boot entry %q is not 4 hex digits", entry)
	}
	entry = strings.ToUpper(entry)
	if _, err := r.Output(ctx, "efibootmgr", "--bootnext", entry); err != nil {
		return fmt.Errorf("set BootNext to %s: %w", entry, err)
	}
	raw, err := r.Output(ctx, "efibootmgr")
	if err != nil {
		return fmt.Errorf("read BootNext back: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := nextLine.FindStringSubmatch(line); m != nil && strings.EqualFold(m[1], entry) {
			return nil
		}
	}
	return fmt.Errorf("BootNext is not %s after setting it", entry)
}

var partUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// RemoveForESP deletes every firmware entry, active or not, whose device
// path starts on the ESP with this PARTUUID, and clears BootNext when it
// pointed at one of them. The fresh installer calls it after a failed run
// (Q257, friend's T480s, .68, 2026-11-22): that run made the PARTUUID at
// random for its new ESP, so no entry of another system carries it.
// efibootmgr also drops a deleted entry from BootOrder. It returns the
// entries it deleted and reads the list back to check that none is left.
func RemoveForESP(ctx context.Context, r Runner, partUUID string) ([]string, error) {
	u := strings.ToLower(partUUID)
	if !partUUIDRe.MatchString(u) {
		return nil, fmt.Errorf("ESP PARTUUID %q is not a UUID", partUUID)
	}
	raw, err := readEntries(ctx, r)
	if err != nil {
		return nil, err
	}
	entries, next := entriesOnESP(raw, u)
	var removed []string
	for _, e := range entries {
		if _, err := r.Output(ctx, "efibootmgr", "--quiet", "--bootnum", e, "--delete-bootnum"); err != nil {
			return removed, fmt.Errorf("delete boot entry %s: %w", e, err)
		}
		removed = append(removed, e)
	}
	if next != "" && slices.Contains(entries, next) {
		if _, err := r.Output(ctx, "efibootmgr", "--quiet", "--delete-bootnext"); err != nil {
			return removed, fmt.Errorf("clear BootNext %s: %w", next, err)
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}
	raw, err = readEntries(ctx, r)
	if err != nil {
		return removed, err
	}
	if left, _ := entriesOnESP(raw, u); len(left) > 0 {
		return removed, fmt.Errorf("boot entries %s are still there after deleting them", strings.Join(left, ", "))
	}
	return removed, nil
}

func readEntries(ctx context.Context, r Runner) ([]byte, error) {
	raw, err := r.Output(ctx, "efibootmgr", "-v")
	if err != nil {
		return nil, fmt.Errorf("read boot entries: %w", err)
	}
	if len(raw) > maxOutput {
		return nil, errors.New("efibootmgr printed too much")
	}
	return raw, nil
}

// entriesOnESP lists the entries whose device path starts on the ESP with
// PARTUUID u, whatever loader they name, and the BootNext value.
func entriesOnESP(raw []byte, u string) (entries []string, next string) {
	for _, line := range strings.Split(string(bytes.TrimRight(raw, "\n")), "\n") {
		if m := nextLine.FindStringSubmatch(line); m != nil {
			next = strings.ToUpper(m[1])
			continue
		}
		m := entryLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		p := hdPath.FindStringSubmatch(strings.ToLower(m[4]))
		if p == nil || p[1] != u {
			continue
		}
		e := strings.ToUpper(m[1])
		if !slices.Contains(entries, e) {
			entries = append(entries, e)
		}
	}
	return entries, next
}

// FindESP returns the device of the partition with this PARTUUID when it is
// an ESP on disk and no other partition carries the same PARTUUID. Empty
// means there is none: the run stopped before the partition table was
// written. disk is the whole disk as lsblk -p prints it.
func FindESP(ctx context.Context, r Runner, disk, partUUID string) (string, error) {
	u := strings.ToLower(partUUID)
	if !partUUIDRe.MatchString(u) {
		return "", fmt.Errorf("ESP PARTUUID %q is not a UUID", partUUID)
	}
	raw, err := r.Output(ctx, "lsblk", "-J", "-l", "-p", "-o", "PATH,TYPE,PARTUUID,PARTLABEL,PARTTYPE,PKNAME")
	if err != nil {
		return "", fmt.Errorf("list disks: %w", err)
	}
	if len(raw) > maxOutput {
		return "", errors.New("lsblk printed too much")
	}
	var out struct {
		BlockDevices []blockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode lsblk: %w", err)
	}
	var found []blockDevice
	for _, d := range out.BlockDevices {
		if strings.ToLower(d.PartUUID) == u {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
	default:
		return "", fmt.Errorf("%d partitions carry the ESP PARTUUID %s", len(found), u)
	}
	d := found[0]
	switch {
	case d.Type != "part":
		return "", fmt.Errorf("%s with the ESP PARTUUID is a %q, not a partition", d.Path, d.Type)
	case d.PKName != disk:
		return "", fmt.Errorf("%s with the ESP PARTUUID is not on %s", d.Path, disk)
	case strings.ToLower(d.PartType) != partTypeESP:
		return "", fmt.Errorf("%s with the ESP PARTUUID is not an ESP", d.Path)
	case !strings.HasPrefix(d.Path, "/dev/") || filepath.Clean(d.Path) != d.Path || strings.ContainsAny(d.Path, "\n\t "):
		return "", fmt.Errorf("lsblk gave an unexpected device path %q", d.Path)
	}
	return d.Path, nil
}

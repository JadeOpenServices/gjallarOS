package targetdisk

import (
	"fmt"
	"strings"
)

// GPT partition type GUIDs the disk list names. lsblk prints them in
// lower case.
const (
	partTypeESP       = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	partTypeMSR       = "e3c9e316-0b5c-4db8-817d-f92df00215ae"
	partTypeWinRE     = "de94bba4-06d1-4d40-a16a-bfd50179d6ac"
	partTypeBasicData = "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7"
	partTypeLinuxSwap = "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f"
)

// Bus names the connection a disk sits on, as the disk list shows it.
func (r Result) Bus() string {
	switch r.Transport {
	case "usb":
		return "USB"
	case "nvme":
		return "NVMe"
	case "sata", "ata":
		return "SATA"
	case "mmc":
		return "SD/eMMC"
	case "":
		return "bus unknown"
	}
	return strings.ToUpper(printable(r.Transport))
}

// SerialEnd shows only the last 4 characters of the serial (or WWN). The
// installer's screen goes into the install log, which a user may share; the
// end is enough to match the sticker on the disk.
func (r Result) SerialEnd() string {
	id := printable(r.Serial)
	kind := "serial"
	if id == "" {
		id, kind = printable(r.WWN), "WWN"
	}
	runes := []rune(id)
	if len(runes) == 0 {
		return "no serial"
	}
	if len(runes) > 4 {
		runes = runes[len(runes)-4:]
	}
	return kind + " …" + string(runes)
}

// Summary is the one line a disk gets in the list and the confirm question.
func (r Result) Summary() string {
	model := printable(r.Model)
	if model == "" {
		model = "unknown model"
	}
	return fmt.Sprintf("%s — %s — %s — %s — %s", r.Path, r.Bus(), SizeText(r.SizeBytes), model, r.SerialEnd())
}

// Details lists the partitions the erase removes, one line each, with what
// the disk itself says about them. All disk text goes through printable.
func (r Result) Details() []string {
	if len(r.Partitions) == 0 {
		return []string{"no partitions"}
	}
	lines := make([]string, 0, len(r.Partitions))
	for _, p := range r.Partitions {
		parts := []string{printable(p.Path), SizeText(p.SizeBytes)}
		if kind := partKind(p); kind != "" {
			parts = append(parts, kind)
		}
		if fs := printable(p.FSType); fs != "" {
			parts = append(parts, fs)
		}
		if label := printable(p.Label); label != "" {
			parts = append(parts, fmt.Sprintf("label %q", label))
		} else if label := printable(p.PartLabel); label != "" {
			parts = append(parts, fmt.Sprintf("name %q", label))
		}
		lines = append(lines, strings.Join(parts, "  "))
	}
	return lines
}

// Warnings names what the erase destroys that a user might not expect.
func (r Result) Warnings() []string {
	var bitlocker, windows, ntfs, luks bool
	for _, p := range r.Partitions {
		switch strings.ToLower(p.FSType) {
		case "bitlocker":
			bitlocker = true
		case "ntfs":
			ntfs = true
		case "crypto_luks":
			luks = true
		}
		switch p.PartType {
		case partTypeMSR, partTypeWinRE:
			windows = true
		}
	}
	var out []string
	if bitlocker {
		out = append(out, "BitLocker: a Windows drive on this disk is encrypted. Its files are gone for good after the erase; copy them off first.")
	}
	if windows || (ntfs && !bitlocker) {
		if windows {
			out = append(out, "Windows is installed on this disk. Installing gjallarOS here removes Windows.")
		} else {
			out = append(out, "This disk holds Windows (NTFS) files. They are erased.")
		}
	}
	if luks {
		out = append(out, "This disk holds encrypted Linux data (LUKS). It is erased.")
	}
	if n := len(r.Partitions); n > 0 {
		word := "partitions"
		if n == 1 {
			word = "partition"
		}
		out = append(out, fmt.Sprintf("All %d %s below are erased.", n, word))
	}
	return out
}

func partKind(p Partition) string {
	switch p.PartType {
	case partTypeESP:
		return "EFI system"
	case partTypeMSR:
		return "Microsoft reserved"
	case partTypeWinRE:
		return "Windows recovery"
	case partTypeBasicData:
		return "basic data"
	case partTypeLinuxSwap:
		return "Linux swap"
	}
	return ""
}

// SizeText writes a size the way the installer shows sizes: MiB below
// 1 GiB, GiB below 1 TiB, then TiB.
func SizeText(bytes uint64) string {
	const (
		mib = uint64(1) << 20
		gib = uint64(1) << 30
		tib = uint64(1) << 40
	)
	switch {
	case bytes >= tib:
		return fmt.Sprintf("%.2f TiB", float64(bytes)/float64(tib))
	case bytes >= gib:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
	}
	return fmt.Sprintf("%d MiB", bytes/mib)
}

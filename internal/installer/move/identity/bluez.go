package identity

import (
	"fmt"
	"regexp"
	"strings"
)

// BlueZ keeps each pairing in <BluezDir>/<adapter>/<device>/info, a keyfile
// bluetoothd reads as root (bluez doc/settings-storage.txt).
const (
	BluezDir = bluezDir
	// MaxAdapters and MaxDevices cap the folders listed; MaxInfo the size
	// of an info file read.
	MaxAdapters = maxAdapters
	MaxDevices  = maxAdapterDir
	MaxInfo     = 16 << 10
)

// BTAddress tells whether name is a Bluetooth address as BlueZ names its
// folders: six uppercase hex pairs.
func BTAddress(name string) bool { return btAddress.MatchString(name) }

var (
	reHexKey = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)
	reHexNum = regexp.MustCompile(`^0x[0-9A-Fa-f]{1,8}$`)
	reUint   = regexp.MustCompile(`^[0-9]{1,20}$`)
	reTechs  = regexp.MustCompile(`^((BR/EDR|LE);)+$`)
)

func hexKey(_ *Profile, v string) bool { return reHexKey.MatchString(v) }
func hexNum(_ *Profile, v string) bool { return reHexNum.MatchString(v) }
func uint64s(_ *Profile, v string) bool {
	return reUint.MatchString(v)
}
func techs(_ *Profile, v string) bool { return reTechs.MatchString(v) }

// flag is an "Authenticated" value: a boolean in the signature keys, a
// small number in the long term keys.
func flag(p *Profile, v string) bool { return boolean(p, v) || uint64s(p, v) }

// uuids is a ";"-list of 128-bit service UUIDs.
func uuids(_ *Profile, v string) bool {
	rest, ok := strings.CutSuffix(v, ";")
	if !ok && v != "" {
		return false
	}
	if rest == "" {
		return true
	}
	for u := range strings.SplitSeq(rest, ";") {
		if !reUUID.MatchString(u) {
			return false
		}
	}
	return true
}

func keys(m map[string]check) func(string) check {
	return func(key string) check { return m[key] }
}

var (
	ltk = keys(map[string]check{
		"Key": hexKey, "Authenticated": flag, "EncSize": uint64s, "EDiv": uint64s, "Rand": uint64s,
	})
	csrk = keys(map[string]check{"Key": hexKey, "Counter": uint64s, "Authenticated": flag})
	// bluezGroups is the allowlist of an info file: the device's name and
	// trust, its id, and the keys of the pairing. Cached services, bearer
	// history and anything else are left out; bluetoothd makes them again.
	bluezGroups = map[string]func(string) check{
		"General": keys(map[string]check{
			"Name": text, "Alias": text, "Class": hexNum, "Appearance": hexNum,
			"SupportedTechnologies": techs, "AddressType": oneOf("static", "public"),
			"Trusted": boolean, "Blocked": boolean, "Services": uuids,
		}),
		"DeviceID": keys(map[string]check{
			"Source": uint64s, "Vendor": uint64s, "Product": uint64s, "Version": uint64s,
		}),
		"LinkKey":               keys(map[string]check{"Key": hexKey, "Type": uint64s, "PINLength": uint64s}),
		"LongTermKey":           ltk,
		"PeripheralLongTermKey": ltk,
		// The name older BlueZ wrote and still reads.
		"SlaveLongTermKey":     ltk,
		"IdentityResolvingKey": keys(map[string]check{"Key": hexKey}),
		"LocalSignatureKey":    csrk,
		"RemoteSignatureKey":   csrk,
		"ConnectionParameters": keys(map[string]check{
			"MinInterval": uint64s, "MaxInterval": uint64s, "Latency": uint64s, "Timeout": uint64s,
		}),
	}
)

// ErrNotPaired refuses the info file of a device BlueZ saw but never
// paired with.
var ErrNotPaired = fmt.Errorf("%w: no pairing key", ErrNotCarried)

// CleanBluezInfo writes a BlueZ info file again from the allowlist above
// (L64). A file with no key of a pairing in it is refused: it is a device
// seen, not paired, and bluetoothd finds it again by itself.
func CleanBluezInfo(raw []byte) ([]byte, error) {
	groups, err := parseKeyfile(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotCarried, err)
	}
	paired := false
	for _, g := range groups {
		switch g.name {
		case "LinkKey", "LongTermKey", "PeripheralLongTermKey", "SlaveLongTermKey":
			paired = true
		}
	}
	if !paired {
		return nil, ErrNotPaired
	}
	var p Profile
	return rewrite(groups, len(raw), &p, func(name string) func(string) check { return bluezGroups[name] })
}

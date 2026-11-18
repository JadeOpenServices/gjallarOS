// Package identity lists what on the old disk makes the old system
// recognisable to other machines: saved networks, VPN keys, pairings,
// fingerprints, the tailnet node, host keys, the machine-id and the
// Nextcloud account (design 345, section 4.4).
//
// This file and scan.go are the inventory: counts and yes/no only, never a
// name, an address, a key or a value. The writers that parse each item and
// write it again from an allowlist come later (one file per item).
package identity

// Item names one identity item. The order of Items is the order of the
// screens.
type Item string

const (
	WiFi           Item = "wifi"
	WiFiEnterprise Item = "wifi-enterprise"
	VPN            Item = "vpn"
	Bluetooth      Item = "bluetooth"
	Fingerprints   Item = "fingerprints"
	Tailscale      Item = "tailscale"
	SSHHostKeys    Item = "ssh-host-keys"
	MachineID      Item = "machine-id"
	Nextcloud      Item = "nextcloud"
)

// Items is every item, in screen order.
var Items = []Item{WiFi, WiFiEnterprise, VPN, Bluetooth, Fingerprints, Tailscale, SSHHostKeys, MachineID, Nextcloud}

// Inventory is what Scan found on the old disk.
type Inventory struct {
	// WiFi counts personal Wi-Fi profiles, WiFiEnterprise the ones with an
	// 802.1X section (certificates, separate yes/no).
	WiFi           int `json:"wifi"`
	WiFiEnterprise int `json:"wifiEnterprise"`
	// VPN counts VPN and WireGuard profiles.
	VPN int `json:"vpn"`
	// Bluetooth counts pairings made with an adapter of this computer;
	// BluetoothOther the ones made with an adapter that is not here, which
	// are never offered.
	Bluetooth      int `json:"bluetooth"`
	BluetoothOther int `json:"bluetoothOther"`
	// Fingerprints counts enrolled fingers of the old account.
	Fingerprints int  `json:"fingerprints"`
	Tailscale    bool `json:"tailscale"`
	SSHHostKeys  int  `json:"sshHostKeys"`
	MachineID    bool `json:"machineId"`
	Nextcloud    bool `json:"nextcloud"`
	// Skipped counts files at identity places that were not understood: a
	// link, a pipe, a file over its cap, a profile that does not parse, a
	// folder over its entry cap. They stay behind and are never copied.
	Skipped int `json:"skipped"`
}

// RiskLine is shown on the screen of every item whose Choice has Risk.
const RiskLine = "If the old disk ever starts again, both copies use the same identity."

// Options are the facts Offer needs beyond the inventory.
type Options struct {
	// PINUnchecked: the old disk has a startup PIN that was not checked
	// (no move prepare record). Secret-bearing items then default to "make
	// new" (open question 1).
	PINUnchecked bool
	// SameComputer: the new disk goes into this computer. Fingerprint
	// templates belong to the reader, so they are only offered then.
	SameComputer bool
}

// Choice is one identity screen.
type Choice struct {
	Item  Item `json:"item"`
	Count int  `json:"count"`
	// Keep is the default: true keeps the item, false makes it new.
	Keep bool `json:"keep"`
	// Risk: the item carries a secret that identifies this machine, and the
	// screen shows RiskLine.
	Risk bool `json:"risk"`
}

// Offer lists the screens for inv, in Items order. An item that was not
// found, or that cannot work on the new system, gets no screen.
func Offer(inv Inventory, o Options) []Choice {
	var out []Choice
	for _, it := range Items {
		n := inv.count(it)
		if n == 0 || (it == Fingerprints && !o.SameComputer) {
			continue
		}
		risk := it == VPN || it == Tailscale || it == SSHHostKeys || it == MachineID
		out = append(out, Choice{Item: it, Count: n, Keep: !(risk && o.PINUnchecked), Risk: risk})
	}
	return out
}

func (inv Inventory) count(it Item) int {
	one := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	switch it {
	case WiFi:
		return inv.WiFi
	case WiFiEnterprise:
		return inv.WiFiEnterprise
	case VPN:
		return inv.VPN
	case Bluetooth:
		return inv.Bluetooth
	case Fingerprints:
		return inv.Fingerprints
	case Tailscale:
		return one(inv.Tailscale)
	case SSHHostKeys:
		return inv.SSHHostKeys
	case MachineID:
		return one(inv.MachineID)
	case Nextcloud:
		return one(inv.Nextcloud)
	}
	return 0
}

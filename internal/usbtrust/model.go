package usbtrust

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const SchemaVersion = 1

type DeviceClass string

const (
	ClassInternal DeviceClass = "internal"
	ClassExternal DeviceClass = "external"
)

type IdentityStrength string

const (
	StrengthDescriptor               IdentityStrength = "descriptor"
	StrengthSerialDescriptor         IdentityStrength = "serial+descriptor"
	StrengthSerialDescriptorTopology IdentityStrength = "serial+descriptor+topology"
	StrengthHardwareAttested         IdentityStrength = "hardware-attested"
)

type Identity struct {
	VIDPID      string   `json:"vidPid"`
	Serial      string   `json:"serial,omitempty"`
	Name        string   `json:"name,omitempty"`
	Hash        string   `json:"hash"`
	ParentHash  string   `json:"parentHash,omitempty"`
	Interfaces  []string `json:"interfaces,omitempty"`
	ConnectType string   `json:"connectType,omitempty"`
	Port        string   `json:"port,omitempty"`
	// BluetoothAddress is what a Bluetooth controller reports as its own
	// address, read after it is allowed. It is not part of the USB
	// identity match; ODDC asks for it as a second check (usbIdentity
	// anchor "bluetooth-address").
	BluetoothAddress string `json:"bluetoothAddress,omitempty"`
}

var bluetoothAddressPattern = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)

type Attestation struct {
	Type        string `json:"type"`
	Identity    string `json:"identity"`
	Fingerprint string `json:"fingerprint"`
}

type Device struct {
	ID             string           `json:"id"`
	Role           string           `json:"role,omitempty"`
	Class          DeviceClass      `json:"class"`
	Portable       bool             `json:"portable"`
	ExpectedByODDC bool             `json:"expectedByOddc"`
	Strength       IdentityStrength `json:"identityStrength"`
	Identity       Identity         `json:"identity"`
	Attestation    *Attestation     `json:"attestation,omitempty"`
	FirstAccepted  string           `json:"firstAccepted"`
	LastAccepted   string           `json:"lastAccepted"`
}

// Enforcement is signed with the trust records so that switching blocking off
// is a TPM-signed decision, not a configuration flag.
type Enforcement struct {
	Armed bool `json:"armed"`
	// EnrolledAt records the one-time automatic internal enrollment.
	EnrolledAt string `json:"enrolledAt"`
	ChangedAt  string `json:"changedAt"`
}

type Document struct {
	Schema      int          `json:"schema"`
	MachineID   string       `json:"machineId"`
	ODDCModel   string       `json:"oddcModel"`
	Revision    uint64       `json:"revision"`
	Devices     []Device     `json:"devices"`
	Enforcement *Enforcement `json:"enforcement,omitempty"`
}

func (d *Document) Normalize() {
	for i := range d.Devices {
		sort.Strings(d.Devices[i].Identity.Interfaces)
	}

	sort.Slice(d.Devices, func(i, j int) bool {
		return d.Devices[i].ID < d.Devices[j].ID
	})
}

func (d Document) Validate() error {
	if d.Schema != SchemaVersion {
		return fmt.Errorf("unsupported USB trust schema %d", d.Schema)
	}

	if strings.TrimSpace(d.MachineID) == "" {
		return errors.New("machineId is required")
	}

	if strings.TrimSpace(d.ODDCModel) == "" {
		return errors.New("oddcModel is required")
	}

	if d.Revision == 0 {
		return errors.New("revision must be greater than zero")
	}

	if d.Enforcement != nil && (strings.TrimSpace(d.Enforcement.EnrolledAt) == "" || strings.TrimSpace(d.Enforcement.ChangedAt) == "") {
		return errors.New("enforcement state requires enrollment and change times")
	}

	seen := make(map[string]struct{}, len(d.Devices))
	internalRoles := make(map[string]struct{})

	for _, device := range d.Devices {
		if strings.TrimSpace(device.ID) == "" {
			return errors.New("device id is required")
		}

		if _, exists := seen[device.ID]; exists {
			return fmt.Errorf("duplicate USB trust device id %q", device.ID)
		}
		seen[device.ID] = struct{}{}

		switch device.Class {
		case ClassInternal:
			role := strings.TrimSpace(device.Role)
			if role == "" {
				return fmt.Errorf("internal device %q has no ODDC role", device.ID)
			}
			if _, exists := internalRoles[role]; exists {
				return fmt.Errorf("duplicate internal USB trust role %q", role)
			}
			internalRoles[role] = struct{}{}
		case ClassExternal:
			if strings.TrimSpace(device.Role) != "" || device.ExpectedByODDC {
				return fmt.Errorf("external device %q cannot claim an ODDC role", device.ID)
			}
		default:
			return fmt.Errorf(
				"device %q has invalid class %q",
				device.ID,
				device.Class,
			)
		}

		if address := device.Identity.BluetoothAddress; address != "" && !bluetoothAddressPattern.MatchString(address) {
			return fmt.Errorf("device %q has an invalid Bluetooth address", device.ID)
		}

		switch device.Strength {
		case StrengthDescriptor, StrengthSerialDescriptor, StrengthSerialDescriptorTopology, StrengthHardwareAttested:
		default:
			return fmt.Errorf("device %q has invalid identity strength %q", device.ID, device.Strength)
		}

		if strings.TrimSpace(device.Identity.VIDPID) == "" {
			return fmt.Errorf("device %q has no VID:PID", device.ID)
		}

		if strings.TrimSpace(device.Identity.Hash) == "" {
			return fmt.Errorf("device %q has no descriptor hash", device.ID)
		}

		if device.Class == ClassInternal && !device.ExpectedByODDC {
			return fmt.Errorf(
				"internal device %q is not backed by ODDC expectation",
				device.ID,
			)
		}

		if device.Class == ClassInternal && device.Portable {
			return fmt.Errorf(
				"internal device %q cannot be portable",
				device.ID,
			)
		}
	}

	return nil
}

func Canonical(d Document) ([]byte, error) {
	d.Normalize()

	if err := d.Validate(); err != nil {
		return nil, err
	}

	return json.Marshal(d)
}

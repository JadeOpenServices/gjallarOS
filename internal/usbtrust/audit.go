package usbtrust

import (
	"fmt"
	"sort"
	"strings"
)

type ExpectedDevice struct {
	Role        string   `json:"role"`
	VIDPID      string   `json:"vidPid"`
	ConnectType string   `json:"connectType,omitempty"`
	Interfaces  []string `json:"interfaces,omitempty"`
	Required    bool     `json:"required"`

	// Port is where ODDC says the part is wired. Set only for parts that
	// ODDC lists with a usbPort; those are the built-in parts.
	Port *ExpectedPort `json:"port,omitempty"`
}

// ExpectedPort is one usbPort entry from `oddc devices --json`.
type ExpectedPort struct {
	// Controller is the PCI vendor:device of the host controller.
	Controller string `json:"controller"`
	// Hub is the root hub on that controller ("usb2" or "usb3").
	Hub string `json:"hub"`
	// RootPort is the port number on that root hub.
	RootPort int `json:"rootPort"`
	// Attachment is "internal" or "detachable". A detachable part may be
	// absent without a finding.
	Attachment string `json:"attachment"`
	// Debug is set when ODDC marks the part unsupported or not proven.
	// Such a part stays blocked unless a contributor allows it.
	Debug bool `json:"debug,omitempty"`
	// Serial says whether the USB serial tells units apart.
	Serial SerialMode `json:"serial,omitempty"`
	// Anchor names a second identity check, e.g. the Bluetooth address.
	Anchor string `json:"anchor,omitempty"`
}

type SerialMode string

const (
	// SerialPerUnit: the serial differs per unit and is part of identity.
	SerialPerUnit SerialMode = "per-unit"
	// SerialPlaceholder: every unit reports the same serial.
	SerialPlaceholder SerialMode = "placeholder"
	// SerialNone: the part reports no serial.
	SerialNone SerialMode = "none"
)

const AnchorBluetoothAddress = "bluetooth-address"

type ObservedDevice struct {
	RuntimeID string   `json:"runtimeId"`
	Identity  Identity `json:"identity"`

	// Wiring is set when the device sits directly on a root port of a
	// controller the kernel could name.
	Wiring *ObservedPort `json:"wiring,omitempty"`
}

// ObservedPort is the kernel's view of a root port, in the same terms
// as ExpectedPort.
type ObservedPort struct {
	Controller string `json:"controller"`
	Hub        string `json:"hub"`
	RootPort   int    `json:"rootPort"`
	// Shared is set when another root hub has the same controller ID and
	// hub, so an ODDC port entry cannot name just one of them.
	Shared bool `json:"shared,omitempty"`
}

type AuditState string

const (
	AuditPass   AuditState = "PASS"
	AuditWarn   AuditState = "WARN"
	AuditReview AuditState = "REVIEW"
	AuditBlock  AuditState = "BLOCK"
)

type AuditCode string

const (
	CodeInternalMatch          AuditCode = "internal-match"
	CodeInternalMissing        AuditCode = "internal-missing"
	CodeInternalUnvalidated    AuditCode = "internal-unvalidated"
	CodeInternalChanged        AuditCode = "internal-changed"
	CodeInternalAmbiguous      AuditCode = "internal-ambiguous"
	CodeUnexpectedInternal     AuditCode = "unexpected-internal"
	CodeTrustedExternal        AuditCode = "trusted-external"
	CodeTrustedExternalChanged AuditCode = "trusted-external-changed"
	CodeUnknownExternal        AuditCode = "unknown-external"

	// CodeInternalPortMatch: the part sits exactly where ODDC says and has
	// no stored identity yet. It is allowed and stored once the store is
	// ready.
	CodeInternalPortMatch AuditCode = "internal-port-match"
	// CodeInternalDebug: ODDC marks the part unsupported or not proven.
	// It stays blocked without a pop-up.
	CodeInternalDebug AuditCode = "internal-debug"
)

type Finding struct {
	State     AuditState `json:"state"`
	Code      AuditCode  `json:"code"`
	Role      string     `json:"role,omitempty"`
	TrustedID string     `json:"trustedId,omitempty"`
	RuntimeID string     `json:"runtimeId,omitempty"`
	Message   string     `json:"message"`
}

type AuditInput struct {
	Expected []ExpectedDevice
	Trusted  *Document
	Observed []ObservedDevice
}

type AuditResult struct {
	Findings []Finding `json:"findings"`
}

func Audit(input AuditInput) (AuditResult, error) {
	if err := validateExpectations(input.Expected); err != nil {
		return AuditResult{}, err
	}

	if input.Trusted != nil {
		if err := input.Trusted.Validate(); err != nil {
			return AuditResult{}, fmt.Errorf(
				"invalid trusted USB state: %w",
				err,
			)
		}
	}

	consumed := make(map[int]bool)
	trustedInternal := make(map[string]Device)
	var trustedExternal []Device

	if input.Trusted != nil {
		for _, device := range input.Trusted.Devices {
			switch device.Class {
			case ClassInternal:
				trustedInternal[device.Role] = device
			case ClassExternal:
				trustedExternal = append(trustedExternal, device)
			}
		}
	}

	var findings []Finding

	builtInPorts := make(map[ObservedPort]struct{})

	for _, expected := range input.Expected {
		if port := expected.Port; port != nil {
			builtInPorts[ObservedPort{Controller: port.Controller, Hub: port.Hub, RootPort: port.RootPort}] = struct{}{}
		}
	}

	for _, expected := range input.Expected {
		var candidates []int

		for i, observed := range input.Observed {
			if consumed[i] {
				continue
			}

			if matchesExpectation(expected, observed) {
				candidates = append(candidates, i)
			}
		}

		trusted, hasTrusted := trustedInternal[expected.Role]

		if expected.Port != nil {
			for _, i := range candidates {
				consumed[i] = true
			}

			findings = append(findings, auditPort(expected, trusted, hasTrusted, candidates, input.Observed)...)

			continue
		}

		if !hasTrusted {
			switch len(candidates) {
			case 0:
				if expected.Required {
					findings = append(findings, Finding{
						State:   AuditWarn,
						Code:    CodeInternalMissing,
						Role:    expected.Role,
						Message: "expected internal USB device is not present",
					})
				}
			case 1:
				i := candidates[0]
				consumed[i] = true

				findings = append(findings, Finding{
					State:     AuditReview,
					Code:      CodeInternalUnvalidated,
					Role:      expected.Role,
					RuntimeID: input.Observed[i].RuntimeID,
					Message:   "expected internal USB device is present but has no accepted machine-local identity",
				})
			default:
				for _, i := range candidates {
					consumed[i] = true
					findings = append(findings, Finding{
						State: AuditReview, Code: CodeInternalAmbiguous, Role: expected.Role,
						RuntimeID: input.Observed[i].RuntimeID,
						Message:   "multiple USB devices match the same ODDC internal-device expectation",
					})
				}
			}

			continue
		}

		matched := -1

		for _, i := range candidates {
			if identityMatchesTrusted(
				trusted,
				input.Observed[i].Identity,
			) {
				matched = i
				break
			}
		}

		if matched >= 0 {
			consumed[matched] = true

			findings = append(findings, Finding{
				State:     AuditPass,
				Code:      CodeInternalMatch,
				Role:      expected.Role,
				TrustedID: trusted.ID,
				RuntimeID: input.Observed[matched].RuntimeID,
				Message:   "internal USB identity matches ODDC expectation and accepted machine identity",
			})

			continue
		}

		if len(candidates) > 0 {
			for _, i := range candidates {
				consumed[i] = true
				finding := Finding{
					State: AuditBlock, Code: CodeInternalChanged, Role: expected.Role,
					TrustedID: trusted.ID, RuntimeID: input.Observed[i].RuntimeID,
					Message: "internal USB device matches the expected role but its accepted physical identity changed",
				}
				if len(candidates) > 1 {
					finding.Code = CodeInternalAmbiguous
					finding.Message = "multiple changed USB identities match the accepted ODDC role; resolve ambiguity before replacement"
				}
				findings = append(findings, finding)
			}

			continue
		}

		if expected.Required {
			findings = append(findings, Finding{
				State:     AuditWarn,
				Code:      CodeInternalMissing,
				Role:      expected.Role,
				TrustedID: trusted.ID,
				Message:   "accepted internal USB device is missing",
			})
		}
	}

	for i, observed := range input.Observed {
		if consumed[i] {
			continue
		}

		trustedIndex := -1

		for j, trusted := range trustedExternal {
			if identityMatchesTrusted(trusted, observed.Identity) {
				trustedIndex = j
				break
			}
		}

		if trustedIndex >= 0 {
			trusted := trustedExternal[trustedIndex]
			consumed[i] = true

			findings = append(findings, Finding{
				State:     AuditPass,
				Code:      CodeTrustedExternal,
				TrustedID: trusted.ID,
				RuntimeID: observed.RuntimeID,
				Message:   "external USB device matches permanent machine trust",
			})

			continue
		}

		changedIndex := -1

		for j, trusted := range trustedExternal {
			if likelySameTrustedExternal(
				trusted,
				observed.Identity,
			) {
				changedIndex = j
				break
			}
		}

		if changedIndex >= 0 {
			trusted := trustedExternal[changedIndex]
			consumed[i] = true

			findings = append(findings, Finding{
				State:     AuditBlock,
				Code:      CodeTrustedExternalChanged,
				TrustedID: trusted.ID,
				RuntimeID: observed.RuntimeID,
				Message:   "previously trusted external USB device appears to have changed physical identity or capabilities",
			})

			continue
		}

		if observed.Identity.ConnectType == "hardwired" || onBuiltInPort(observed, builtInPorts) {
			findings = append(findings, Finding{
				State:     AuditBlock,
				Code:      CodeUnexpectedInternal,
				RuntimeID: observed.RuntimeID,
				Message:   "unexpected hardwired USB device is not described by ODDC",
			})
			continue
		}

		findings = append(findings, Finding{
			State:     AuditBlock,
			Code:      CodeUnknownExternal,
			RuntimeID: observed.RuntimeID,
			Message:   "external USB device is not trusted",
		})
	}

	sort.SliceStable(findings, func(i, j int) bool {
		left := string(findings[i].State) +
			string(findings[i].Code) +
			findings[i].Role +
			findings[i].RuntimeID

		right := string(findings[j].State) +
			string(findings[j].Code) +
			findings[j].Role +
			findings[j].RuntimeID

		return left < right
	})

	return AuditResult{
		Findings: findings,
	}, nil
}

// auditPort judges a part that ODDC lists with a usbPort. Candidates
// already sit on the exact root port ODDC names.
func auditPort(
	expected ExpectedDevice,
	trusted Device,
	hasTrusted bool,
	candidates []int,
	observed []ObservedDevice,
) []Finding {
	base := Finding{Role: expected.Role}
	if hasTrusted {
		base.TrustedID = trusted.ID
	}

	if len(candidates) == 0 {
		if !expected.Required || expected.Port.Debug {
			return nil
		}

		finding := base
		finding.State, finding.Code = AuditWarn, CodeInternalMissing
		finding.Message = "built-in USB part is not present on its port"

		return []Finding{finding}
	}

	if len(candidates) > 1 {
		var findings []Finding

		for _, i := range candidates {
			finding := base
			finding.State, finding.Code, finding.RuntimeID = AuditReview, CodeInternalAmbiguous, observed[i].RuntimeID
			finding.Message = "more than one USB device sits on the port of this built-in part"
			findings = append(findings, finding)
		}

		return findings
	}

	device := observed[candidates[0]]
	finding := base
	finding.RuntimeID = device.RuntimeID

	switch {
	case device.Wiring.Shared:
		// The worse of ODDC and the kernel wins: when ODDC's port names
		// more than one root hub, the port proves nothing.
		finding.State, finding.Code = AuditReview, CodeInternalAmbiguous
		finding.Message = "two USB controllers share this controller ID, so the port does not name one part"
	case device.Identity.ConnectType == "hotplug":
		finding.State, finding.Code = AuditReview, CodeInternalAmbiguous
		finding.Message = "the kernel reports this port as external, ODDC lists a built-in part there"
	case expected.Port.Debug:
		finding.State, finding.Code = AuditBlock, CodeInternalDebug
		finding.Message = "ODDC marks this built-in part as unsupported or not proven"
	case hasTrusted && portIdentityMatches(trusted, device.Identity, expected.Port.Serial):
		finding.State, finding.Code = AuditPass, CodeInternalMatch
		finding.Message = "built-in USB part sits on its ODDC port and matches its stored identity"
	case hasTrusted:
		finding.State, finding.Code = AuditBlock, CodeInternalChanged
		finding.Message = "built-in USB part sits on its ODDC port but its stored identity changed"
	default:
		finding.State, finding.Code = AuditPass, CodeInternalPortMatch
		finding.Message = "built-in USB part sits on its ODDC port; identity not stored yet"
	}

	return []Finding{finding}
}

// portIdentityMatches compares a built-in part with its stored identity.
// A serial counts only when ODDC says it differs per unit; otherwise the
// port is the anchor.
func portIdentityMatches(
	trusted Device,
	observed Identity,
	serial SerialMode,
) bool {
	if serial != SerialPerUnit {
		trusted.Identity.Serial = ""
		observed.Serial = ""
	}

	return identityMatchesTrusted(trusted, observed)
}

func onBuiltInPort(
	observed ObservedDevice,
	ports map[ObservedPort]struct{},
) bool {
	if observed.Wiring == nil {
		return false
	}

	wiring := *observed.Wiring
	wiring.Shared = false
	_, ok := ports[wiring]

	return ok
}

func likelySameTrustedExternal(
	trusted Device,
	observed Identity,
) bool {
	expected := trusted.Identity

	if expected.VIDPID != observed.VIDPID {
		return false
	}

	if expected.Serial != "" {
		return observed.Serial != "" &&
			expected.Serial == observed.Serial
	}

	if trusted.Portable {
		return false
	}

	if expected.ParentHash != "" &&
		expected.Port != "" {
		return expected.ParentHash == observed.ParentHash &&
			expected.Port == observed.Port
	}

	if expected.ParentHash != "" {
		return expected.ParentHash == observed.ParentHash
	}

	if expected.Port != "" {
		return expected.Port == observed.Port
	}

	return false
}

func validateExpectations(expected []ExpectedDevice) error {
	roles := make(map[string]struct{}, len(expected))
	ports := make(map[ObservedPort]string)

	for _, device := range expected {
		if strings.TrimSpace(device.Role) == "" {
			return fmt.Errorf("ODDC USB expectation is missing a role")
		}

		if strings.TrimSpace(device.VIDPID) == "" {
			return fmt.Errorf(
				"ODDC USB expectation %q is missing VID:PID",
				device.Role,
			)
		}

		if _, exists := roles[device.Role]; exists {
			return fmt.Errorf(
				"duplicate ODDC USB expectation role %q",
				device.Role,
			)
		}

		roles[device.Role] = struct{}{}

		if port := device.Port; port != nil {
			key := ObservedPort{Controller: port.Controller, Hub: port.Hub, RootPort: port.RootPort}
			if other, exists := ports[key]; exists {
				return fmt.Errorf(
					"ODDC USB expectations %q and %q share one port",
					other,
					device.Role,
				)
			}
			ports[key] = device.Role
		}
	}

	return nil
}

func matchesExpectation(
	expected ExpectedDevice,
	device ObservedDevice,
) bool {
	observed := device.Identity

	if expected.VIDPID != observed.VIDPID {
		return false
	}

	// A part with a port matches only on that exact root port. The
	// kernel's connect type is judged in auditPort, not here.
	if port := expected.Port; port != nil {
		wiring := device.Wiring
		if wiring == nil ||
			wiring.Controller != port.Controller ||
			wiring.Hub != port.Hub ||
			wiring.RootPort != port.RootPort {
			return false
		}

		return containsAllInterfaces(observed.Interfaces, expected.Interfaces)
	}

	if expected.ConnectType != "" &&
		expected.ConnectType != observed.ConnectType {
		return false
	}

	return containsAllInterfaces(
		observed.Interfaces,
		expected.Interfaces,
	)
}

func identityMatchesTrusted(
	trusted Device,
	observed Identity,
) bool {
	expected := trusted.Identity

	if expected.VIDPID != observed.VIDPID ||
		expected.Hash != observed.Hash {
		return false
	}

	if expected.Serial != "" &&
		expected.Serial != observed.Serial {
		return false
	}

	if expected.ConnectType != "" &&
		expected.ConnectType != observed.ConnectType {
		return false
	}

	if !sameStringSet(
		expected.Interfaces,
		observed.Interfaces,
	) {
		return false
	}

	// Portable external hardware may legitimately move between ports,
	// docks and parent hubs. Physical topology remains part of identity
	// for internal/non-portable hardware.
	if !trusted.Portable {
		if expected.ParentHash != "" &&
			expected.ParentHash != observed.ParentHash {
			return false
		}

		if expected.Port != "" &&
			expected.Port != observed.Port {
			return false
		}
	}

	return true
}

func containsAllInterfaces(
	actual []string,
	required []string,
) bool {
	if len(required) == 0 {
		return true
	}

	have := make(map[string]struct{}, len(actual))

	for _, value := range actual {
		have[value] = struct{}{}
	}

	for _, value := range required {
		if _, ok := have[value]; !ok {
			return false
		}
	}

	return true
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	a := append([]string(nil), left...)
	b := append([]string(nil), right...)

	sort.Strings(a)
	sort.Strings(b)

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

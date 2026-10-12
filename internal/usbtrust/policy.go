package usbtrust

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

type Target string

const (
	TargetAllow Target = "allow"
	TargetBlock Target = "block"
)

// Decision is a derived, inspectable policy for one connected device. A
// Connection token binds a review to the exact observation shown to the user.
type Decision struct {
	ObservedDevice
	Connection string        `json:"connection"`
	Target     Target        `json:"target"`
	Reason     string        `json:"reason"`
	Role       string        `json:"role,omitempty"`
	TrustedID  string        `json:"trustedId,omitempty"`
	Risks      []RiskFinding `json:"risks"`
	Actions    []Action      `json:"actions"`
}

type connection struct {
	identity Identity
	token    string
	choice   Target
}

// Sessions belongs to a single observation-backend lifetime. Call Reset when
// that backend restarts or observation continuity cannot be established.
// Callers serialize access; none of this state is persisted.
type Sessions struct {
	connected map[string]connection
}

func (s *Sessions) Reset() { s.connected = nil }

func (s *Sessions) Disconnect(runtimeID string) { delete(s.connected, runtimeID) }

func (s *Sessions) Observe(observed []ObservedDevice) error {
	next := make(map[string]connection, len(observed))
	for _, device := range observed {
		id, err := strconv.ParseUint(device.RuntimeID, 10, 32)
		if err != nil || id == 0 || strconv.FormatUint(id, 10) != device.RuntimeID {
			return fmt.Errorf("invalid USB runtime ID %q", device.RuntimeID)
		}
		if _, exists := next[device.RuntimeID]; exists {
			return fmt.Errorf("duplicate USB runtime ID %q", device.RuntimeID)
		}
		if device.Identity.VIDPID == "" || device.Identity.Hash == "" {
			return fmt.Errorf("USB observation %s has incomplete identity", device.RuntimeID)
		}
		identity := device.Identity
		identity.Interfaces = append([]string(nil), identity.Interfaces...)
		sort.Strings(identity.Interfaces)
		previous, exists := s.connected[device.RuntimeID]
		if !exists || !reflect.DeepEqual(previous.identity, identity) {
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return err
			}
			previous = connection{identity: identity, token: hex.EncodeToString(token[:])}
		}
		next[device.RuntimeID] = previous
	}
	s.connected = next
	return nil
}

func (s *Sessions) Check(runtimeID, token string) error {
	current, ok := s.connected[runtimeID]
	if !ok || token == "" || current.token != token {
		return fmt.Errorf("USB connection changed; review device again")
	}
	return nil
}

func (s *Sessions) Choose(runtimeID, token string, target Target) error {
	if err := s.Check(runtimeID, token); err != nil {
		return err
	}
	if target != TargetAllow && target != TargetBlock {
		return fmt.Errorf("invalid USB target %q", target)
	}
	current := s.connected[runtimeID]
	current.choice = target
	s.connected[runtimeID] = current
	return nil
}

// Derive never grants trust from parent trust alone. An accepted identity,
// an explicit current-connection choice, or a built-in part on the exact
// root port ODDC names allows.
func (s *Sessions) Derive(input AuditInput) ([]Decision, error) {
	audit, err := Audit(input)
	if err != nil {
		return nil, err
	}
	decisions := make([]Decision, 0, len(input.Observed))
	for _, device := range input.Observed {
		current, ok := s.connected[device.RuntimeID]
		if !ok {
			return nil, fmt.Errorf("USB observation has no current session")
		}
		identity := device.Identity
		identity.Interfaces = append([]string(nil), identity.Interfaces...)
		sort.Strings(identity.Interfaces)
		if !reflect.DeepEqual(current.identity, identity) {
			return nil, fmt.Errorf("USB policy observation differs from current session")
		}
		risks, err := AssessIdentityRisk(device.Identity)
		if err != nil {
			return nil, err
		}
		if device.Identity.Serial == "" {
			risks = append(risks, RiskFinding{Code: "descriptor-only", Severity: RiskReview, Detail: "descriptor-only identity is spoofable; no device attestation"})
		}
		d := Decision{ObservedDevice: device, Connection: current.token, Target: TargetBlock, Reason: "unknown-external", Risks: risks, Actions: []Action{ActionKeepBlocked, ActionAllowOnce}}
		for _, finding := range audit.Findings {
			if finding.RuntimeID != device.RuntimeID {
				continue
			}
			d.Reason, d.Role, d.TrustedID = string(finding.Code), finding.Role, finding.TrustedID
			if finding.State == AuditPass {
				d.Target = TargetAllow
			}
		}
		// The client displays these domain-owned capabilities; it must not
		// infer enrollment/replacement eligibility from role strings.
		switch AuditCode(d.Reason) {
		case CodeUnknownExternal:
			d.Actions = append(d.Actions, ActionTrustPermanent)
		case CodeInternalUnvalidated:
			d.Actions = append(d.Actions, ActionEnrollInternal)
		case CodeInternalChanged, CodeTrustedExternalChanged:
			d.Actions = append(d.Actions, ActionAcceptReplacement)
		case CodeInternalDebug:
			// ODDC does not stand behind this part; no one-off allow either.
			d.Actions = []Action{ActionKeepBlocked}
		}
		if current.choice != "" {
			d.Target = current.choice
			d.Reason = "blocked-for-connection"
			if current.choice == TargetAllow {
				d.Reason = "allowed-for-connection"
			}
		}
		decisions = append(decisions, d)
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].RuntimeID < decisions[j].RuntimeID })
	return decisions, nil
}

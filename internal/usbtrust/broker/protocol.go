package broker

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

type Action = usbtrust.Action

const (
	ActionStatus            = usbtrust.ActionStatus
	ActionAudit             = usbtrust.ActionAudit
	ActionPolicy            = usbtrust.ActionPolicy
	ActionKeepBlocked       = usbtrust.ActionKeepBlocked
	ActionProvisionKey      = usbtrust.ActionProvisionKey
	ActionAllowOnce         = usbtrust.ActionAllowOnce
	ActionTrustPermanent    = usbtrust.ActionTrustPermanent
	ActionForget            = usbtrust.ActionForget
	ActionAcceptReplacement = usbtrust.ActionAcceptReplacement
	ActionEnrollInternal    = usbtrust.ActionEnrollInternal
	ActionDisarm            = usbtrust.ActionDisarm
	ActionArm               = usbtrust.ActionArm
	ActionAllowMedia        = usbtrust.ActionAllowMedia
	ActionRenew             = usbtrust.ActionRenew
)

type Request struct {
	Action     Action `json:"action"`
	RuntimeID  string `json:"runtimeId,omitempty"`
	TrustedID  string `json:"trustedId,omitempty"`
	Role       string `json:"role,omitempty"`
	Connection string `json:"connection,omitempty"`

	// Portable is explicit for permanent external trust.
	// nil means the caller did not make a portability decision.
	Portable *bool `json:"portable,omitempty"`

	// RecoveryKey is the disk encryption passphrase that authorizes disarm.
	RecoveryKey string `json:"recoveryKey,omitempty"`

	// Media is the SCSI LUN (H:C:T:L) whose current medium allow-media opens.
	Media string `json:"media,omitempty"`
}

type Status struct {
	Devices             []usbtrust.Device `json:"devices,omitempty"`
	Enforcing           bool              `json:"enforcing"`
	Armed               bool              `json:"armed"`
	PermanentTrustReady bool              `json:"permanentTrustReady"`
	StatePresent        bool              `json:"statePresent"`
	Revision            uint64            `json:"revision,omitempty"`
	TrustedDevices      int               `json:"trustedDevices"`
	ExpectedDevices     int               `json:"expectedDevices"`
	ObservedDevices     int               `json:"observedDevices"`
	LapseDays           int               `json:"lapseDays,omitempty"`
	Lapses              []TrustLapse      `json:"lapses,omitempty"`
}

// TrustLapse says when an external device's permanent trust runs out.
// LapsesAt is empty when its stored time is unusable, which counts as lapsed.
type TrustLapse struct {
	TrustedID string `json:"trustedId"`
	Name      string `json:"name,omitempty"`
	LastUsed  string `json:"lastUsed,omitempty"`
	LapsesAt  string `json:"lapsesAt,omitempty"`
	Lapsed    bool   `json:"lapsed"`
}

type Response struct {
	Enforcing bool                     `json:"enforcing"`
	OK        bool                     `json:"ok"`
	Message   string                   `json:"message,omitempty"`
	Error     string                   `json:"error,omitempty"`
	Revision  uint64                   `json:"revision,omitempty"`
	Status    *Status                  `json:"status,omitempty"`
	Audit     *usbtrust.AuditResult    `json:"audit,omitempty"`
	Policy    []usbtrust.Decision      `json:"policy,omitempty"`
	Media     []usbtrust.MediaDecision `json:"media,omitempty"`
	// MediaPending is set while the broker has not scanned the readers since
	// it started: Media may then miss inserted media.
	MediaPending bool `json:"mediaPending,omitempty"`
	// Renewals lists trusted devices that lapse soon and may be renewed.
	Renewals []TrustLapse `json:"renewals,omitempty"`
}

func ValidateRequest(request Request) error {
	if !request.Action.Valid() {
		return fmt.Errorf(
			"unsupported USB trust action %q",
			request.Action,
		)
	}
	// Reject surplus fields rather than letting an unrelated identifier alter
	// the target of a mutation.
	if request.Action.Mutation() {
		if request.TrustedID != "" && request.Action != ActionForget && request.Action != ActionAcceptReplacement && request.Action != ActionRenew {
			return fmt.Errorf("%s does not accept trustedId", request.Action)
		}
		if request.Role != "" && request.Action != ActionEnrollInternal {
			return fmt.Errorf("%s does not accept role", request.Action)
		}
		if request.Portable != nil && request.Action != ActionTrustPermanent {
			return fmt.Errorf("%s does not accept portable", request.Action)
		}
		if request.RecoveryKey != "" && request.Action != ActionDisarm {
			return fmt.Errorf("%s does not accept recoveryKey", request.Action)
		}
		if request.Media != "" && request.Action != ActionAllowMedia {
			return fmt.Errorf("%s does not accept media", request.Action)
		}
		if (request.Action == ActionForget || request.Action == ActionRenew) && (request.RuntimeID != "" || request.Connection != "") {
			return fmt.Errorf("%s does not accept a connection", request.Action)
		}
	}

	switch request.Action {
	case ActionProvisionKey, ActionArm:
		return noMutationArguments(request)
	case ActionStatus, ActionAudit, ActionPolicy:
		return noMutationArguments(request)

	case ActionAllowOnce, ActionKeepBlocked:
		if err := requireCurrentConnection(request); err != nil {
			return err
		}

	case ActionTrustPermanent:
		if err := requireCurrentConnection(request); err != nil {
			return err
		}

		if request.Portable == nil {
			return fmt.Errorf(
				"%s requires an explicit portable decision",
				request.Action,
			)
		}

	case ActionForget, ActionRenew:
		if strings.TrimSpace(request.TrustedID) == "" {
			return fmt.Errorf(
				"%s requires trustedId",
				request.Action,
			)
		}

	case ActionAcceptReplacement:
		if err := requireCurrentConnection(request); err != nil {
			return err
		}
		if strings.TrimSpace(request.TrustedID) == "" {
			return fmt.Errorf(
				"%s requires trustedId",
				request.Action,
			)
		}

	case ActionAllowMedia:
		if request.RuntimeID != "" {
			return fmt.Errorf("%s does not accept runtimeId", request.Action)
		}
		if strings.TrimSpace(request.Media) == "" || strings.TrimSpace(request.Connection) == "" {
			return fmt.Errorf("%s requires media and connection", request.Action)
		}

	case ActionDisarm:
		if request.RuntimeID != "" || request.TrustedID != "" || request.Connection != "" {
			return fmt.Errorf("%s does not accept device arguments", request.Action)
		}
		if request.RecoveryKey == "" {
			return fmt.Errorf("%s requires the disk encryption passphrase", request.Action)
		}

	case ActionEnrollInternal:
		if err := requireCurrentConnection(request); err != nil {
			return err
		}
		if strings.TrimSpace(request.Role) == "" {
			return fmt.Errorf(
				"%s requires role",
				request.Action,
			)
		}
	}

	return nil
}

func requireCurrentConnection(request Request) error {
	if strings.TrimSpace(request.RuntimeID) == "" ||
		strings.TrimSpace(request.Connection) == "" {
		return fmt.Errorf(
			"%s requires runtimeId and connection",
			request.Action,
		)
	}

	return nil
}

func noMutationArguments(request Request) error {
	if request.RuntimeID != "" ||
		request.TrustedID != "" ||
		request.Role != "" ||
		request.Connection != "" ||
		request.Portable != nil ||
		request.RecoveryKey != "" ||
		request.Media != "" {
		return fmt.Errorf(
			"%s does not accept mutation arguments",
			request.Action,
		)
	}

	return nil
}

func DecodeRequest(reader io.Reader) (Request, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var request Request

	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf(
			"decode USB trust request: %w",
			err,
		)
	}

	var trailing any

	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Request{}, fmt.Errorf(
				"USB trust request contains trailing JSON",
			)
		}

		return Request{}, fmt.Errorf(
			"decode trailing USB trust request data: %w",
			err,
		)
	}

	if err := ValidateRequest(request); err != nil {
		return Request{}, err
	}

	return request, nil
}

func EncodeResponse(
	writer io.Writer,
	response Response,
) error {
	encoder := json.NewEncoder(writer)

	if err := encoder.Encode(response); err != nil {
		return fmt.Errorf(
			"encode USB trust response: %w",
			err,
		)
	}

	return nil
}

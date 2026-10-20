package usbtrust

type Action string

const (
	ActionStatus            Action = "status"
	ActionAudit             Action = "audit"
	ActionPolicy            Action = "policy"
	ActionKeepBlocked       Action = "keep-blocked"
	ActionProvisionKey      Action = "provision-key"
	ActionAllowOnce         Action = "allow-once"
	ActionTrustPermanent    Action = "trust-permanent"
	ActionForget            Action = "forget"
	ActionAcceptReplacement Action = "accept-replacement"
	ActionEnrollInternal    Action = "enroll-internal"
	ActionDisarm            Action = "disarm"
	// ActionAllowMedia opens the medium that is in an allowed reader now.
	ActionAllowMedia Action = "allow-media"
	// ActionRenew restarts the lapse clock of a trusted device that is about
	// to lapse, without it being connected.
	ActionRenew Action = "renew"
)

func (a Action) Valid() bool {
	switch a {
	case ActionStatus,
		ActionPolicy,
		ActionKeepBlocked,
		ActionProvisionKey,
		ActionAudit,
		ActionAllowOnce,
		ActionTrustPermanent,
		ActionForget,
		ActionAcceptReplacement,
		ActionEnrollInternal,
		ActionDisarm,
		ActionAllowMedia,
		ActionRenew:
		return true
	default:
		return false
	}
}

func (a Action) Mutation() bool {
	switch a {
	case ActionProvisionKey:
		return true
	case ActionAllowOnce,
		ActionKeepBlocked,
		ActionTrustPermanent,
		ActionForget,
		ActionAcceptReplacement,
		ActionEnrollInternal,
		ActionDisarm,
		ActionAllowMedia,
		ActionRenew:
		return true
	default:
		return false
	}
}

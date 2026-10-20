// Package controller composes domain decisions, verified persistence and live
// observations. It is used only by the privileged USB trust daemon.
package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/readmodel"
)

type Controller struct {
	Reader                       readmodel.Reader
	MachineID, ModelID, StateDir string
	Signer                       usbtrust.Signer
	// Apply receives derived policy only; no adapter may create trust records.
	Apply     func(context.Context, []usbtrust.Decision) error
	Commit    func(context.Context, usbtrust.Document) error
	Provision func(context.Context) error
	// ArmOnStart arms enforcement once signed state can be written. The first
	// arming also enrolls every unvalidated ODDC internal device.
	ArmOnStart bool
	// SetImplicitTarget keeps USBGuard's fallback in step with the armed state.
	SetImplicitTarget func(context.Context, usbtrust.Target) error
	// Release allows present devices again after a verified disarm.
	Release func(context.Context) error
	// VerifyRecoveryKey checks the disk encryption passphrase for disarm.
	VerifyRecoveryKey func(context.Context, []byte) error
	// DisarmDelay slows repeated passphrase guesses.
	DisarmDelay time.Duration
	// Media keeps media in allowed USB readers closed until allowed.
	Media interface {
		MediaPolicy() (media []usbtrust.MediaDecision, listed bool)
		AllowMedia(id, connection string) error
		// MountedPorts lists USB devices, by port, with a mounted medium.
		MountedPorts() map[string]bool
	}
	mu               sync.Mutex
	sessions         usbtrust.Sessions
	persistenceReady bool
	implicit         usbtrust.Target
	// bluetooth keeps the last Bluetooth address read per runtime ID. A
	// blocked part loses its driver and its address with it; without the
	// cache it would read as "not checked" and be allowed again.
	bluetooth map[string]string
	// armed mirrors the last verified document; disarmed withdraws ArmOnStart
	// until the daemon restarts.
	armed, disarmed atomic.Bool
	// kept holds the ports the arming in this process kept open; it stays
	// nil when this process did not arm. ready is set once the first
	// Reconcile has run, so the arming decision is known.
	kept  atomic.Pointer[map[string]bool]
	ready atomic.Bool
}

// Ready reports whether the first Reconcile has run. The media gate waits for
// it so it never closes a medium the arming is about to keep.
func (c *Controller) Ready() bool { return c.ready.Load() }

// KeptAtArming lists the USB ports whose mounted storage the arming in this
// process kept open. It is nil after a boot or a broker restart.
func (c *Controller) KeptAtArming() map[string]bool {
	if kept := c.kept.Load(); kept != nil {
		return *kept
	}
	return nil
}

func (c *Controller) EnforcementEnabled() bool {
	if c.Apply == nil {
		return false
	}
	return c.armed.Load() || (c.ArmOnStart && !c.disarmed.Load())
}

// MediaPolicy lists the media in gated USB readers; listed is false while
// the gate has not scanned yet.
func (c *Controller) MediaPolicy() (media []usbtrust.MediaDecision, listed bool) {
	if c.Media == nil {
		return nil, true
	}
	return c.Media.MediaPolicy()
}

func (c *Controller) SetPersistenceReady(ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.persistenceReady = ready
}

func (c *Controller) Status(ctx context.Context) (broker.Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	input, err := c.snapshot(ctx)
	if err != nil {
		return broker.Status{}, err
	}
	s := broker.Status{
		StatePresent:        input.Trusted != nil,
		PermanentTrustReady: c.persistenceReady,
		ExpectedDevices:     len(input.Expected),
		ObservedDevices:     len(input.Observed),
	}
	s.Enforcing = c.EnforcementEnabled()
	s.Armed = c.armed.Load()
	if input.Trusted != nil {
		s.Revision = input.Trusted.Revision
		s.TrustedDevices = len(input.Trusted.Devices)
		s.Devices = append([]usbtrust.Device(nil), input.Trusted.Devices...)
		if input.Lapse.Enabled() {
			s.LapseDays = int(input.Lapse.After / (24 * time.Hour))
			for _, device := range input.Trusted.Devices {
				if lapse, ok := trustLapse(input.Lapse, device); ok {
					s.Lapses = append(s.Lapses, lapse)
				}
			}
		}
	}
	return s, nil
}

// Renewals lists the unconnected trusted devices whose trust lapses soon,
// for the renewal notice.
func (c *Controller) Renewals(ctx context.Context) ([]broker.TrustLapse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.persistenceReady {
		return nil, nil
	}
	input, err := c.snapshot(ctx)
	if err != nil || input.Trusted == nil {
		return nil, err
	}
	// A device connected under live trust renews itself; asking about it
	// would also claim it has not been connected.
	audit, err := usbtrust.Audit(input)
	if err != nil {
		return nil, err
	}
	connected := map[string]bool{}
	for _, finding := range audit.Findings {
		if finding.Code == usbtrust.CodeTrustedExternal {
			connected[finding.TrustedID] = true
		}
	}
	var renewals []broker.TrustLapse
	for _, device := range input.Trusted.Devices {
		if connected[device.ID] || !input.Lapse.Renewable(device) {
			continue
		}
		lapse, _ := trustLapse(input.Lapse, device)
		renewals = append(renewals, lapse)
	}
	return renewals, nil
}

func trustLapse(l usbtrust.Lapse, device usbtrust.Device) (broker.TrustLapse, bool) {
	at, ok := l.LapsesAt(device)
	if !ok {
		return broker.TrustLapse{}, false
	}
	lapse := broker.TrustLapse{TrustedID: device.ID, Name: device.Identity.Name, Lapsed: l.Lapsed(device)}
	if used, valid := usbtrust.LastUsed(device); valid {
		lapse.LastUsed = used.UTC().Format(time.RFC3339)
	}
	if !at.IsZero() {
		lapse.LapsesAt = at.UTC().Format(time.RFC3339)
	}
	return lapse, true
}

func (c *Controller) Audit(ctx context.Context) (usbtrust.AuditResult, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	input, err := c.snapshot(ctx)
	if err != nil {
		return usbtrust.AuditResult{}, 0, err
	}
	result, err := usbtrust.Audit(input)
	return result, revision(input), err
}

func (c *Controller) Policy(ctx context.Context) ([]usbtrust.Decision, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	input, err := c.snapshot(ctx)
	if err != nil {
		return nil, 0, err
	}
	policy, err := c.sessions.Derive(input)
	if err == nil && !c.persistenceReady {
		for i := range policy {
			policy[i].Actions = transientActionsOnly(policy[i].Actions)
		}
	}
	return policy, revision(input), err
}

func (c *Controller) Reconcile(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.ready.Store(true)
	if c.EnforcementEnabled() {
		// USBGuard keeps its fallback across a broker restart and does not
		// restart when only the mode changes, so a new broker may find it
		// still allowing new devices.
		if err := c.syncImplicit(ctx); err != nil {
			return err
		}
	}
	input, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	if c.ArmOnStart && !c.disarmed.Load() && !c.armed.Load() && c.persistenceReady {
		if err := c.arm(ctx, &input); err != nil {
			return fmt.Errorf("arm USB enforcement: %w", err)
		}
	}
	if c.persistenceReady {
		if err := c.enrollPortMatches(ctx, &input); err != nil {
			return fmt.Errorf("store built-in USB parts: %w", err)
		}
		if err := c.learnBluetoothAddresses(ctx, &input); err != nil {
			return fmt.Errorf("store Bluetooth addresses: %w", err)
		}
		if err := c.markLapseStart(ctx, &input); err != nil {
			return fmt.Errorf("store USB trust lapse start: %w", err)
		}
		if err := c.refreshLastUsed(ctx, &input); err != nil {
			return fmt.Errorf("store USB last use: %w", err)
		}
	}
	return c.apply(ctx, input)
}

// enrollPortMatches stores built-in parts that sit exactly on their ODDC
// port and have no stored identity yet. Until the store is ready they are
// allowed for this boot only.
func (c *Controller) enrollPortMatches(ctx context.Context, input *usbtrust.AuditInput) error {
	audit, err := usbtrust.Audit(*input)
	if err != nil {
		return err
	}
	doc, err := c.next(input.Trusted)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	added := false
	for _, finding := range audit.Findings {
		if finding.Code != usbtrust.CodeInternalPortMatch {
			continue
		}
		for _, observed := range input.Observed {
			if observed.RuntimeID != finding.RuntimeID || !hasTopology(observed.Identity) {
				continue
			}
			device, err := newDevice(observed.Identity, now)
			if err != nil {
				return err
			}
			device.Class, device.Role, device.ExpectedByODDC = usbtrust.ClassInternal, finding.Role, true
			if device.Identity.Serial != "" {
				device.Strength = usbtrust.StrengthSerialDescriptorTopology
			}
			doc.Devices = append(doc.Devices, device)
			added = true
		}
	}
	if !added {
		return nil
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	if err := c.commit(ctx, doc); err != nil {
		return err
	}
	input.Trusted = &doc
	return c.sessions.Observe(input.Observed)
}

// learnBluetoothAddresses stores the address of a built-in Bluetooth part
// the first time it is read; it is readable only after the part is allowed.
func (c *Controller) learnBluetoothAddresses(ctx context.Context, input *usbtrust.AuditInput) error {
	if input.Trusted == nil {
		return nil
	}
	doc, err := c.next(input.Trusted)
	if err != nil {
		return err
	}
	changed, err := usbtrust.LearnBluetoothAddresses(*input, &doc)
	if err != nil || !changed {
		return err
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	if err := c.commit(ctx, doc); err != nil {
		return err
	}
	input.Trusted = &doc
	return nil
}

// markLapseStart signs when lapsing trust was switched on, so that records
// last used long ago do not all lapse at once, and drops the mark once it
// is off again.
func (c *Controller) markLapseStart(ctx context.Context, input *usbtrust.AuditInput) error {
	if input.Trusted == nil || input.Lapse.Enabled() == (input.Trusted.LapseSince != "") {
		return nil
	}
	doc, err := c.next(input.Trusted)
	if err != nil {
		return err
	}
	doc.LapseSince = ""
	if input.Lapse.Enabled() {
		doc.LapseSince = input.Lapse.Now.UTC().Format(time.RFC3339)
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	if err := c.commit(ctx, doc); err != nil {
		return err
	}
	input.Trusted = &doc
	input.Lapse = usbtrust.NewLapse(input.Lapse.After, input.Lapse.Now, &doc)
	return nil
}

// refreshLastUsed signs a new lastUsed for trusted external devices that
// are connected, at most once a day each.
func (c *Controller) refreshLastUsed(ctx context.Context, input *usbtrust.AuditInput) error {
	if input.Trusted == nil {
		return nil
	}
	doc, err := c.next(input.Trusted)
	if err != nil {
		return err
	}
	changed, err := usbtrust.RefreshLastUsed(*input, &doc)
	if err != nil || !changed {
		return err
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	if err := c.commit(ctx, doc); err != nil {
		return err
	}
	input.Trusted = &doc
	return nil
}

// rememberBluetooth fills in addresses that cannot be read right now from
// the last reading of the same connection, and forgets connections that
// are gone.
func (c *Controller) rememberBluetooth(observed []usbtrust.ObservedDevice) {
	seen := make(map[string]string, len(c.bluetooth))
	for i := range observed {
		id := observed[i].RuntimeID
		if observed[i].Identity.BluetoothAddress == "" {
			observed[i].Identity.BluetoothAddress = c.bluetooth[id]
		}
		if address := observed[i].Identity.BluetoothAddress; address != "" {
			seen[id] = address
		}
	}
	c.bluetooth = seen
}

// arm signs the armed state. Automatic internal enrollment happens only the
// first time; re-arming after a disarm trusts nothing new.
func (c *Controller) arm(ctx context.Context, input *usbtrust.AuditInput) error {
	doc, err := c.next(input.Trusted)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	state := usbtrust.Enforcement{Armed: true, EnrolledAt: now, ChangedAt: now}
	if doc.Enforcement != nil {
		state.EnrolledAt = doc.Enforcement.EnrolledAt
	} else {
		audit, err := usbtrust.Audit(*input)
		if err != nil {
			return err
		}
		for _, finding := range audit.Findings {
			if finding.Code != usbtrust.CodeInternalUnvalidated {
				continue
			}
			for _, observed := range input.Observed {
				if observed.RuntimeID != finding.RuntimeID || !hasTopology(observed.Identity) {
					continue
				}
				device, err := newDevice(observed.Identity, now)
				if err != nil {
					return err
				}
				device.Class, device.Role, device.ExpectedByODDC = usbtrust.ClassInternal, finding.Role, true
				if device.Identity.Serial != "" {
					device.Strength = usbtrust.StrengthSerialDescriptorTopology
				}
				doc.Devices = append(doc.Devices, device)
			}
		}
	}
	doc.Enforcement = &state
	if err := doc.Validate(); err != nil {
		return err
	}
	if err := c.commit(ctx, doc); err != nil {
		return err
	}
	input.Trusted = &doc
	c.armed.Store(true)
	if err := c.sessions.Observe(input.Observed); err != nil {
		return err
	}
	return c.keepMounted(*input)
}

// keepMounted allows, for this connection only, USB storage whose medium is
// mounted while blocking is switched on, so arming does not pull a filesystem
// away. Only devices with nothing but mass-storage interfaces qualify, and
// only when the owner would be offered allow-once for them anyway.
func (c *Controller) keepMounted(input usbtrust.AuditInput) error {
	if c.Media == nil {
		return nil
	}
	mounted := c.Media.MountedPorts()
	policy, err := c.sessions.Derive(input)
	if err != nil {
		return err
	}
	kept := map[string]bool{}
	for _, decision := range policy {
		if decision.Target != usbtrust.TargetBlock || decision.Identity.Port == "" ||
			!mounted[decision.Identity.Port] || !massStorageOnly(decision.Identity.Interfaces) ||
			!slices.Contains(decision.Actions, usbtrust.ActionAllowOnce) {
			continue
		}
		if err := c.sessions.Choose(decision.RuntimeID, decision.Connection, usbtrust.TargetAllow); err != nil {
			return err
		}
		kept[decision.Identity.Port] = true
	}
	c.kept.Store(&kept)
	return nil
}

// massStorageOnly reports whether every interface is USB mass storage (08).
func massStorageOnly(interfaces []string) bool {
	if len(interfaces) == 0 {
		return false
	}
	for _, iface := range interfaces {
		if !strings.HasPrefix(iface, "08:") {
			return false
		}
	}
	return true
}

// disarm needs the disk encryption passphrase, so a stolen sudo session alone
// cannot switch blocking off.
func (c *Controller) disarm(ctx context.Context, key []byte) (broker.Response, error) {
	input, err := c.snapshot(ctx)
	if err != nil {
		return broker.Response{}, err
	}
	if input.Trusted == nil || input.Trusted.Enforcement == nil || !input.Trusted.Enforcement.Armed {
		return broker.Response{}, fmt.Errorf("USB enforcement is not armed")
	}
	if c.VerifyRecoveryKey == nil {
		return broker.Response{}, fmt.Errorf("disk encryption passphrase verification is not configured")
	}
	if err := c.VerifyRecoveryKey(ctx, key); err != nil {
		select {
		case <-ctx.Done():
		case <-time.After(c.DisarmDelay):
		}
		return broker.Response{}, fmt.Errorf("disk encryption passphrase rejected")
	}
	doc, err := c.next(input.Trusted)
	if err != nil {
		return broker.Response{}, err
	}
	state := *doc.Enforcement
	state.Armed, state.ChangedAt = false, time.Now().UTC().Format(time.RFC3339)
	doc.Enforcement = &state
	if err := c.commit(ctx, doc); err != nil {
		return broker.Response{}, fmt.Errorf("commit USB trust: %w", err)
	}
	c.armed.Store(false)
	c.disarmed.Store(true)
	if err := c.syncImplicit(ctx); err != nil {
		return broker.Response{}, fmt.Errorf("enforcement disarmed; USBGuard fallback: %w", err)
	}
	if c.Release != nil && c.Apply != nil {
		if err := c.Release(ctx); err != nil {
			return broker.Response{}, fmt.Errorf("enforcement disarmed; releasing devices: %w", err)
		}
	}
	return broker.Response{OK: true, Revision: doc.Revision, Message: "USB enforcement disarmed"}, nil
}

func (c *Controller) next(trusted *usbtrust.Document) (usbtrust.Document, error) {
	doc := usbtrust.Document{Schema: usbtrust.SchemaVersion, MachineID: c.MachineID, ODDCModel: c.ModelID}
	if trusted != nil {
		doc = *trusted
		doc.Devices = append([]usbtrust.Device(nil), doc.Devices...)
	}
	if doc.Revision == ^uint64(0) {
		return doc, fmt.Errorf("USB trust revision exhausted")
	}
	doc.Revision++
	return doc, nil
}

func (c *Controller) commit(ctx context.Context, doc usbtrust.Document) error {
	if c.Commit != nil {
		return c.Commit(ctx, doc)
	}
	return usbtrust.CommitSigned(ctx, c.StateDir, doc, c.Signer)
}

func (c *Controller) syncImplicit(ctx context.Context) error {
	if c.SetImplicitTarget == nil {
		return nil
	}
	target := usbtrust.TargetAllow
	if c.EnforcementEnabled() {
		target = usbtrust.TargetBlock
	}
	// Audit mode leaves the configured fallback alone until blocking was on.
	if c.implicit == target || (c.implicit == "" && target == usbtrust.TargetAllow) {
		return nil
	}
	if err := c.SetImplicitTarget(ctx, target); err != nil {
		return err
	}
	c.implicit = target
	return nil
}

func (c *Controller) snapshot(ctx context.Context) (input usbtrust.AuditInput, err error) {
	defer func() {
		if err != nil {
			c.sessions.Reset()
		}
	}()
	if c.Reader.Resolved == nil || c.Reader.Trust == nil || c.Reader.Observations == nil {
		return input, fmt.Errorf("USB trust sources are incomplete")
	}
	input.Expected, err = c.Reader.Expected(ctx)
	if err != nil {
		return input, err
	}
	input.Trusted, err = c.Reader.Trust.Trusted(ctx)
	if err != nil {
		return input, err
	}
	if input.Trusted != nil {
		if err := input.Trusted.Validate(); err != nil {
			return input, err
		}
		if input.Trusted.MachineID != c.MachineID || input.Trusted.ODDCModel != c.ModelID {
			return input, fmt.Errorf("USB trust state belongs to a different machine or ODDC model")
		}
	}
	c.armed.Store(input.Trusted != nil && input.Trusted.Enforcement != nil && input.Trusted.Enforcement.Armed)
	input.Lapse = usbtrust.NewLapse(c.Reader.LapseAfter, time.Now().UTC(), input.Trusted)
	input.Observed, err = c.Reader.Observed(ctx)
	if err != nil {
		return input, err
	}
	c.rememberBluetooth(input.Observed)
	err = c.sessions.Observe(input.Observed)
	return input, err
}

func (c *Controller) Mutate(ctx context.Context, request broker.Request) (broker.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := broker.ValidateRequest(request); err != nil {
		return broker.Response{}, err
	}
	if request.Action == broker.ActionAllowMedia {
		if c.Media == nil {
			return broker.Response{}, fmt.Errorf("the storage media gate is not running")
		}
		if err := c.Media.AllowMedia(request.Media, request.Connection); err != nil {
			return broker.Response{}, err
		}
		return broker.Response{OK: true, Message: "storage medium opened", Enforcing: c.EnforcementEnabled()}, nil
	}
	if request.Action == broker.ActionProvisionKey {
		if c.Provision == nil {
			return broker.Response{}, fmt.Errorf("TPM provisioning is not configured")
		}
		if err := c.Provision(ctx); err != nil {
			return broker.Response{}, err
		}
		c.persistenceReady = true
		return broker.Response{OK: true, Message: "TPM signing key provisioned and verified", Enforcing: c.EnforcementEnabled()}, nil
	}
	if persistentMutation(request.Action) && !c.persistenceReady {
		return broker.Response{}, fmt.Errorf("permanent USB trust is unavailable until the TPM signing key is provisioned and verified")
	}
	if request.Action == broker.ActionDisarm {
		return c.disarm(ctx, []byte(request.RecoveryKey))
	}
	input, err := c.snapshot(ctx)
	if err != nil {
		return broker.Response{}, err
	}
	if request.Action != broker.ActionForget && request.Action != broker.ActionRenew {
		if err := c.sessions.Check(request.RuntimeID, request.Connection); err != nil {
			return broker.Response{}, err
		}
		if err := c.offered(input, request); err != nil {
			return broker.Response{}, err
		}
	}
	if request.Action == broker.ActionAllowOnce || request.Action == broker.ActionKeepBlocked {
		target := usbtrust.TargetAllow
		if request.Action == broker.ActionKeepBlocked {
			target = usbtrust.TargetBlock
		}
		if err := c.sessions.Choose(request.RuntimeID, request.Connection, target); err != nil {
			return broker.Response{}, err
		}
	} else {
		previousPolicy, err := c.sessions.Derive(input)
		if err != nil {
			return broker.Response{}, err
		}
		document, err := c.change(input, request)
		if err != nil {
			return broker.Response{}, err
		}
		if err := c.commit(ctx, document); err != nil {
			return broker.Response{}, fmt.Errorf("commit USB trust: %w", err)
		}
		input.Trusted = &document
		// Revoke choices/reviews for affected identities only. Trusting a disk
		// must not discard an unrelated keyboard's current allow-once grant.
		for _, decision := range previousPolicy {
			if decision.RuntimeID == request.RuntimeID || (request.TrustedID != "" && request.Action != broker.ActionRenew && decision.TrustedID == request.TrustedID) {
				c.sessions.Disconnect(decision.RuntimeID)
			}
		}
		if err := c.sessions.Observe(input.Observed); err != nil {
			return broker.Response{}, err
		}
	}
	if err := c.apply(ctx, input); err != nil {
		return broker.Response{}, fmt.Errorf("decision recorded; enforcement failed: %w", err)
	}
	return broker.Response{OK: true, Revision: revision(input), Message: "USB trust decision recorded", Enforcing: c.EnforcementEnabled()}, nil
}

// offered accepts only the actions the policy lists for this device, so a
// client cannot reach a mutation the review never showed. Keeping a device
// blocked is always allowed.
func (c *Controller) offered(input usbtrust.AuditInput, request broker.Request) error {
	if request.Action == broker.ActionKeepBlocked {
		return nil
	}
	policy, err := c.sessions.Derive(input)
	if err != nil {
		return err
	}
	for _, decision := range policy {
		if decision.RuntimeID != request.RuntimeID {
			continue
		}
		for _, action := range decision.Actions {
			if action == request.Action {
				return nil
			}
		}
		return fmt.Errorf("%s is not offered for this USB device (%s)", request.Action, decision.Reason)
	}
	return fmt.Errorf("USB device is not connected")
}

func persistentMutation(action broker.Action) bool {
	switch action {
	case broker.ActionTrustPermanent,
		broker.ActionForget,
		broker.ActionAcceptReplacement,
		broker.ActionEnrollInternal,
		broker.ActionDisarm,
		broker.ActionRenew:
		return true
	default:
		return false
	}
}

func transientActionsOnly(actions []usbtrust.Action) []usbtrust.Action {
	result := make([]usbtrust.Action, 0, len(actions))
	for _, action := range actions {
		switch action {
		case usbtrust.ActionKeepBlocked, usbtrust.ActionAllowOnce:
			result = append(result, action)
		}
	}
	return result
}

func (c *Controller) apply(ctx context.Context, input usbtrust.AuditInput) error {
	policy, err := c.sessions.Derive(input)
	if err != nil {
		return err
	}
	if err := c.syncImplicit(ctx); err != nil {
		return err
	}
	if c.EnforcementEnabled() {
		return c.Apply(ctx, policy)
	}
	return nil
}

func (c *Controller) change(input usbtrust.AuditInput, request broker.Request) (usbtrust.Document, error) {
	doc, err := c.next(input.Trusted)
	if err != nil {
		return doc, err
	}
	index := -1
	for i, device := range doc.Devices {
		if device.ID == request.TrustedID {
			index = i
		}
	}
	if request.Action == broker.ActionForget {
		if index < 0 {
			return doc, fmt.Errorf("trusted device not found")
		}
		doc.Devices = append(doc.Devices[:index], doc.Devices[index+1:]...)
		return doc, doc.Validate()
	}
	if request.Action == broker.ActionRenew {
		if index < 0 {
			return doc, fmt.Errorf("trusted device not found")
		}
		// Only the warning window: a lapsed device has to be connected and
		// reviewed, so renewing cannot bring back trust the machine already
		// dropped.
		if !input.Lapse.Renewable(doc.Devices[index]) {
			return doc, fmt.Errorf("renewal is offered only in the last days before this device's trust lapses")
		}
		doc.Devices[index].LastUsed = input.Lapse.Now.UTC().Format(time.RFC3339)
		return doc, doc.Validate()
	}
	var observed usbtrust.ObservedDevice
	for _, device := range input.Observed {
		if device.RuntimeID == request.RuntimeID {
			observed = device
		}
	}
	policy, err := c.sessions.Derive(input)
	if err != nil {
		return doc, err
	}
	var decision usbtrust.Decision
	for _, d := range policy {
		if d.RuntimeID == request.RuntimeID {
			decision = d
		}
	}
	// Enrollment/replacement validation uses persistent classification, not a
	// temporary allow/block choice that changes the presented policy reason.
	audit, err := usbtrust.Audit(input)
	if err != nil {
		return doc, err
	}
	for _, finding := range audit.Findings {
		if finding.RuntimeID == request.RuntimeID {
			decision.Reason = string(finding.Code)
		}
	}
	device, err := newDevice(observed.Identity, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return doc, err
	}
	switch request.Action {
	case broker.ActionTrustPermanent:
		if decision.Role != "" || decision.TrustedID != "" || observed.Identity.ConnectType == "hardwired" {
			return doc, fmt.Errorf("use internal enrollment or explicit replacement for this device")
		}
		device.Portable = *request.Portable
	case broker.ActionEnrollInternal:
		if decision.Role != request.Role || decision.Reason != string(usbtrust.CodeInternalUnvalidated) {
			return doc, fmt.Errorf("device is not an unambiguous unvalidated ODDC expectation")
		}
		device.Class, device.Role, device.ExpectedByODDC = usbtrust.ClassInternal, request.Role, true
	case broker.ActionAcceptReplacement:
		if index < 0 {
			return doc, fmt.Errorf("trusted device not found")
		}
		old := doc.Devices[index]
		if old.Class == usbtrust.ClassInternal && (decision.Role != old.Role || decision.TrustedID != old.ID || decision.Reason != string(usbtrust.CodeInternalChanged)) {
			return doc, fmt.Errorf("replacement must be an unambiguous changed identity for the accepted ODDC role")
		}
		if old.Class == usbtrust.ClassExternal && (decision.Role != "" || decision.TrustedID != old.ID || observed.Identity.ConnectType == "hardwired") {
			return doc, fmt.Errorf("external replacement cannot enroll internal hardware")
		}
		device.ID, device.Class, device.Role, device.Portable, device.ExpectedByODDC, device.FirstAccepted = old.ID, old.Class, old.Role, old.Portable, old.ExpectedByODDC, old.FirstAccepted
	default:
		return doc, fmt.Errorf("unsupported USB trust mutation %q", request.Action)
	}
	if !device.Portable && !hasTopology(device.Identity) {
		return doc, fmt.Errorf("non-portable trust requires observed topology")
	}
	if !device.Portable && device.Identity.Serial != "" {
		device.Strength = usbtrust.StrengthSerialDescriptorTopology
	}
	if index >= 0 {
		doc.Devices[index] = device
	} else {
		doc.Devices = append(doc.Devices, device)
	}
	return doc, doc.Validate()
}

func newDevice(identity usbtrust.Identity, now string) (usbtrust.Device, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return usbtrust.Device{}, err
	}
	device := usbtrust.Device{ID: "device:" + hex.EncodeToString(id[:]), Class: usbtrust.ClassExternal, Identity: identity, FirstAccepted: now, LastAccepted: now, LastUsed: now, Strength: usbtrust.StrengthDescriptor}
	if identity.Serial != "" {
		device.Strength = usbtrust.StrengthSerialDescriptor
	}
	return device, nil
}

func hasTopology(identity usbtrust.Identity) bool {
	return identity.ParentHash != "" || identity.Port != ""
}

func revision(input usbtrust.AuditInput) uint64 {
	if input.Trusted != nil {
		return input.Trusted.Revision
	}
	return 0
}

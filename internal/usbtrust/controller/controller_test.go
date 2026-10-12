package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/broker"
	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust/readmodel"
)

type sources struct {
	resolved   map[string]any
	observed   []usbtrust.ObservedDevice
	doc        *usbtrust.Document
	trustError error
}

func (s *sources) Resolved(context.Context) (map[string]any, error) { return s.resolved, nil }
func (s *sources) Observed(context.Context) ([]usbtrust.ObservedDevice, error) {
	return s.observed, nil
}
func (s *sources) Trusted(context.Context) (*usbtrust.Document, error) { return s.doc, s.trustError }

func fixture() (*Controller, *sources) {
	s := &sources{observed: []usbtrust.ObservedDevice{{RuntimeID: "12", Identity: usbtrust.Identity{VIDPID: "1234:5678", Hash: "synthetic", Port: "1-2", Interfaces: []string{"08:06:50"}}}}}
	c := &Controller{Reader: readmodel.Reader{Resolved: s, Observations: s, Trust: s}, MachineID: "machine", ModelID: "model/test"}
	c.Commit = func(_ context.Context, doc usbtrust.Document) error { s.doc = &doc; return nil }
	c.SetPersistenceReady(true)
	return c, s
}

func TestPolicyHidesPersistentActionsUntilSignerReady(t *testing.T) {
	c, _ := fixture()
	c.SetPersistenceReady(false)

	policy, _, err := c.Policy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) != 1 {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	for _, action := range policy[0].Actions {
		switch action {
		case usbtrust.ActionTrustPermanent,
			usbtrust.ActionEnrollInternal,
			usbtrust.ActionAcceptReplacement:
			t.Fatalf("persistent action %q exposed before TPM readiness", action)
		}
	}

	portable := false
	_, err = c.Mutate(context.Background(), broker.Request{
		Action:     broker.ActionTrustPermanent,
		RuntimeID:  policy[0].RuntimeID,
		Connection: policy[0].Connection,
		Portable:   &portable,
	})
	if err == nil {
		t.Fatal("persistent mutation accepted before TPM readiness")
	}
}

func TestProvisionEnablesPersistentActions(t *testing.T) {
	c, _ := fixture()
	c.SetPersistenceReady(false)
	c.Provision = func(context.Context) error { return nil }

	if _, err := c.Mutate(context.Background(), broker.Request{Action: broker.ActionProvisionKey}); err != nil {
		t.Fatal(err)
	}

	status, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.PermanentTrustReady {
		t.Fatal("successful TPM provisioning did not mark permanent trust ready")
	}

	policy, _, err := c.Policy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, action := range policy[0].Actions {
		if action == usbtrust.ActionTrustPermanent {
			found = true
		}
	}
	if !found {
		t.Fatal("permanent trust action remained hidden after TPM provisioning")
	}
}

func TestPermanentTrustForgetAndMachineBinding(t *testing.T) {
	c, s := fixture()
	ctx := context.Background()
	policy, _, err := c.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	portable := false
	r := broker.Request{Action: broker.ActionTrustPermanent, RuntimeID: "12", Connection: policy[0].Connection, Portable: &portable}
	if _, err := c.Mutate(ctx, r); err != nil {
		t.Fatal(err)
	}
	policy, _, err = c.Policy(ctx)
	if err != nil || policy[0].Target != usbtrust.TargetAllow {
		t.Fatalf("%v %v", policy, err)
	}
	c.MachineID = "other"
	if _, _, err := c.Policy(ctx); err == nil {
		t.Fatal("cross-machine trust accepted")
	}
	c.MachineID = "machine"
	if _, err := c.Mutate(ctx, broker.Request{Action: broker.ActionForget, TrustedID: s.doc.Devices[0].ID}); err != nil {
		t.Fatal(err)
	}
	policy, _, _ = c.Policy(ctx)
	if policy[0].Target != usbtrust.TargetBlock {
		t.Fatal("forgotten device still allowed")
	}
}

func TestFailedSigningNeverAppliesApproval(t *testing.T) {
	c, _ := fixture()
	ctx := context.Background()
	policy, _, _ := c.Policy(ctx)
	c.Commit = func(context.Context, usbtrust.Document) error { return errors.New("TPM unavailable") }
	c.Apply = func(context.Context, []usbtrust.Decision) error {
		t.Fatal("failed signing reached enforcement")
		return nil
	}
	portable := true
	if _, err := c.Mutate(ctx, broker.Request{Action: broker.ActionTrustPermanent, RuntimeID: "12", Connection: policy[0].Connection, Portable: &portable}); err == nil {
		t.Fatal("failed signing became success")
	}
}

func TestTransientDisconnectAndCorruptState(t *testing.T) {
	c, s := fixture()
	ctx := context.Background()
	policy, _, _ := c.Policy(ctx)
	r := broker.Request{Action: broker.ActionAllowOnce, RuntimeID: "12", Connection: policy[0].Connection}
	if _, err := c.Mutate(ctx, r); err != nil {
		t.Fatal(err)
	}
	if s.doc != nil {
		t.Fatal("allow once persisted")
	}
	saved := s.observed
	s.observed = nil
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	s.observed = saved
	if _, err := c.Mutate(ctx, r); err == nil {
		t.Fatal("reconnected device accepted stale review")
	}
	s.trustError = errors.New("invalid signature")
	if _, _, err := c.Policy(ctx); err == nil {
		t.Fatal("corrupt state treated as empty")
	}
}

func TestSurplusTrustedIDCannotOverwriteRecord(t *testing.T) {
	c, _ := fixture()
	ctx := context.Background()
	policy, _, _ := c.Policy(ctx)
	portable := true
	_, err := c.Mutate(ctx, broker.Request{Action: broker.ActionTrustPermanent, RuntimeID: "12", Connection: policy[0].Connection, TrustedID: "victim", Portable: &portable})
	if err == nil {
		t.Fatal("surplus trusted ID accepted")
	}
}

func TestPermanentChangePreservesUnrelatedAllowOnce(t *testing.T) {
	c, s := fixture()
	s.observed = append(s.observed, usbtrust.ObservedDevice{RuntimeID: "13", Identity: usbtrust.Identity{VIDPID: "1234:0001", Hash: "keyboard", Port: "1-3", Interfaces: []string{"03:01:01"}}})
	ctx := context.Background()
	policy, _, _ := c.Policy(ctx)
	if _, err := c.Mutate(ctx, broker.Request{Action: broker.ActionAllowOnce, RuntimeID: "13", Connection: policy[1].Connection}); err != nil {
		t.Fatal(err)
	}
	portable := false
	if _, err := c.Mutate(ctx, broker.Request{Action: broker.ActionTrustPermanent, RuntimeID: "12", Connection: policy[0].Connection, Portable: &portable}); err != nil {
		t.Fatal(err)
	}
	policy, _, err := c.Policy(ctx)
	if err != nil || policy[1].Reason != "allowed-for-connection" {
		t.Fatalf("keyboard grant lost: %v %v", policy, err)
	}
}

func TestInternalEnrollmentAndReplacementRequireCurrentODDCRole(t *testing.T) {
	c, s := fixture()
	s.resolved = map[string]any{"hardware": map[string]any{"input": map[string]any{"bus": "usb", "attachment": "internal", "deviceId": "1234:5678"}}}
	ctx := context.Background()
	policy, _, err := c.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if policy[0].Reason != "internal-unvalidated" {
		t.Fatal(policy)
	}
	r := broker.Request{Action: broker.ActionEnrollInternal, RuntimeID: "12", Connection: policy[0].Connection, Role: "hardware.wrong"}
	if _, err := c.Mutate(ctx, r); err == nil {
		t.Fatal("unrelated ODDC role accepted")
	}
	r.Role = "hardware.input"
	if _, err := c.Mutate(ctx, r); err != nil {
		t.Fatal(err)
	}
	accepted := s.doc.Devices[0]
	if accepted.Class != usbtrust.ClassInternal || !accepted.ExpectedByODDC || accepted.Portable {
		t.Fatal(accepted)
	}
	s.observed[0].Identity.Hash = "replacement"
	policy, _, err = c.Policy(ctx)
	if err != nil || policy[0].Target != usbtrust.TargetBlock {
		t.Fatalf("changed internal identity: %v %v", policy, err)
	}
	r = broker.Request{Action: broker.ActionAcceptReplacement, RuntimeID: "12", Connection: policy[0].Connection, TrustedID: accepted.ID}
	if _, err := c.Mutate(ctx, r); err != nil {
		t.Fatal(err)
	}
	if s.doc.Devices[0].ID != accepted.ID || s.doc.Devices[0].Identity.Hash != "replacement" {
		t.Fatal(s.doc)
	}
}

func TestAmbiguousInternalDevicesCannotBecomePermanentExternalTrust(t *testing.T) {
	c, s := fixture()
	s.resolved = map[string]any{"hardware": map[string]any{"input": map[string]any{"bus": "usb", "attachment": "internal", "deviceId": "1234:5678"}}}
	duplicate := s.observed[0]
	duplicate.RuntimeID = "13"
	duplicate.Identity.Port = "1-3"
	s.observed = append(s.observed, duplicate)
	ctx := context.Background()
	policy, _, err := c.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	portable := true
	for _, d := range policy {
		if d.Reason != "internal-ambiguous" || d.Target != usbtrust.TargetBlock {
			t.Fatal(d)
		}
		for _, r := range []broker.Request{
			{Action: broker.ActionTrustPermanent, RuntimeID: d.RuntimeID, Connection: d.Connection, Portable: &portable},
			{Action: broker.ActionEnrollInternal, RuntimeID: d.RuntimeID, Connection: d.Connection, Role: d.Role},
		} {
			if _, err := c.Mutate(ctx, r); err == nil {
				t.Fatalf("ambiguous device accepted: %v", r)
			}
		}
	}
}

func TestMutateRejectsActionsPolicyDidNotOffer(t *testing.T) {
	c, _ := fixture()
	ctx := context.Background()
	policy, _, err := c.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Mutate(ctx, broker.Request{Action: broker.ActionEnrollInternal, RuntimeID: "12", Connection: policy[0].Connection, Role: "hardware.camera.front"})
	if err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("unoffered enrollment: %v", err)
	}
	if _, err := c.Mutate(ctx, broker.Request{Action: broker.ActionKeepBlocked, RuntimeID: "12", Connection: policy[0].Connection}); err != nil {
		t.Fatalf("keep blocked must always work: %v", err)
	}
}

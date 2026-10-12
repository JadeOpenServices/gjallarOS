package usbtrust

import "testing"

func TestConnectionAuthorizationLifecycle(t *testing.T) {
	d := ObservedDevice{RuntimeID: "7", Identity: syntheticDock().Identity}
	s := &Sessions{}
	input := AuditInput{Observed: []ObservedDevice{d}}
	if err := s.Observe(input.Observed); err != nil {
		t.Fatal(err)
	}
	initial, err := s.Derive(input)
	if err != nil || initial[0].Target != TargetBlock {
		t.Fatalf("initial: %v %v", initial, err)
	}
	token := initial[0].Connection
	if err := s.Choose("7", token, TargetAllow); err != nil {
		t.Fatal(err)
	}
	allowed, _ := s.Derive(input)
	if allowed[0].Reason != "allowed-for-connection" {
		t.Fatal(allowed)
	}
	if err := s.Observe(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(input.Observed); err != nil {
		t.Fatal(err)
	}
	if err := s.Choose("7", token, TargetAllow); err == nil {
		t.Fatal("stale approval accepted")
	}
	reconnected, _ := s.Derive(input)
	if reconnected[0].Target != TargetBlock {
		t.Fatal("allow-once survived reconnect")
	}
}

func TestChangedInterfaceInvalidatesReviewAndTransientTrust(t *testing.T) {
	d := ObservedDevice{RuntimeID: "7", Identity: syntheticDock().Identity}
	s := &Sessions{}
	_ = s.Observe([]ObservedDevice{d})
	before, _ := s.Derive(AuditInput{Observed: []ObservedDevice{d}})
	_ = s.Choose("7", before[0].Connection, TargetAllow)
	d.Identity.Interfaces = append(d.Identity.Interfaces, "03:01:01")
	_ = s.Observe([]ObservedDevice{d})
	after, _ := s.Derive(AuditInput{Observed: []ObservedDevice{d}})
	if after[0].Target != TargetBlock || after[0].Connection == before[0].Connection {
		t.Fatal(after)
	}
}

func TestBluetoothAddressKeepsConnectionChoice(t *testing.T) {
	// SYNTHETIC: a dongle allowed once; its address shows up once the
	// Bluetooth driver binds to it.
	d := ObservedDevice{RuntimeID: "7", Identity: syntheticDock().Identity}
	s := &Sessions{}
	_ = s.Observe([]ObservedDevice{d})
	before, _ := s.Derive(AuditInput{Observed: []ObservedDevice{d}})
	if err := s.Choose("7", before[0].Connection, TargetAllow); err != nil {
		t.Fatal(err)
	}
	d.Identity.BluetoothAddress = "02:11:22:33:44:55"
	if err := s.Observe([]ObservedDevice{d}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Derive(AuditInput{Observed: []ObservedDevice{d}})
	if err != nil || after[0].Target != TargetAllow || after[0].Connection != before[0].Connection {
		t.Fatalf("allow-once lost when the address appeared: %+v %v", after, err)
	}
	if after[0].Identity.BluetoothAddress != "02:11:22:33:44:55" {
		t.Fatal("decision dropped the address")
	}
}

func TestDerivedPolicyNeverTrustsDockChildren(t *testing.T) {
	dock := syntheticDock()
	child := ObservedDevice{RuntimeID: "22", Identity: Identity{VIDPID: "2222:0001", Hash: "child", ParentHash: dock.Identity.Hash, Interfaces: []string{"03:01:01"}}}
	input := AuditInput{Trusted: trustedDocument(dock), Observed: []ObservedDevice{{RuntimeID: "20", Identity: dock.Identity}, child}}
	s := &Sessions{}
	_ = s.Observe(input.Observed)
	decisions, err := s.Derive(input)
	if err != nil {
		t.Fatal(err)
	}
	if decisions[0].Target != TargetAllow || decisions[1].Target != TargetBlock {
		t.Fatal(decisions)
	}
	s.Reset()
	if err := s.Check("20", decisions[0].Connection); err == nil {
		t.Fatal("backend reset retained sessions")
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/devgate"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// proof is a startup PIN or recovery key the caller typed for one
// request. The service hands it to gjallar-devproof and keeps only the
// kind.
type proof struct {
	kind   string
	secret []byte
}

// developerNeed reports what the configuration after change asks of the
// developer grant, and whether the grant on disk already covers it. A
// grant that does not load covers nothing.
func (s *service) developerNeed(data []byte, change settings.Change) (devgate.Grant, bool, error) {
	if change.Local != nil {
		return devgate.Grant{}, true, nil
	}
	updated, err := change.Apply(data)
	if err != nil {
		return devgate.Grant{}, false, fmt.Errorf("%w: %v", errRefused, err)
	}
	cfg, err := config.Parse(updated)
	if err != nil {
		return devgate.Grant{}, false, fmt.Errorf("%w: the changed configuration does not load: %v", errRefused, err)
	}
	need := devgate.Needs(cfg.Channel(), cfg.DeveloperMode)
	if need.Empty() {
		return need, true, nil
	}
	return need, s.loadGrant().Covers(need), nil
}

func (s *service) loadGrant() devgate.Grant {
	g, err := devgate.Load(s.grantPath)
	if err != nil {
		log.Printf("%v; it counts as no grant", err)
		return devgate.Grant{}
	}
	return g
}

// touchesDeveloperKeys reports whether change sets a key the grant is
// about.
func touchesDeveloperKeys(change settings.Change) bool {
	for _, key := range change.Keys {
		if key.Key == "releaseChannel" || key.Key == "developerMode" {
			return true
		}
	}
	return false
}

// checkProof runs in keep after the password check, so nobody can spend
// the PIN tries without the admin password. A change that needs a grant
// it does not have takes a proof only when it is critical, which every
// change of releaseChannel or developerMode is.
func (s *service) checkProof(ctx context.Context, change *settings.Change, p *proof) error {
	data, _, err := s.readConfig()
	if err != nil {
		return err
	}
	_, covered, err := s.developerNeed(data, *change)
	if err != nil || covered {
		return err
	}
	if p == nil || change.Tier != config.TierCritical {
		if touchesDeveloperKeys(*change) {
			return fmt.Errorf("%w: the %s needs the startup PIN or the recovery key; gjallarctl settings set asks for it", errRefused, developerWhat(*change))
		}
		return fmt.Errorf("%w: this device runs a developer channel or developer mode without a confirmed grant; run gjallarctl settings confirm-developer first, or switch back with gjallarctl settings set releaseChannel=stable developerMode=false", errRefused)
	}
	if err := s.askDeveloperProof(ctx, p); err != nil {
		return err
	}
	change.Proof = p.kind
	return nil
}

func developerWhat(change settings.Change) string {
	channel, mode := false, false
	for _, key := range change.Keys {
		channel = channel || key.Key == "releaseChannel"
		mode = mode || key.Key == "developerMode"
	}
	switch {
	case channel && mode:
		return "release channel and developer mode"
	case mode:
		return "developer mode"
	}
	return "release channel"
}

// askDeveloperProof has gjallar-devproof test p and turns its answer into
// an error the caller can read. The secret is cleared afterwards.
func (s *service) askDeveloperProof(ctx context.Context, p *proof) error {
	defer clear(p.secret)
	if p.kind != devgate.ProofPIN && p.kind != devgate.ProofRecovery {
		return fmt.Errorf("%w: unknown proof %q; use the startup PIN or the recovery key", errRefused, p.kind)
	}
	reply, err := s.askProof(ctx, p.kind, p.secret)
	switch {
	case err == nil:
		log.Printf("developer proof by %s accepted", p.kind)
		return nil
	case errors.Is(err, devgate.ErrWrong) && p.kind == devgate.ProofRecovery:
		log.Print("developer proof: wrong recovery key")
		return fmt.Errorf("%w: wrong recovery key", errDenied)
	case errors.Is(err, devgate.ErrWrong):
		log.Printf("developer proof: wrong startup PIN, %d tries left", reply.PINLeft)
		if reply.PINLeft == 0 {
			return fmt.Errorf("%w: wrong startup PIN; no tries left until %s, the recovery key still works", errDenied, reply.Retry.In(currentZone()).Format(time.DateTime))
		}
		return fmt.Errorf("%w: wrong startup PIN; %d tries left", errDenied, reply.PINLeft)
	case reply.Error != "":
		log.Printf("developer proof: %v", err)
		return fmt.Errorf("%w: %v", errRefused, err)
	}
	log.Printf("developer proof: %v", err)
	return err
}

// updateGrant fits the grant to the configuration just written: a proof
// widens it to what the configuration needs, and anything the
// configuration no longer asks for is dropped, so turning a setting back
// on needs the proof again. The caller holds s.mu.
func (s *service) updateGrant(need devgate.Grant, proofKind string) error {
	old, err := devgate.Load(s.grantPath)
	if err != nil {
		log.Printf("%v; replacing it", err)
		old = devgate.Grant{}
	}
	g := old
	if proofKind != "" {
		g.Channels = g.Channels || need.Channels
		g.DeveloperMode = g.DeveloperMode || need.DeveloperMode
		g.Proof, g.Time = proofKind, s.now().UTC().Truncate(time.Second)
	}
	g.Channels = g.Channels && need.Channels
	g.DeveloperMode = g.DeveloperMode && need.DeveloperMode
	if g == old && err == nil {
		return nil
	}
	if err := devgate.Save(s.grantPath, g); err != nil {
		return err
	}
	log.Printf("developer grant now: channels %v, developer mode %v", g.Channels, g.DeveloperMode)
	return nil
}

// RequestWithProof is Request with the startup PIN or recovery key for a
// change of releaseChannel or developerMode.
func (s *service) RequestWithProof(ctx context.Context, sender, values, kind string, secret []byte) (settings.Pending, error) {
	defer clear(secret)
	wanted, who, err := s.parseRequest(ctx, sender, values)
	if err != nil {
		return settings.Pending{}, err
	}
	return s.request(ctx, sender, who, wanted, nil, &proof{kind: kind, secret: secret})
}

// ConfirmDeveloper writes the grant for the channel and developer mode the
// configuration already has, for a device set up before the gate. It
// needs the password and the proof, and starts no rebuild.
func (s *service) ConfirmDeveloper(ctx context.Context, sender, kind string, secret []byte) error {
	defer clear(secret)
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return err
	}
	data, _, err := s.readConfig()
	if err != nil {
		return err
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return fmt.Errorf("the current configuration does not load: %w", err)
	}
	need := devgate.Needs(cfg.Channel(), cfg.DeveloperMode)
	if need.Empty() {
		return fmt.Errorf("%w: this device runs the stable channel without developer mode; nothing to confirm", errRefused)
	}
	if s.loadGrant().Covers(need) {
		return nil
	}
	if err := s.sys.Authorize(ctx, sender, "org.gjallaros.settings."+string(config.TierCritical), map[string]string{
		"polkit.message":     "Authentication is required to confirm the developer channel or developer mode of this GjallarOS device",
		"org.gjallaros.keys": "releaseChannel developerMode",
	}); err != nil {
		return err
	}
	p := &proof{kind: kind, secret: secret}
	if err := s.askDeveloperProof(ctx, p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read again under the lock: an Apply may have changed the
	// configuration while the proof was checked.
	data, _, err = s.readConfig()
	if err != nil {
		return err
	}
	if cfg, err = config.Parse(data); err != nil {
		return fmt.Errorf("the current configuration does not load: %w", err)
	}
	if err := s.updateGrant(devgate.Needs(cfg.Channel(), cfg.DeveloperMode), kind); err != nil {
		return err
	}
	log.Printf("uid %d confirmed the developer settings with the %s", who.UID, kind)
	return nil
}

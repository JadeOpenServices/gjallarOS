package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/noctalia"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// maxPendingPerUser bounds what one user can keep root holding.
const maxPendingPerUser = 4

// errRefused is a request root will not take; errDenied is a failed or
// dismissed password check.
var (
	errRefused = errors.New("refused")
	errDenied  = errors.New("not authorized")
)

// caller is who sent a method call, as the bus and logind say.
type caller struct {
	UID     uint32
	Session string
}

// system is everything the service asks the running system. The bus
// implementation is in bus.go; tests use a fake.
type system interface {
	// Caller resolves the unique bus name of a method call.
	Caller(ctx context.Context, sender string) (caller, error)
	// Authorize asks polkit, with the dialog, whether sender may run
	// action. It returns errDenied when not.
	Authorize(ctx context.Context, sender, action string, details map[string]string) error
	// Home reports whether root's network verdict is a proven home now.
	Home() bool
	// Rebuilding reports whether the rebuild unit is still running.
	Rebuilding(ctx context.Context) (bool, error)
	// StartRebuild starts the rebuild unit with the checked colors.
	StartRebuild(ctx context.Context, colors string) error
}

type service struct {
	sys          system
	configPath   string
	checkoutPath string
	checkout     func(string) error
	now          func() time.Time

	mu      sync.Mutex
	pending map[string]settings.Change
}

func newService(sys system, checkoutPath string, checkout func(string) error) *service {
	return &service{
		sys:          sys,
		configPath:   checkoutPath + "/user.config.json",
		checkoutPath: checkoutPath,
		checkout:     checkout,
		now:          time.Now,
		pending:      map[string]settings.Change{},
	}
}

// keyValue is one entry of List.
type keyValue struct {
	Key   string          `json:"key"`
	Tier  config.Tier     `json:"tier"`
	Value json.RawMessage `json:"value,omitempty"`
}

// pendingChange is one entry of Pending.
type pendingChange struct {
	settings.Change
	ReadyAt time.Time `json:"readyAt"`
}

func (s *service) readConfig() ([]byte, map[string]json.RawMessage, error) {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return nil, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", s.configPath, err)
	}
	return data, fields, nil
}

// List returns every key with its tier and current value, sorted by key.
func (s *service) List() ([]keyValue, error) {
	_, fields, err := s.readConfig()
	if err != nil {
		return nil, err
	}
	tiers, err := config.Tiers()
	if err != nil {
		return nil, err
	}
	list := make([]keyValue, 0, len(tiers))
	for key, tier := range tiers {
		list = append(list, keyValue{Key: key, Tier: tier, Value: fields[key]})
	}
	slices.SortFunc(list, func(a, b keyValue) int { return strings.Compare(a.Key, b.Key) })
	return list, nil
}

// Get returns one key's tier and value.
func (s *service) Get(key string) (keyValue, error) {
	tier, err := config.TierOf(key)
	if err != nil {
		return keyValue{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	_, fields, err := s.readConfig()
	if err != nil {
		return keyValue{}, err
	}
	return keyValue{Key: key, Tier: tier, Value: fields[key]}, nil
}

// Request checks a change and keeps it until it is applied, cancelled, or
// its session ends. A critical change needs the password already here.
func (s *service) Request(ctx context.Context, sender, values string) (pendingChange, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return pendingChange{}, err
	}
	if len(values) > settings.MaxRequestBytes {
		return pendingChange{}, fmt.Errorf("%w: the request is larger than %d bytes", errRefused, settings.MaxRequestBytes)
	}
	var wanted map[string]json.RawMessage
	if err := json.Unmarshal([]byte(values), &wanted); err != nil {
		return pendingChange{}, fmt.Errorf("%w: the request is not a JSON object of keys and values", errRefused)
	}
	data, _, err := s.readConfig()
	if err != nil {
		return pendingChange{}, err
	}
	change, err := settings.Plan(data, wanted)
	if err != nil {
		return pendingChange{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	change.UID, change.Session = who.UID, who.Session

	s.mu.Lock()
	count := 0
	for _, other := range s.pending {
		if other.UID == who.UID {
			count++
		}
	}
	s.mu.Unlock()
	if count >= maxPendingPerUser {
		return pendingChange{}, fmt.Errorf("%w: %d changes already wait; apply or cancel one first", errRefused, count)
	}

	if change.Tier == config.TierCritical {
		if err := s.authorize(ctx, sender, change); err != nil {
			return pendingChange{}, err
		}
	}
	change.Asked = s.now()
	s.mu.Lock()
	s.pending[change.ID] = change
	s.mu.Unlock()
	log.Printf("uid %d session %s asked %s change %s: %s", who.UID, who.Session, change.Tier, change.ID, change.KeyNames())
	return pendingChange{Change: change, ReadyAt: change.ReadyAt(s.sys.Home())}, nil
}

// Pending lists the caller's own changes.
func (s *service) Pending(ctx context.Context, sender string) ([]pendingChange, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nil, err
	}
	home := s.sys.Home()
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []pendingChange
	for _, change := range s.pending {
		if change.UID == who.UID && change.Session == who.Session {
			list = append(list, pendingChange{Change: change, ReadyAt: change.ReadyAt(home)})
		}
	}
	slices.SortFunc(list, func(a, b pendingChange) int { return a.Asked.Compare(b.Asked) })
	return list, nil
}

// Cancel drops one of the caller's changes.
func (s *service) Cancel(ctx context.Context, sender, id string) error {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.own(who, id); !ok {
		return fmt.Errorf("%w: no change %s waits in this session", errRefused, id)
	}
	delete(s.pending, id)
	log.Printf("uid %d cancelled change %s", who.UID, id)
	return nil
}

// Apply writes a change once its wait is over and the password is given,
// then starts the rebuild with the caller's checked colors.
func (s *service) Apply(ctx context.Context, sender, id, colors string) error {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return err
	}
	if colors != "" {
		if err := noctalia.CheckColors(colors); err != nil {
			return fmt.Errorf("%w: %v", errRefused, err)
		}
	}
	s.mu.Lock()
	change, ok := s.own(who, id)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: no change %s waits in this session", errRefused, id)
	}
	if ready := change.ReadyAt(s.sys.Home()); s.now().Before(ready) {
		return fmt.Errorf("%w: change %s can be applied from %s", errRefused, id, ready.Format(time.DateTime))
	}
	if running, err := s.sys.Rebuilding(ctx); err != nil {
		return err
	} else if running {
		return fmt.Errorf("%w: a rebuild is running; apply when it has finished", errRefused)
	}
	if err := s.authorize(ctx, sender, change); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.own(who, id); !ok {
		return fmt.Errorf("%w: change %s was cancelled", errRefused, id)
	}
	// Asked again under the lock: another Apply may have started a
	// rebuild while this one waited for the password.
	if running, err := s.sys.Rebuilding(ctx); err != nil {
		return err
	} else if running {
		return fmt.Errorf("%w: a rebuild is running; apply when it has finished", errRefused)
	}
	if err := s.checkout(s.checkoutPath); err != nil {
		return fmt.Errorf("%w: %v; nothing written", errRefused, err)
	}
	data, _, err := s.readConfig()
	if err != nil {
		return err
	}
	updated, err := change.Apply(data)
	if err != nil {
		delete(s.pending, id)
		return fmt.Errorf("%w: %v", errRefused, err)
	}
	if err := config.WriteText(s.configPath, updated); err != nil {
		return err
	}
	delete(s.pending, id)
	log.Printf("uid %d applied %s change %s: %s", who.UID, change.Tier, id, change.KeyNames())
	if err := s.sys.StartRebuild(ctx, colors); err != nil {
		return fmt.Errorf("the change is written, but the rebuild did not start: %w; run rebuild", err)
	}
	return nil
}

// DropSession forgets every change asked from a session that ended.
func (s *service) DropSession(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, change := range s.pending {
		if change.Session == session {
			delete(s.pending, id)
			log.Printf("session %s ended; dropped change %s", session, id)
		}
	}
}

// own returns the change when it belongs to the caller's user and session.
// The caller holds s.mu.
func (s *service) own(who caller, id string) (settings.Change, bool) {
	change, ok := s.pending[id]
	if !ok || change.UID != who.UID || change.Session != who.Session {
		return settings.Change{}, false
	}
	return change, true
}

func (s *service) authorize(ctx context.Context, sender string, change settings.Change) error {
	return s.sys.Authorize(ctx, sender, "org.gjallaros.settings."+string(change.Tier), map[string]string{
		"polkit.message":       change.Message(),
		"org.gjallaros.keys":   change.KeyNames(),
		"org.gjallaros.change": change.ID,
	})
}

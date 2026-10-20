package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/localchanges"
	"github.com/JadeOpenServices/gjallarOS/internal/namecheck"
	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
	"github.com/JadeOpenServices/gjallarOS/internal/noctalia"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

const (
	// maxPendingPerUser bounds what one user can keep root holding.
	maxPendingPerUser = 4
	// nameCheckTimeout bounds the tailnet and network checks of a new
	// hostname.
	nameCheckTimeout = 15 * time.Second
)

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
	// Network returns the device of the current network and whether root's
	// verdict trusts it now.
	Network() (device string, trusted bool)
	// Tailnet lists the tailnet peers; it is empty when Tailscale is not
	// running.
	Tailnet(ctx context.Context) (nettrust.Tailnet, error)
	// NameTaken asks the unprivileged helper who answers for name.local on
	// device. It returns an invalid address when no one does.
	NameTaken(ctx context.Context, device, name string) (netip.Addr, error)
	// Rebuilding reports whether the rebuild unit is still running.
	Rebuilding(ctx context.Context) (bool, error)
	// StartRebuild starts the rebuild unit with the checked colors.
	StartRebuild(ctx context.Context, colors string) error
	// SystemDiskOnUSB says why USB blocking must stay off because of the
	// disk the system runs from; "" when that disk is not on USB.
	SystemDiskOnUSB() string
}

type service struct {
	sys          system
	configPath   string
	checkoutPath string
	checkout     func(string) error
	now          func() time.Time
	// localPath is root's copy of the developer-mode folder, readLocal
	// reads it.
	localPath string
	readLocal func(string) (localchanges.Tree, error)

	// keys signs and checks settings files; device is this machine's
	// app-specific id in them. Without keys, Export and Import refuse.
	keys   *keyStore
	device string

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
		localPath:    localchanges.Path,
		readLocal:    localchanges.ReadRoot,
		pending:      map[string]settings.Change{},
	}
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
func (s *service) List() ([]settings.KeyValue, error) {
	_, fields, err := s.readConfig()
	if err != nil {
		return nil, err
	}
	tiers, err := config.Tiers()
	if err != nil {
		return nil, err
	}
	list := make([]settings.KeyValue, 0, len(tiers))
	for key, tier := range tiers {
		list = append(list, settings.KeyValue{Key: key, Tier: tier, Value: fields[key]})
	}
	slices.SortFunc(list, func(a, b settings.KeyValue) int { return strings.Compare(a.Key, b.Key) })
	return list, nil
}

// Get returns one key's tier and value.
func (s *service) Get(key string) (settings.KeyValue, error) {
	tier, err := config.TierOf(key)
	if err != nil {
		return settings.KeyValue{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	_, fields, err := s.readConfig()
	if err != nil {
		return settings.KeyValue{}, err
	}
	return settings.KeyValue{Key: key, Tier: tier, Value: fields[key]}, nil
}

// Request checks a change and keeps it until it is applied, cancelled, or
// its session ends. A critical change needs the password already here.
func (s *service) Request(ctx context.Context, sender, values string) (settings.Pending, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return settings.Pending{}, err
	}
	if len(values) > settings.MaxRequestBytes {
		return settings.Pending{}, fmt.Errorf("%w: the request is larger than %d bytes", errRefused, settings.MaxRequestBytes)
	}
	var wanted map[string]json.RawMessage
	if err := json.Unmarshal([]byte(values), &wanted); err != nil {
		return settings.Pending{}, fmt.Errorf("%w: the request is not a JSON object of keys and values", errRefused)
	}
	return s.request(ctx, sender, who, wanted, nil)
}

// request plans wanted for who and keeps it as a pending change.
func (s *service) request(ctx context.Context, sender string, who caller, wanted map[string]json.RawMessage, notes []string) (settings.Pending, error) {
	data, _, err := s.readConfig()
	if err != nil {
		return settings.Pending{}, err
	}
	change, err := settings.Plan(data, wanted)
	if err != nil {
		return settings.Pending{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	return s.keep(ctx, sender, who, change, notes)
}

// RequestLocal checks a new developer-mode folder, sent as data by the
// owner's session, against root's copy and keeps the change. Root never
// reads the folder in the user's home itself.
func (s *service) RequestLocal(ctx context.Context, sender string, files map[string][]byte) (settings.Pending, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return settings.Pending{}, err
	}
	if err := s.developerMode(); err != nil {
		return settings.Pending{}, err
	}
	wanted, err := localchanges.Check(files)
	if err != nil {
		return settings.Pending{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	current, err := s.readLocal(s.localPath)
	if err != nil {
		return settings.Pending{}, err
	}
	change, err := settings.PlanLocal(current, wanted)
	if err != nil {
		return settings.Pending{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	return s.keep(ctx, sender, who, change, nil)
}

// developerMode refuses unless the configuration turns developer mode on.
func (s *service) developerMode() error {
	data, _, err := s.readConfig()
	if err != nil {
		return err
	}
	current, err := config.Parse(data)
	if err != nil {
		return fmt.Errorf("the current configuration does not load: %w", err)
	}
	if !current.DeveloperMode {
		return fmt.Errorf("%w: developer mode is off; turn it on with gjallarctl settings set developerMode=true", errRefused)
	}
	return nil
}

// keep stores a planned change for who.
func (s *service) keep(ctx context.Context, sender string, who caller, change settings.Change, notes []string) (settings.Pending, error) {
	change.UID, change.Session = who.UID, who.Session

	s.mu.Lock()
	count := 0
	local := ""
	for _, other := range s.pending {
		if other.UID == who.UID {
			count++
			if other.Local != nil {
				local = other.ID
			}
		}
	}
	s.mu.Unlock()
	if count >= maxPendingPerUser {
		return settings.Pending{}, fmt.Errorf("%w: %d changes already wait; apply or cancel one first", errRefused, count)
	}
	if change.Local != nil && local != "" {
		return settings.Pending{}, fmt.Errorf("%w: folder change %s already waits; apply or cancel it first", errRefused, local[:min(len(local), 8)])
	}

	nameNotes, err := s.checkHostname(ctx, change)
	if err != nil {
		return settings.Pending{}, err
	}
	notes = append(notes, nameNotes...)
	if err := s.checkUSBSystemDisk(change); err != nil {
		return settings.Pending{}, err
	}
	if change.Tier == config.TierCritical {
		if err := s.authorize(ctx, sender, change); err != nil {
			return settings.Pending{}, err
		}
	}
	change.Asked = s.now()
	s.mu.Lock()
	s.pending[change.ID] = change
	s.mu.Unlock()
	log.Printf("uid %d session %s asked %s change %s: %s", who.UID, who.Session, change.Tier, change.ID, change.KeyNames())
	return settings.Pending{Change: change, ReadyAt: change.ReadyAt(s.sys.Home()), Notes: notes}, nil
}

// exportAction asks for the caller's own password: a signed file is one
// this device will take back later.
const exportAction = "org.gjallaros.settings.export"

// Export signs the current settings, as root reads them from the checkout,
// with the caller's key. It never signs bytes the caller sent, so a session
// can only ever get a file of the settings the device already has.
func (s *service) Export(ctx context.Context, sender string) ([]byte, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nil, err
	}
	if s.keys == nil || s.keys.hostKey == nil {
		return nil, errNoSigningKey
	}
	if err := s.sys.Authorize(ctx, sender, exportAction, map[string]string{
		"polkit.message": "Authentication is required to sign and export your GjallarOS settings",
	}); err != nil {
		return nil, err
	}
	_, fields, err := s.readConfig()
	if err != nil {
		return nil, err
	}
	tiers, err := config.Tiers()
	if err != nil {
		return nil, err
	}
	exported := map[string]json.RawMessage{}
	for key, tier := range tiers {
		if value, ok := fields[key]; ok && tier != config.TierInstaller {
			exported[key] = value
		}
	}
	key, name, err := s.keys.signer(who.UID)
	if err != nil {
		return nil, err
	}
	data, err := settings.Seal(key, settings.File{
		Schema:   settings.FileSchema,
		Device:   s.device,
		User:     name,
		UID:      who.UID,
		Created:  s.now().UTC().Truncate(time.Second),
		Settings: exported,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("uid %d exported %d settings", who.UID, len(exported))
	return data, nil
}

// Import checks a settings file and asks for the change it makes, as
// Request does: a valid signature says where the file came from, not that
// the change is wanted now, so the usual tier, wait and password apply.
func (s *service) Import(ctx context.Context, sender string, data []byte) (settings.Pending, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return settings.Pending{}, err
	}
	if s.keys == nil {
		return settings.Pending{}, errNoSigningKey
	}
	trusted, err := s.keys.trusted()
	if err != nil {
		return settings.Pending{}, err
	}
	file, public, err := settings.Open(data, func(key ed25519.PublicKey) bool {
		_, ok := trusted[string(key)]
		return ok
	})
	if err != nil {
		return settings.Pending{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	owner := trusted[string(public)]
	if file.UID != owner.UID || file.User != owner.User || file.Device != s.device {
		return settings.Pending{}, fmt.Errorf("%w: the file's contents do not match the key that signed it", errRefused)
	}
	if who.UID != file.UID {
		return settings.Pending{}, fmt.Errorf("%w: %s made this file; only that account can import it", errRefused, file.User)
	}
	note := fmt.Sprintf("from a file %s made on this device on %s", file.User, file.Created.In(currentZone()).Format("2006-01-02 15:04:05 MST"))
	if days := int(s.now().Sub(file.Created).Hours() / 24); days > 0 {
		note += fmt.Sprintf(", %d days ago", days)
	}
	log.Printf("uid %d imports a file of uid %d made %s", who.UID, file.UID, file.Created.Format(time.RFC3339))
	return s.request(ctx, sender, who, file.Settings, []string{note})
}

// currentZone is the device's time zone as it is now. time.Local is read
// once when the service starts, so after a timezone change it would show
// times in the old zone.
func currentZone() *time.Location {
	data, err := os.ReadFile("/etc/localtime")
	if err != nil {
		return time.Local
	}
	zone, err := time.LoadLocationFromTZData("", data)
	if err != nil {
		return time.Local
	}
	return zone
}

// Pending lists the caller's own changes.
func (s *service) Pending(ctx context.Context, sender string) ([]settings.Pending, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nil, err
	}
	home := s.sys.Home()
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []settings.Pending
	for _, change := range s.pending {
		if change.UID == who.UID && change.Session == who.Session {
			list = append(list, settings.Pending{Change: change, ReadyAt: change.ReadyAt(home)})
		}
	}
	slices.SortFunc(list, func(a, b settings.Pending) int { return a.Asked.Compare(b.Asked) })
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
	// Asked again: the wait gives another machine time to take the name.
	if _, err := s.checkHostname(ctx, change); err != nil {
		return err
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
	if change.Local != nil {
		if err := s.writeLocal(change); err != nil {
			if errors.Is(err, errRefused) {
				delete(s.pending, id)
			}
			return err
		}
	} else if err := s.writeConfig(change); err != nil {
		if errors.Is(err, errRefused) {
			delete(s.pending, id)
		}
		return err
	}
	delete(s.pending, id)
	log.Printf("uid %d applied %s change %s: %s", who.UID, change.Tier, id, change.KeyNames())
	if err := s.sys.StartRebuild(ctx, colors); err != nil {
		return fmt.Errorf("the change is written, but the rebuild did not start: %w; run rebuild", err)
	}
	return nil
}

// writeConfig writes a change of keys into user.config.json. It is
// errRefused when a key no longer holds the value the change was asked
// against. The caller holds s.mu.
func (s *service) writeConfig(change settings.Change) error {
	data, _, err := s.readConfig()
	if err != nil {
		return err
	}
	updated, err := change.Apply(data)
	if err != nil {
		return fmt.Errorf("%w: %v", errRefused, err)
	}
	return config.WriteText(s.configPath, updated)
}

// writeLocal makes the change's folder root's copy. It is errRefused when
// developer mode is off by now or the copy is no longer the one the change
// was asked against. The caller holds s.mu.
func (s *service) writeLocal(change settings.Change) error {
	if err := s.developerMode(); err != nil {
		return err
	}
	current, err := s.readLocal(s.localPath)
	if err != nil {
		return err
	}
	if current.Hash() != change.Local.OldHash {
		return fmt.Errorf("%w: %s changed since this change was asked; ask again", errRefused, s.localPath)
	}
	if err := localchanges.Replace(s.localPath, change.Local.Tree); err != nil {
		return fmt.Errorf("write %s: %w", s.localPath, err)
	}
	written, err := s.readLocal(s.localPath)
	if err != nil {
		return fmt.Errorf("read back %s: %w", s.localPath, err)
	}
	if written.Hash() != change.Local.NewHash {
		return fmt.Errorf("%s does not hold the folder that was asked after writing it", s.localPath)
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

// checkHostname refuses a new hostname that a tailnet peer or a machine on
// the current network already uses, ignoring letter case. The network is
// asked only when root trusts it; the notes say what was not checked. A
// check that fails refuses the change too.
func (s *service) checkHostname(ctx context.Context, change settings.Change) ([]string, error) {
	var name, old string
	found := false
	for _, key := range change.Keys {
		if key.Key == "hostname" {
			found = json.Unmarshal(key.New, &name) == nil
			if key.Old != nil {
				json.Unmarshal(key.Old, &old)
			}
		}
	}
	if !found || strings.EqualFold(name, old) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, nameCheckTimeout)
	defer cancel()

	var notes []string
	tailnet, err := s.sys.Tailnet(ctx)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: could not check the tailnet for hostname %q: %v", errRefused, name, err)
	}
	if peer, ok := namecheck.Clash(tailnet, name); ok {
		return nil, fmt.Errorf("%w: hostname %q is taken on the tailnet by %s; pick another one", errRefused, name, peerName(peer))
	}
	if !tailnet.Running {
		notes = append(notes, "hostname not checked on the tailnet: Tailscale is not running")
	}

	device, trusted := s.sys.Network()
	if !trusted {
		return append(notes, "hostname not checked on this network: it is not trusted"), nil
	}
	addr, err := s.sys.NameTaken(ctx, device, name)
	if err != nil {
		return nil, fmt.Errorf("%w: could not check this network for hostname %q: %v", errRefused, name, err)
	}
	if addr.IsValid() {
		return nil, fmt.Errorf("%w: hostname %q is taken on this network by %s; pick another one", errRefused, name, addr)
	}
	return notes, nil
}

// checkUSBSystemDisk refuses turning USB blocking on while the system runs
// from a USB disk: blocking could cut that disk off (wiki 752 Q11).
func (s *service) checkUSBSystemDisk(change settings.Change) error {
	for _, key := range change.Keys {
		var on bool
		if key.Key != "usbTrustEnforce" || json.Unmarshal(key.New, &on) != nil || !on {
			continue
		}
		if reason := s.sys.SystemDiskOnUSB(); reason != "" {
			return fmt.Errorf("%w: USB blocking stays off: %s", errRefused, reason)
		}
	}
	return nil
}

func peerName(peer nettrust.Peer) string {
	if name := strings.TrimSuffix(peer.DNSName, "."); name != "" {
		return name
	}
	return peer.HostName
}

func (s *service) authorize(ctx context.Context, sender string, change settings.Change) error {
	return s.sys.Authorize(ctx, sender, "org.gjallaros.settings."+string(change.Tier), map[string]string{
		"polkit.message":       change.Message(),
		"org.gjallaros.keys":   change.KeyNames(),
		"org.gjallaros.change": change.ID,
	})
}

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// Pairings (ROADMAP "Pairing and own keys", step 3.4c). A user trusts the
// key of their account on another device after both devices showed the
// same code. Each local user's pairings are one root-only file in pairDir;
// they go when the user goes, as keys do.

const pairDir = "/var/lib/gjallarOS/settings/pairings"

// pairAction asks for the caller's own password: a pairing file and a
// pairing both decide which files this account takes later.
const pairAction = "org.gjallaros.settings.pair"

var errNoOwnKey = fmt.Errorf("%w: this account has no signing key yet; make your own pairing file first with gjallarctl settings pair export FILE", errRefused)

// pairing is another device's account this user trusts.
type pairing struct {
	Key    []byte `json:"key"`
	Device string `json:"device"`
	UID    uint32 `json:"uid"`
	User   string `json:"user"`
	// Hostname is what the other device said it is called; shown only.
	Hostname string    `json:"hostname"`
	Added    time.Time `json:"added"`
}

func (p pairing) side() settings.PairSide {
	return settings.PairSide{Key: p.Key, Device: p.Device, UID: p.UID, User: p.User}
}

type pairRecord struct {
	UID   uint32    `json:"uid"`
	User  string    `json:"user"`
	Pairs []pairing `json:"pairs"`
}

func (k *keyStore) pairPath(uid uint32) string {
	return filepath.Join(k.pairDir, strconv.FormatUint(uint64(uid), 10)+".json")
}

// own returns uid's current public key and account name without making a
// key. A key the host key no longer gives is not current: the other device
// holds a pairing file with the new one, or none.
func (k *keyStore) own(uid uint32) (ed25519.PublicKey, string, error) {
	name, err := k.userName(uid)
	if err != nil {
		return nil, "", fmt.Errorf("look up uid %d: %w", uid, err)
	}
	record, err := k.read(uid)
	if errors.Is(err, os.ErrNotExist) || err == nil && record.User != name {
		return nil, "", errNoOwnKey
	}
	if err != nil {
		return nil, "", err
	}
	if k.hostKey != nil {
		key, err := k.derive(record)
		if err != nil {
			return nil, "", err
		}
		if !bytes.Equal(key.Public().(ed25519.PublicKey), record.Public) {
			return nil, "", errNoOwnKey
		}
	}
	return record.Public, name, nil
}

// pairs returns uid's pairings. A record left by a removed user, or by an
// earlier account on this uid, is deleted and counts for no one.
func (k *keyStore) pairs(uid uint32) ([]pairing, error) {
	data, err := os.ReadFile(k.pairPath(uid))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record pairRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("read %s: %w", k.pairPath(uid), err)
	}
	if record.UID != uid {
		return nil, fmt.Errorf("%s belongs to uid %d", k.pairPath(uid), record.UID)
	}
	name, err := k.userName(uid)
	if errors.Is(err, errNoAccount) || err == nil && name != record.User {
		k.removePairs(record)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return record.Pairs, nil
}

func (k *keyStore) savePairs(uid uint32, name string, pairs []pairing) error {
	if len(pairs) == 0 {
		if err := os.Remove(k.pairPath(uid)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(pairRecord{UID: uid, User: name, Pairs: pairs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(k.pairDir, 0o700); err != nil {
		return err
	}
	return writeRoot(k.pairPath(uid), append(data, '\n'))
}

func (k *keyStore) removePairs(record pairRecord) {
	if err := os.Remove(k.pairPath(record.UID)); err != nil {
		log.Printf("settings files: delete the pairings of removed user %s: %v", record.User, err)
		return
	}
	log.Printf("settings files: deleted the pairings of removed user %s (uid %d)", record.User, record.UID)
}

// sweepPairs reads every pairing record, so those of removed users go.
func (k *keyStore) sweepPairs() {
	entries, err := os.ReadDir(k.pairDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("settings files: %v", err)
		}
		return
	}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		uid, err := strconv.ParseUint(id, 10, 32)
		if !ok || err != nil || !entry.Type().IsRegular() {
			continue
		}
		if _, err := k.pairs(uint32(uid)); err != nil {
			log.Printf("settings files: %v", err)
		}
	}
}

// PairExport signs a pairing file for the caller's account.
func (s *service) PairExport(ctx context.Context, sender string) ([]byte, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nil, err
	}
	if s.keys == nil || s.keys.hostKey == nil {
		return nil, errNoSigningKey
	}
	if err := s.sys.Authorize(ctx, sender, pairAction, map[string]string{
		"polkit.message": "Authentication is required to make a pairing file for your account",
	}); err != nil {
		return nil, err
	}
	_, fields, err := s.readConfig()
	if err != nil {
		return nil, err
	}
	var hostname string
	if err := json.Unmarshal(fields["hostname"], &hostname); err != nil || hostname == "" {
		return nil, fmt.Errorf("%w: read this device's hostname from the settings", errRefused)
	}
	key, name, err := s.keys.signer(who.UID)
	if err != nil {
		return nil, err
	}
	data, err := settings.SealPairing(key, settings.PairFile{
		Schema:   settings.PairSchema,
		Device:   s.device,
		User:     name,
		UID:      who.UID,
		Created:  s.now().UTC().Truncate(time.Second),
		Hostname: hostname,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("uid %d made a pairing file", who.UID)
	return data, nil
}

// pairOffer checks a pairing file from another device for the caller and
// returns what it would add.
func (s *service) pairOffer(ctx context.Context, sender string, data []byte) (caller, string, pairing, settings.PairOffer, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return caller{}, "", pairing{}, settings.PairOffer{}, err
	}
	if s.keys == nil {
		return caller{}, "", pairing{}, settings.PairOffer{}, errNoSigningKey
	}
	file, public, err := settings.OpenPairing(data)
	if err != nil {
		return caller{}, "", pairing{}, settings.PairOffer{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	local, err := s.keys.trusted()
	if err != nil {
		return caller{}, "", pairing{}, settings.PairOffer{}, err
	}
	if _, ok := local[string(public)]; ok {
		return caller{}, "", pairing{}, settings.PairOffer{}, fmt.Errorf("%w: this pairing file was made on this device; use the file of the other device", errRefused)
	}
	// Another key with this device's id: both systems came from one disk
	// image and share /etc/machine-id. Imports tell devices apart by that
	// id, so such a pair cannot work until one of them gets its own.
	if file.Device == s.device {
		return caller{}, "", pairing{}, settings.PairOffer{}, fmt.Errorf("%w: the other device has the same device id as this one; the two systems were copied from one disk image and share /etc/machine-id. Give one of them a new machine id, then make new pairing files", errRefused)
	}
	own, name, err := s.keys.own(who.UID)
	if err != nil {
		return caller{}, "", pairing{}, settings.PairOffer{}, err
	}
	other := pairing{Key: public, Device: file.Device, UID: file.UID, User: file.User, Hostname: file.Hostname}
	offer := settings.PairOffer{
		ID:       settings.PairID(other.side()),
		Code:     settings.PairCode(settings.PairSide{Key: own, Device: s.device, UID: who.UID, User: name}, other.side()),
		Hostname: file.Hostname,
		User:     file.User,
	}
	return who, name, other, offer, nil
}

// PairCheck shows the code of a pairing file and changes nothing.
func (s *service) PairCheck(ctx context.Context, sender string, data []byte) (settings.PairOffer, error) {
	_, _, _, offer, err := s.pairOffer(ctx, sender, data)
	return offer, err
}

// PairAdd trusts the other device's key for the caller once the code the
// user compared, code, is the one these bytes give, and the caller gave
// their password. A pairing of the same account on the same device is
// replaced, so pairing again after a key change works.
func (s *service) PairAdd(ctx context.Context, sender string, data []byte, code string) (settings.PairOffer, error) {
	who, name, other, offer, err := s.pairOffer(ctx, sender, data)
	if err != nil {
		return settings.PairOffer{}, err
	}
	if code != offer.Code {
		return settings.PairOffer{}, fmt.Errorf("%w: the pairing file is not the one whose code was shown; check it again", errRefused)
	}
	if err := s.sys.Authorize(ctx, sender, pairAction, map[string]string{
		"polkit.message": fmt.Sprintf("Authentication is required to trust settings files of %s on the device that says it is %q", other.User, other.Hostname),
	}); err != nil {
		return settings.PairOffer{}, err
	}
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	pairs, err := s.keys.pairs(who.UID)
	if err != nil {
		return settings.PairOffer{}, err
	}
	pairs = slices.DeleteFunc(pairs, func(p pairing) bool {
		return p.Device == other.Device && p.UID == other.UID || bytes.Equal(p.Key, other.Key)
	})
	other.Added = s.now().UTC().Truncate(time.Second)
	if err := s.keys.savePairs(who.UID, name, append(pairs, other)); err != nil {
		return settings.PairOffer{}, err
	}
	log.Printf("uid %d paired with %s (uid %d) on device %s", who.UID, other.User, other.UID, other.Device)
	offer.Added = other.Added
	return offer, nil
}

// PairList returns the caller's pairings.
func (s *service) PairList(ctx context.Context, sender string) ([]settings.PairOffer, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return nil, err
	}
	if s.keys == nil {
		return nil, errNoSigningKey
	}
	pairs, err := s.keys.pairs(who.UID)
	if err != nil {
		return nil, err
	}
	list := []settings.PairOffer{}
	for _, p := range pairs {
		list = append(list, settings.PairOffer{ID: settings.PairID(p.side()), Hostname: p.Hostname, User: p.User, Added: p.Added})
	}
	return list, nil
}

// PairRemove stops trusting one of the caller's pairings. Taking trust
// away needs no password.
func (s *service) PairRemove(ctx context.Context, sender, id string) error {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return err
	}
	if s.keys == nil {
		return errNoSigningKey
	}
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	pairs, err := s.keys.pairs(who.UID)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(pairs), func(p pairing) bool { return settings.PairID(p.side()) == id })
	if len(kept) == len(pairs) {
		return fmt.Errorf("%w: no pairing has id %q; see gjallarctl settings pair list", errRefused, id)
	}
	name, err := s.keys.userName(who.UID)
	if err != nil {
		return err
	}
	if err := s.keys.savePairs(who.UID, name, kept); err != nil {
		return err
	}
	log.Printf("uid %d removed pairing %s", who.UID, id)
	return nil
}

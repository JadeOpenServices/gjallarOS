package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Signing keys for settings files (ROADMAP "Signed settings files"). The
// service holds one 32-byte host key, sealed with systemd-creds (TPM2 when
// there is one) and handed over by systemd as a credential, so the service
// itself never reaches the TPM. Each user's Ed25519 key comes from the host
// key and a random salt in that user's record; without the host key a
// record is only a public key. Records are root-only files in keyDir.

const (
	keyCredential = "gjallar-settings-key"
	keyDir        = "/var/lib/gjallarOS/settings/keys"
	hostKeySize   = 32
	saltSize      = 32
)

// appID makes the device id in settings files app-specific, as
// sd_id128_get_machine_app_specific does, so files never carry the
// machine id itself.
var appID, _ = hex.DecodeString("60643cd9bce37c0ff1c82f5c6f32b196")

var errNoSigningKey = fmt.Errorf("%w: signing keys are not set up on this device (the sealed host key did not load)", errRefused)

// errNoAccount says a uid has no account. Only this, not a failed lookup,
// lets the store delete that uid's key.
var errNoAccount = errors.New("no account has this uid")

// keyRecord is one user's key. Retired holds public keys of earlier host
// keys, so files signed before a lost TPM or a new host key still import.
type keyRecord struct {
	UID     uint32    `json:"uid"`
	User    string    `json:"user"`
	Salt    []byte    `json:"salt"`
	Public  []byte    `json:"public"`
	Created time.Time `json:"created"`
	Retired [][]byte  `json:"retired,omitempty"`
}

type keyOwner struct {
	UID  uint32
	User string
}

type keyStore struct {
	dir     string
	hostKey []byte // nil when the credential did not load
	// userName returns the account name of uid; tests replace it.
	userName func(uid uint32) (string, error)
	now      func() time.Time
}

// loadKeyStore reads the host key systemd passed in. Without one, Export
// refuses and Import still checks files against the stored public keys.
func loadKeyStore() *keyStore {
	store := &keyStore{dir: keyDir, userName: lookupUserName, now: time.Now}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		log.Printf("settings files: no credential directory; export is off")
		return store
	}
	key, err := os.ReadFile(filepath.Join(dir, keyCredential))
	switch {
	case err != nil:
		log.Printf("settings files: %v; export is off", err)
	case len(key) != hostKeySize:
		log.Printf("settings files: the host key has %d bytes, not %d; export is off", len(key), hostKeySize)
	default:
		store.hostKey = key
	}
	return store
}

func lookupUserName(uid uint32) (string, error) {
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	var unknown user.UnknownUserIdError
	if errors.As(err, &unknown) {
		return "", fmt.Errorf("uid %d: %w", uid, errNoAccount)
	}
	if err != nil {
		return "", err
	}
	return account.Username, nil
}

func (k *keyStore) path(uid uint32) string {
	return filepath.Join(k.dir, strconv.FormatUint(uint64(uid), 10)+".json")
}

func (k *keyStore) derive(record keyRecord) (ed25519.PrivateKey, error) {
	info := fmt.Sprintf("gjallaros settings signing key v1\x00%d\x00%s", record.UID, record.User)
	seed, err := hkdf.Key(sha256.New, k.hostKey, record.Salt, info, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// signer returns uid's private key and account name. A user without a
// record, or whose uid now belongs to another name, starts with a fresh
// key; one whose host key changed gets a new key and keeps the old public
// key for checking.
func (k *keyStore) signer(uid uint32) (ed25519.PrivateKey, string, error) {
	if k.hostKey == nil {
		return nil, "", errNoSigningKey
	}
	name, err := k.userName(uid)
	if err != nil {
		return nil, "", fmt.Errorf("look up uid %d: %w", uid, err)
	}
	record, err := k.read(uid)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	fresh := err != nil || record.User != name || record.UID != uid
	if !fresh {
		key, err := k.derive(record)
		if err != nil {
			return nil, "", err
		}
		if bytes.Equal(key.Public().(ed25519.PublicKey), record.Public) {
			return key, name, nil
		}
		log.Printf("settings files: the host key changed; uid %d gets a new signing key", uid)
		record.Retired = append(record.Retired, record.Public)
	} else {
		record = keyRecord{UID: uid, User: name}
	}
	record.Salt = make([]byte, saltSize)
	if _, err := rand.Read(record.Salt); err != nil {
		return nil, "", err
	}
	key, err := k.derive(record)
	if err != nil {
		return nil, "", err
	}
	record.Public = key.Public().(ed25519.PublicKey)
	record.Created = k.now().UTC()
	if err := k.write(record); err != nil {
		return nil, "", err
	}
	log.Printf("settings files: made a signing key for uid %d", uid)
	return key, name, nil
}

// trusted maps every public key of this device's users to its owner. A
// record whose uid no longer has that name belongs to a removed user: it
// counts for no one and is deleted, so the key cannot come back with a
// later account of the same name.
func (k *keyStore) trusted() (map[string]keyOwner, error) {
	entries, err := os.ReadDir(k.dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]keyOwner{}, nil
	}
	if err != nil {
		return nil, err
	}
	keys := map[string]keyOwner{}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		uid, err := strconv.ParseUint(id, 10, 32)
		if !ok || err != nil || !entry.Type().IsRegular() {
			continue
		}
		record, err := k.read(uint32(uid))
		if err != nil {
			log.Printf("settings files: %v", err)
			continue
		}
		name, err := k.userName(record.UID)
		if errors.Is(err, errNoAccount) || err == nil && name != record.User {
			k.remove(record)
			continue
		}
		if err != nil {
			log.Printf("settings files: %v; keeping its key", err)
			continue
		}
		owner := keyOwner{UID: record.UID, User: record.User}
		for _, public := range append([][]byte{record.Public}, record.Retired...) {
			if len(public) == ed25519.PublicKeySize {
				keys[string(public)] = owner
			}
		}
	}
	return keys, nil
}

func (k *keyStore) read(uid uint32) (keyRecord, error) {
	data, err := os.ReadFile(k.path(uid))
	if err != nil {
		return keyRecord{}, err
	}
	var record keyRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return keyRecord{}, fmt.Errorf("read %s: %w", k.path(uid), err)
	}
	if record.UID != uid {
		return keyRecord{}, fmt.Errorf("%s belongs to uid %d", k.path(uid), record.UID)
	}
	return record, nil
}

func (k *keyStore) remove(record keyRecord) {
	if err := os.Remove(k.path(record.UID)); err != nil {
		log.Printf("settings files: delete the key of removed user %s: %v", record.User, err)
		return
	}
	log.Printf("settings files: deleted the key of removed user %s (uid %d)", record.User, record.UID)
}

func (k *keyStore) write(record keyRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(k.dir, 0o700); err != nil {
		return err
	}
	return writeRoot(k.path(record.UID), append(data, '\n'))
}

// deviceID is the app-specific id of this machine: HMAC-SHA256 keyed with
// the machine id over appID, as systemd derives app-specific ids.
func deviceID(machineIDPath string) (string, error) {
	data, err := os.ReadFile(machineIDPath)
	if err != nil {
		return "", err
	}
	machine, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(machine) != 16 {
		return "", fmt.Errorf("%s does not hold a machine id", machineIDPath)
	}
	mac := hmac.New(sha256.New, machine)
	mac.Write(appID)
	return hex.EncodeToString(mac.Sum(nil)[:16]), nil
}

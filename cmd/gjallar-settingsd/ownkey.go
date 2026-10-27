package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/settings"
	"github.com/JadeOpenServices/gjallarOS/internal/sshsig"
)

// Own signing keys (ROADMAP "Pairing and own keys"). The main user may sign
// settings files with their own OpenSSH key, settingsSigningKey, instead of
// the key GjallarOS keeps for them. That key is then the only signer for
// that account: export writes a file for the user to sign, import takes
// only files with a signature of that key, and pairing is off.

var errOwnKey = fmt.Errorf("%w: your settings files are signed with your own OpenSSH key (settingsSigningKey)", errRefused)

// ownKey returns the own key of who's account. Only the account named
// username in the settings can have one.
func (s *service) ownKey(who caller) (sshsig.Key, bool, error) {
	if s.keys == nil {
		return sshsig.Key{}, false, errNoSigningKey
	}
	_, fields, err := s.readConfig()
	if err != nil {
		return sshsig.Key{}, false, err
	}
	var line, owner string
	if raw, ok := fields["settingsSigningKey"]; ok {
		if err := json.Unmarshal(raw, &line); err != nil {
			return sshsig.Key{}, false, fmt.Errorf("read settingsSigningKey from the settings: %w", err)
		}
	}
	if line == "" {
		return sshsig.Key{}, false, nil
	}
	if err := json.Unmarshal(fields["username"], &owner); err != nil {
		return sshsig.Key{}, false, fmt.Errorf("read username from the settings: %w", err)
	}
	name, err := s.keys.userName(who.UID)
	if err != nil {
		return sshsig.Key{}, false, fmt.Errorf("look up uid %d: %w", who.UID, err)
	}
	if name != owner {
		return sshsig.Key{}, false, nil
	}
	key, err := sshsig.ParseKey(line)
	if err != nil {
		return sshsig.Key{}, false, fmt.Errorf("settingsSigningKey in the settings: %w", err)
	}
	return key, true, nil
}

// refuseOwnKey refuses what GjallarOS would sign or trust for an account
// that has its own key.
func (s *service) refuseOwnKey(who caller, what string) error {
	_, own, err := s.ownKey(who)
	if err != nil {
		return err
	}
	if own {
		return fmt.Errorf("%w; %s", errOwnKey, what)
	}
	return nil
}

// ImportSigned checks a settings file signed with the caller's own key and
// asks for the change it makes, as Import does. sig is the file's .sig
// from ssh-keygen -Y sign. A file made on another device brings no
// settings scoped to its device.
func (s *service) ImportSigned(ctx context.Context, sender string, data, sig []byte) (settings.Pending, error) {
	who, err := s.sys.Caller(ctx, sender)
	if err != nil {
		return settings.Pending{}, err
	}
	key, own, err := s.ownKey(who)
	if err != nil {
		return settings.Pending{}, err
	}
	if !own {
		return settings.Pending{}, fmt.Errorf("%w: your account has no own signing key; GjallarOS signs your settings files, import them without a .sig file", errRefused)
	}
	file, err := settings.OpenSSH(data, func(message []byte) error {
		return sshsig.Verify(ctx, key, settings.SSHNamespace, message, sig)
	})
	if errors.Is(err, sshsig.ErrNoKeygen) {
		return settings.Pending{}, err
	}
	if err != nil {
		return settings.Pending{}, fmt.Errorf("%w: %v", errRefused, err)
	}
	var note string
	if file.Device == s.device {
		note = fmt.Sprintf("from a file signed with your own key %s, made on this device on %s", key.Fingerprint(), s.madeOn(file.Created))
	} else {
		left := dropDeviceScoped(file.Settings)
		if len(file.Settings) == 0 {
			return settings.Pending{}, fmt.Errorf("%w: the file holds only settings that belong to its own device", errRefused)
		}
		note = fmt.Sprintf("from a file signed with your own key %s, made on another device on %s", key.Fingerprint(), s.madeOn(file.Created))
		if left > 0 {
			note += fmt.Sprintf("; %d settings of that device were left out", left)
		}
	}
	log.Printf("uid %d imports a file signed with its own key %s, made on device %s %s", who.UID, key.Fingerprint(), file.Device, file.Created.Format(time.RFC3339))
	return s.request(ctx, sender, who, file.Settings, []string{note})
}

// signingKeyNotes says what a change of settingsSigningKey does to export,
// import and pairing.
func signingKeyNotes(change settings.Change) []string {
	for _, key := range change.Keys {
		var line string
		if key.Key != "settingsSigningKey" || json.Unmarshal(key.New, &line) != nil {
			continue
		}
		if line == "" {
			return []string{"Without settingsSigningKey, GjallarOS signs the settings files of the main account again. Files signed with the own key no longer import."}
		}
		return []string{"With settingsSigningKey, only that key signs the settings files of the main account. Export then writes a file to sign with ssh-keygen; files GjallarOS signed and files of paired devices no longer import for that account, and it cannot pair."}
	}
	return nil
}

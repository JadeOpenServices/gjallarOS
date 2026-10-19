// Package settings holds the rules of the root settings service (ROADMAP
// section 14, step 3.3): which keys a change may touch, how long it waits,
// and the check that what gets written is what was asked.
package settings

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
)

const (
	// MaxRequestBytes bounds the values of one request.
	MaxRequestBytes = 64 << 10
	// ReadingDelay is the wait for a security change at home.
	ReadingDelay = 10 * time.Second
	// SecurityDelay is the wait for a security change away from home and
	// for every critical change.
	SecurityDelay = time.Hour

	// BusName, ObjectPath and Interface name the service on the system bus.
	BusName    = "org.gjallaros.Settings1"
	ObjectPath = "/org/gjallaros/Settings1"
	Interface  = "org.gjallaros.Settings1"
	// RebuildUnit is the unit Apply starts.
	RebuildUnit = "gjallar-settings-rebuild.service"
)

// KeyChange is one key with its value before and after, as compact JSON.
// Old is empty when the file does not list the key.
type KeyChange struct {
	Key string          `json:"key"`
	Old json.RawMessage `json:"old,omitempty"`
	New json.RawMessage `json:"new"`
}

// Change is a request root has checked and stores until it is applied or
// cancelled.
type Change struct {
	ID      string      `json:"id"`
	UID     uint32      `json:"uid"`
	Session string      `json:"session"`
	Tier    config.Tier `json:"tier"`
	Keys    []KeyChange `json:"keys"`
	Asked   time.Time   `json:"asked"`
}

// KeyValue is one key of the service's List and Get answers.
type KeyValue struct {
	Key   string          `json:"key"`
	Tier  config.Tier     `json:"tier"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Pending is a change with the earliest time it may be applied, as the
// service's Request and Pending answer it.
type Pending struct {
	Change
	ReadyAt time.Time `json:"readyAt"`
	// Notes say what the service could not check, such as a hostname on a
	// network it does not trust.
	Notes []string `json:"notes,omitempty"`
}

var tierRank = map[config.Tier]int{
	config.TierEveryday: 1,
	config.TierSecurity: 2,
	config.TierCritical: 3,
}

// Plan checks a request against the current configuration text. Every key
// must exist and be changeable after install, every value must fit its key,
// and the whole result must pass the configuration checks. Keys whose value
// does not change are dropped; a request that changes nothing is an error.
// On a JODS-managed device only everyday keys may change.
func Plan(data []byte, values map[string]json.RawMessage) (Change, error) {
	size := 0
	for key, value := range values {
		size += len(key) + len(value)
	}
	if size > MaxRequestBytes {
		return Change{}, fmt.Errorf("the request holds %d bytes; at most %d are allowed", size, MaxRequestBytes)
	}
	current, err := config.Parse(data)
	if err != nil {
		return Change{}, fmt.Errorf("the current configuration does not load: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Change{}, fmt.Errorf("parse user configuration: %w", err)
	}

	change := Change{Tier: config.TierEveryday}
	wanted := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		tier, err := config.TierOf(key)
		if err != nil {
			return Change{}, err
		}
		if tier == config.TierInstaller {
			return Change{}, fmt.Errorf("%s only applies while installing and cannot be changed here", key)
		}
		if current.EndpointManagedDevice && tier != config.TierEveryday {
			return Change{}, fmt.Errorf("%s is a %s setting; JODS manages it on this device", key, tier)
		}
		newValue, err := compact(value)
		if err != nil {
			return Change{}, fmt.Errorf("%s: %w", key, err)
		}
		var oldValue json.RawMessage
		if raw, ok := fields[key]; ok {
			if oldValue, err = compact(raw); err != nil {
				return Change{}, fmt.Errorf("%s: %w", key, err)
			}
		}
		if bytes.Equal(oldValue, newValue) {
			continue
		}
		wanted[key] = newValue
		change.Keys = append(change.Keys, KeyChange{Key: key, Old: oldValue, New: newValue})
		if tightens(key, oldValue, newValue) {
			tier = config.TierEveryday
		}
		if tierRank[tier] > tierRank[change.Tier] {
			change.Tier = tier
		}
	}
	if len(change.Keys) == 0 {
		return Change{}, fmt.Errorf("nothing changes; every value is already set")
	}
	if _, _, err := config.ChangeText(data, wanted); err != nil {
		return Change{}, err
	}
	slices.SortFunc(change.Keys, func(a, b KeyChange) int {
		return strings.Compare(a.Key, b.Key)
	})
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Change{}, err
	}
	change.ID = hex.EncodeToString(id)
	return change, nil
}

// tightens reports whether a change only makes the system stricter, so it
// need not wait out its tier's delay. Only the keys named here qualify; the
// managed-device check still runs on their real tier.
func tightens(key string, oldValue, newValue json.RawMessage) bool {
	switch key {
	case "usbTrustLapseDays":
		// Turning the lapse on or shortening it applies at once; turning it
		// off or lengthening it waits.
		var before, after int
		if json.Unmarshal(newValue, &after) != nil || after <= 0 {
			return false
		}
		if len(oldValue) > 0 && json.Unmarshal(oldValue, &before) != nil {
			return false
		}
		return before <= 0 || after < before
	}
	return false
}

// ReadyAt is the earliest time the change may be applied. home says whether
// root's network verdict is a proven home right now; it shortens the wait of
// a security change, never of a critical one.
func (c Change) ReadyAt(home bool) time.Time {
	switch c.Tier {
	case config.TierEveryday:
		return c.Asked
	case config.TierSecurity:
		if home {
			return c.Asked.Add(ReadingDelay)
		}
		return c.Asked.Add(SecurityDelay)
	default:
		return c.Asked.Add(SecurityDelay)
	}
}

// Apply returns the configuration text with the change written in. It fails
// when a key no longer holds the old value the change was asked against, so
// nothing is written that the person did not see.
func (c Change) Apply(data []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("parse user configuration: %w", err)
	}
	values := make(map[string]json.RawMessage, len(c.Keys))
	for _, key := range c.Keys {
		var now json.RawMessage
		if raw, ok := fields[key.Key]; ok {
			var err error
			if now, err = compact(raw); err != nil {
				return nil, fmt.Errorf("%s: %w", key.Key, err)
			}
		}
		if !bytes.Equal(now, key.Old) {
			return nil, fmt.Errorf("%s changed since this change was asked; ask again", key.Key)
		}
		values[key.Key] = key.New
	}
	updated, _, err := config.ChangeText(data, values)
	return updated, err
}

// MaxShownValue is how much of one value the password dialog shows.
const MaxShownValue = 120

// Message is the text of the password dialog: every key with its old and
// new value. Characters that do not print, including those that turn text
// direction, are shown as escapes, so the dialog cannot be made to show
// something other than what root writes.
func (c Change) Message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Change %d GjallarOS setting(s) (%s):", len(c.Keys), c.Tier)
	for _, key := range c.Keys {
		old := "(not set)"
		if key.Old != nil {
			old = Shown(key.Old, MaxShownValue)
		}
		fmt.Fprintf(&b, "\n%s: %s -> %s", key.Key, old, Shown(key.New, MaxShownValue))
	}
	return b.String()
}

// KeyNames lists the keys of the change, for polkit rules.
func (c Change) KeyNames() string {
	names := make([]string, len(c.Keys))
	for i, key := range c.Keys {
		names[i] = key.Key
	}
	return strings.Join(names, ",")
}

// Shown is a value as a person may see it: characters that do not print
// become escapes, and after limit characters (0 for no limit) it is cut.
func Shown(value json.RawMessage, limit int) string {
	var b strings.Builder
	runes := 0
	for _, r := range string(value) {
		if runes == limit && limit > 0 {
			b.WriteString("...")
			break
		}
		runes++
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteString(strings.Trim(strconv.QuoteRuneToASCII(r), "'"))
	}
	return b.String()
}

func compact(value json.RawMessage) (json.RawMessage, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, value); err != nil {
		return nil, fmt.Errorf("the value is not valid JSON")
	}
	return buf.Bytes(), nil
}

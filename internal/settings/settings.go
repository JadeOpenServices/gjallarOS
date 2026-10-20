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
	"github.com/JadeOpenServices/gjallarOS/internal/localchanges"
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
	// Local is set instead of Keys for a new developer-mode folder.
	Local *LocalChange `json:"local,omitempty"`
	Asked time.Time    `json:"asked"`
}

// LocalChange replaces root's copy of the developer-mode folder.
type LocalChange struct {
	// OldHash is the copy the change was asked against, NewHash the one it
	// writes (localchanges.Tree.Hash).
	OldHash string       `json:"oldHash"`
	NewHash string       `json:"newHash"`
	Files   []FileChange `json:"files"`
	// Tree is the folder root writes. It stays in the service.
	Tree localchanges.Tree `json:"-"`
}

// FileChange is one file of a local change with its SHA-256 before and
// after. Old is empty for a new file, New for a removed one.
type FileChange struct {
	Path string `json:"path"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new,omitempty"`
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
	change.ID, err = newID()
	return change, err
}

// PlanLocal is the change that turns root's copy of the developer-mode
// folder from current into wanted. It is a security change: new local code
// runs as root after the rebuild.
func PlanLocal(current, wanted localchanges.Tree) (Change, error) {
	old := map[string]string{}
	for _, file := range current.Files {
		old[file.Path] = file.SHA256
	}
	local := &LocalChange{OldHash: current.Hash(), NewHash: wanted.Hash(), Tree: wanted}
	for _, file := range wanted.Files {
		if old[file.Path] != file.SHA256 {
			local.Files = append(local.Files, FileChange{Path: file.Path, Old: old[file.Path], New: file.SHA256})
		}
		delete(old, file.Path)
	}
	for path, sum := range old {
		local.Files = append(local.Files, FileChange{Path: path, Old: sum})
	}
	if len(local.Files) == 0 {
		return Change{}, fmt.Errorf("nothing changes; root's copy already holds this folder")
	}
	slices.SortFunc(local.Files, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	id, err := newID()
	return Change{ID: id, Tier: config.TierSecurity, Local: local}, err
}

func newID() (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	return hex.EncodeToString(id), nil
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
	case "developerMode":
		// Turning developer mode off returns to the official system.
		return string(newValue) == "false"
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
	if c.Local != nil {
		return nil, fmt.Errorf("a local change writes the developer-mode folder, not the configuration")
	}
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

const (
	// MaxShownValue is how much of one value the password dialog shows.
	MaxShownValue = 120
	// MaxShownFiles is how many files of a local change it lists.
	MaxShownFiles = 20
)

// Message is the text of the password dialog: every key with its old and
// new value. Characters that do not print, including those that turn text
// direction, are shown as escapes, so the dialog cannot be made to show
// something other than what root writes. A local change lists its files and
// the folder's new hash, which gjallarctl system apply prints too.
func (c Change) Message() string {
	var b strings.Builder
	lines := c.Lines()
	if c.Local != nil {
		fmt.Fprintf(&b, "Change %d file(s) of the developer-mode folder (%s), new hash %s:", len(c.Local.Files), c.Tier, ShortHash(c.Local.NewHash))
		if len(lines) > MaxShownFiles {
			lines = append(lines[:MaxShownFiles], fmt.Sprintf("and %d more", len(lines)-MaxShownFiles))
		}
	} else {
		fmt.Fprintf(&b, "Change %d GjallarOS setting(s) (%s):", len(c.Keys), c.Tier)
	}
	for _, line := range lines {
		fmt.Fprintf(&b, "\n%s", line)
	}
	return b.String()
}

// Lines shows the change one line per value or file that differs.
func (c Change) Lines() []string {
	var lines []string
	for _, key := range c.Keys {
		lines = append(lines, key.Lines()...)
	}
	if c.Local != nil {
		for _, file := range c.Local.Files {
			what := "changed"
			switch {
			case file.Old == "":
				what = "added"
			case file.New == "":
				what = "removed"
			}
			lines = append(lines, fmt.Sprintf("local %s: %s", Shown(json.RawMessage(file.Path), 0), what))
		}
	}
	return lines
}

// ShortHash is the start of a folder hash, as the dialog and the terminal
// show it.
func ShortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// Lines shows a key's change as "path: old -> new", one line per value
// that differs. Objects and lists are followed down to what changed, so a
// long value cut at MaxShownValue cannot hide the part that differs behind
// the same first characters on both sides.
func (k KeyChange) Lines() []string {
	var lines []string
	shownLines(Shown(json.RawMessage(k.Key), 0), k.Old, k.New, &lines)
	return lines
}

func shownLines(path string, old, new json.RawMessage, lines *[]string) {
	if bytes.Equal(old, new) {
		return
	}
	var oldFields, newFields map[string]json.RawMessage
	if json.Unmarshal(old, &oldFields) == nil && json.Unmarshal(new, &newFields) == nil &&
		oldFields != nil && newFields != nil {
		names := make([]string, 0, len(oldFields)+len(newFields))
		for name := range oldFields {
			names = append(names, name)
		}
		for name := range newFields {
			if _, ok := oldFields[name]; !ok {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range names {
			shownLines(path+"."+Shown(json.RawMessage(name), 0), oldFields[name], newFields[name], lines)
		}
		return
	}
	var oldItems, newItems []json.RawMessage
	if json.Unmarshal(old, &oldItems) == nil && json.Unmarshal(new, &newItems) == nil &&
		oldItems != nil && newItems != nil {
		for i := range max(len(oldItems), len(newItems)) {
			var o, n json.RawMessage
			if i < len(oldItems) {
				o = oldItems[i]
			}
			if i < len(newItems) {
				n = newItems[i]
			}
			shownLines(fmt.Sprintf("%s[%d]", path, i), o, n, lines)
		}
		return
	}
	shown := func(v json.RawMessage) string {
		if v == nil {
			return "(not set)"
		}
		return Shown(v, MaxShownValue)
	}
	*lines = append(*lines, fmt.Sprintf("%s: %s -> %s", path, shown(old), shown(new)))
}

// KeyNames lists the keys of the change, for polkit rules.
func (c Change) KeyNames() string {
	names := make([]string, len(c.Keys))
	for i, key := range c.Keys {
		names[i] = key.Key
	}
	if c.Local != nil {
		names = append(names, "local")
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

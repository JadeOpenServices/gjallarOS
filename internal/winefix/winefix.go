// Package winefix holds the fixes gjallarctl wine knows for apps that need
// a library Wine does not bring (Plane 277, wiki 931 step 2). The table is
// part of the build and never fetched: each row names what it does, the
// libraries it brings, and every file it downloads with a pinned https
// address, size and sha256. Rows that are asked for often but not offered
// are listed with the reason.
package winefix

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
)

//go:embed fixes.json
var shipped []byte

// MaxFile bounds one download.
const MaxFile = 256 << 20

// Hosts are the only hosts a row may download from.
var Hosts = []string{"download.microsoft.com", "us.download.nvidia.com"}

// Kinds of fix: a safe fix changes nothing Wine brings; an offered one
// puts a publisher's library in place of Wine's own, so it is offered
// only for an app that asks for that library.
const (
	KindSafe  = "safe"
	KindOffer = "offer"
)

// Actions a fix can take. Any other is refused.
const (
	DoVerb        = "verb"
	DoDLLOverride = "dll_override"
)

// OverrideModes are the dll_override modes Wine knows.
var OverrideModes = []string{"native", "builtin", "native,builtin", "builtin,native", "disabled"}

type Table struct {
	Schema  int       `json:"schema"`
	Source  string    `json:"source"`
	Fixes   []Fix     `json:"fixes"`
	Refused []Refusal `json:"refused"`
}

type Fix struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Kind      string   `json:"kind"`
	Why       string   `json:"why"`
	DLLs      []string `json:"dlls"`
	Actions   []Action `json:"actions"`
	Files     []File   `json:"files"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Action is one step of a fix: a winetricks verb, or one library set to a
// mode.
type Action struct {
	Do   string `json:"do"`
	Verb string `json:"verb,omitempty"`
	DLL  string `json:"dll,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// File is one download of a verb. Its name is the last part of its URL,
// which is where winetricks looks for it in its cache.
type File struct {
	Verb   string `json:"verb"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func (f File) Name() string { return path.Base(f.URL) }

type Refusal struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

var (
	idRe       = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	dllRe      = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,59}\.dll$`)
	overRe     = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,59}$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fileNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)
)

// Load gives the table this build ships.
var Load = sync.OnceValues(func() (*Table, error) { return Parse(shipped) })

// Parse reads and checks a table.
func Parse(data []byte) (*Table, error) {
	var t Table
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("fix table: %w", err)
	}
	if dec.More() {
		return nil, errors.New("fix table: more than one table")
	}
	if err := t.check(); err != nil {
		return nil, fmt.Errorf("fix table: %w", err)
	}
	return &t, nil
}

func (t *Table) check() error {
	if t.Schema != 1 {
		return fmt.Errorf("schema %d is not 1", t.Schema)
	}
	if err := text("source", t.Source); err != nil {
		return err
	}
	ids := map[string]bool{}
	urls := map[string]bool{}
	for i := range t.Fixes {
		f := &t.Fixes[i]
		if !idRe.MatchString(f.ID) || ids[f.ID] {
			return fmt.Errorf("fix %d: id %q is not a new short name", i, f.ID)
		}
		ids[f.ID] = true
		if err := f.check(urls); err != nil {
			return fmt.Errorf("fix %s: %w", f.ID, err)
		}
	}
	for _, f := range t.Fixes {
		for _, c := range f.Conflicts {
			if c == f.ID || !ids[c] {
				return fmt.Errorf("fix %s: conflict %q is not another fix", f.ID, c)
			}
		}
	}
	for _, r := range t.Refused {
		if !idRe.MatchString(r.ID) || ids[r.ID] {
			return fmt.Errorf("refused %q: not a new short name", r.ID)
		}
		ids[r.ID] = true
		if err := text("reason", r.Reason); err != nil {
			return fmt.Errorf("refused %s: %w", r.ID, err)
		}
	}
	return nil
}

func (f *Fix) check(urls map[string]bool) error {
	for name, v := range map[string]string{"title": f.Title, "why": f.Why} {
		if err := text(name, v); err != nil {
			return err
		}
	}
	if f.Kind != KindSafe && f.Kind != KindOffer {
		return fmt.Errorf("kind %q is neither %s nor %s", f.Kind, KindSafe, KindOffer)
	}
	for i, d := range f.DLLs {
		if !dllRe.MatchString(d) || slices.Contains(f.DLLs[:i], d) {
			return fmt.Errorf("library %q is not a new lower-case .dll name", d)
		}
	}
	if len(f.Actions) == 0 {
		return errors.New("no actions")
	}
	verbs := map[string]bool{}
	for _, a := range f.Actions {
		switch a.Do {
		case DoVerb:
			if !idRe.MatchString(a.Verb) || a.DLL != "" || a.Mode != "" || verbs[a.Verb] {
				return fmt.Errorf("verb action %+v is not one new verb", a)
			}
			verbs[a.Verb] = true
		case DoDLLOverride:
			if a.Verb != "" || !overRe.MatchString(a.DLL) || strings.HasSuffix(a.DLL, ".dll") || !slices.Contains(OverrideModes, a.Mode) {
				return fmt.Errorf("dll_override %+v needs a library name without .dll and one of %s", a, strings.Join(OverrideModes, " "))
			}
		default:
			return fmt.Errorf("action %q is not one this version knows", a.Do)
		}
	}
	names := map[string]bool{}
	for _, file := range f.Files {
		if !verbs[file.Verb] {
			return fmt.Errorf("file %s belongs to %q, not a verb of this fix", file.URL, file.Verb)
		}
		if err := checkURL(file.URL); err != nil {
			return err
		}
		if urls[file.URL] {
			return fmt.Errorf("%s is in the table twice", file.URL)
		}
		urls[file.URL] = true
		key := file.Verb + "/" + strings.ToLower(file.Name())
		if !fileNameRe.MatchString(file.Name()) || names[key] {
			return fmt.Errorf("%s: its file name is not plain or not new for %s", file.URL, file.Verb)
		}
		names[key] = true
		if !sha256Re.MatchString(file.SHA256) {
			return fmt.Errorf("%s: sha256 is not 64 lower-case hex digits", file.URL)
		}
		if file.Size <= 0 || file.Size > MaxFile {
			return fmt.Errorf("%s: size %d is not between 1 byte and %d MiB", file.URL, file.Size, MaxFile>>20)
		}
	}
	return nil
}

// checkURL allows a plain https address on one of Hosts.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return err
	case u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Port() != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		!slices.Contains(Hosts, u.Host):
		return fmt.Errorf("%s is not a plain https address on %s", raw, strings.Join(Hosts, " or "))
	case u.Path != path.Clean(u.Path) || !strings.HasPrefix(u.Path, "/") || strings.HasSuffix(u.Path, "/"):
		return fmt.Errorf("%s is not a clean file path", raw)
	}
	return nil
}

// text allows one line of up to 300 printable characters.
func text(name, v string) error {
	if v == "" || len(v) > 300 || strings.TrimSpace(v) != v {
		return fmt.Errorf("%s %q is empty, too long or padded", name, v)
	}
	for _, r := range v {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("%s %q holds a control character", name, v)
		}
	}
	return nil
}

// Find gives the fix or the refusal called id; both are nil for a name the
// table does not know.
func (t *Table) Find(id string) (*Fix, *Refusal) {
	for i := range t.Fixes {
		if t.Fixes[i].ID == id {
			return &t.Fixes[i], nil
		}
	}
	for i := range t.Refused {
		if t.Refused[i].ID == id {
			return nil, &t.Refused[i]
		}
	}
	return nil, nil
}

// DLLName gives a library name as the table writes it: lower case, ending
// in .dll.
func DLLName(name string) string {
	name = strings.ToLower(name)
	if !strings.HasSuffix(name, ".dll") {
		name += ".dll"
	}
	return name
}

// ByDLL gives the fixes that bring a library.
func (t *Table) ByDLL(name string) []*Fix {
	name = DLLName(name)
	var out []*Fix
	for i := range t.Fixes {
		if slices.Contains(t.Fixes[i].DLLs, name) {
			out = append(out, &t.Fixes[i])
		}
	}
	return out
}

// Size gives the sum of a fix's downloads.
func (f *Fix) Size() (n int64) {
	for _, file := range f.Files {
		n += file.Size
	}
	return n
}

// CheckWinetricks checks the table against the winetricks script that
// runs its verbs: the body of each verb must download exactly the files
// the table pins for it, each by a plain "w_download URL SHA256" line, and
// must not call other verbs, helpers or downloads the table cannot pin.
func CheckWinetricks(script []byte, t *Table) error {
	var errs []error
	for _, f := range t.Fixes {
		for _, a := range f.Actions {
			if a.Do != DoVerb {
				continue
			}
			if err := checkVerb(script, a.Verb, f.Files); err != nil {
				errs = append(errs, fmt.Errorf("fix %s: %w", f.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// notPinned are words in a verb body that run something the table does not
// pin, or that winetricks refuses for a 64-bit setup.
var notPinned = []string{"w_call", "helper_", "w_download_to", "w_download_manual", "w_package_unsupported_win64", "w_package_broken_win64"}

func checkVerb(script []byte, verb string, files []File) error {
	body, err := verbBody(script, verb)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, file := range files {
		if file.Verb == verb {
			want[file.URL+" "+file.SHA256] = true
		}
	}
	got := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, w := range notPinned {
			if strings.Contains(line, w) {
				return fmt.Errorf("verb %s uses %s: %q", verb, w, line)
			}
		}
		fields := strings.Fields(line)
		if !slices.Contains(fields, "w_download") {
			continue
		}
		if len(fields) != 3 || fields[0] != "w_download" || strings.ContainsAny(line, "$`\"'") {
			return fmt.Errorf("verb %s downloads in a way the table cannot pin: %q", verb, line)
		}
		got[fields[1]+" "+fields[2]] = true
	}
	for k := range want {
		if !got[k] {
			return fmt.Errorf("verb %s does not download %s", verb, k)
		}
	}
	for k := range got {
		if !want[k] {
			return fmt.Errorf("verb %s downloads %s, which the table does not pin", verb, k)
		}
	}
	return nil
}

// verbBody gives the lines of "load_VERB()" up to its closing brace; the
// verb must be defined once.
func verbBody(script []byte, verb string) (string, error) {
	s := string(script)
	head := "\nload_" + verb + "()\n{\n"
	at := strings.Index(s, head)
	if at < 0 || strings.Count(s, head) != 1 {
		return "", fmt.Errorf("verb %s is not defined once in winetricks", verb)
	}
	rest := s[at+len(head):]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		return "", fmt.Errorf("verb %s has no end in winetricks", verb)
	}
	return rest[:end], nil
}

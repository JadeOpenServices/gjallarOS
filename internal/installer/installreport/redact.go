package installreport

import (
	"net"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Known holds values the installer knows belong to this user or machine.
// Each is replaced wherever it shows up as a whole word in the error text.
type Known struct {
	User, Name, Host, Email, GitHub string
	// LiveHost and LiveUser are the running system's host and login names,
	// which differ from the new ones during a first install.
	LiveHost, LiveUser string
	// Serials are disk serial numbers and WWIDs.
	Serials []string
}

// Redaction is a second layer. The report's first protection is that it
// only carries allowlisted fields and the installer never prints secrets;
// these rules catch personal values that still reach an error message.
// Pattern scrubbers always miss something, so the user reads the exact
// text before anything is saved.

// genericWords identify nobody. A user or host called like this would
// otherwise blank out half of every error ("nixos-rebuild", "/nix/store").
var genericWords = map[string]bool{
	"nix": true, "nixos": true, "root": true, "admin": true, "user": true,
	"users": true, "home": true, "store": true, "system": true, "boot": true,
	"linux": true, "gjallar": true, "gjallaros": true, "install": true,
	"installer": true, "test": true, "localhost": true, "default": true,
}

var (
	urlUserinfo  = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`)
	secretAssign = regexp.MustCompile(`(?i)\b(password|passphrase|passwd|psk|token|secret)(\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)`)
	keyAssign    = regexp.MustCompile(`(?i)\b(pin|key)(\s*=\s*)("[^"]*"|'[^']*'|\S+)`)
	homePath     = regexp.MustCompile(`/home/[^/\s'"]+`)
	diskByPath   = regexp.MustCompile(`/dev/disk/by-(id|uuid|partuuid|label|partlabel|path)/[^\s'"]+`)
	emailAddress = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	uuidPattern  = regexp.MustCompile(`\b[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\b`)
	vfatSerial   = regexp.MustCompile(`\b[0-9A-F]{4}-[0-9A-F]{4}\b`)
	macPattern   = regexp.MustCompile(`\b[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}\b`)
	ipToken      = regexp.MustCompile(`[0-9A-Za-z:.%]*[:.][0-9A-Za-z:.%]*`)
	storePath    = regexp.MustCompile(`/nix/store/[^\s'"]+`)
	opaqueRun    = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)
	gitID        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sriHash      = regexp.MustCompile(`^sha(1|256|384|512)-`)
)

// Redact returns text with secrets and personal values replaced.
func Redact(text string, known Known) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r), !unicode.IsPrint(r) && r != ' ':
			return -1
		}
		return r
	}, text)

	text = urlUserinfo.ReplaceAllString(text, "${1}<redacted>@")
	text = secretAssign.ReplaceAllString(text, "${1}${2}<redacted>")
	text = keyAssign.ReplaceAllString(text, "${1}${2}<redacted>")
	text = replaceKnown(text, known)
	text = homePath.ReplaceAllString(text, "/home/<user>")
	text = diskByPath.ReplaceAllString(text, "/dev/disk/by-${1}/<redacted>")
	text = emailAddress.ReplaceAllString(text, "<email>")
	text = uuidPattern.ReplaceAllString(text, "<uuid>")
	text = vfatSerial.ReplaceAllString(text, "<uuid>")
	text = macPattern.ReplaceAllString(text, "<mac>")
	text = outsideStore(text, ipAddresses)
	text = outsideStore(text, opaqueValues)
	return text
}

type knownValue struct{ value, label string }

func replaceKnown(text string, known Known) string {
	values := []knownValue{
		{known.Email, "<email>"},
		{known.Name, "<name>"},
		{known.GitHub, "<github-user>"},
		{known.Host, "<host>"},
		{known.User, "<user>"},
		{known.LiveHost, "<host>"},
		{known.LiveUser, "<user>"},
	}
	for _, serial := range known.Serials {
		values = append(values, knownValue{serial, "<serial>"})
	}
	// Longest first, so a full name goes before the user name inside it.
	sort.SliceStable(values, func(i, j int) bool { return len(values[i].value) > len(values[j].value) })
	for _, v := range values {
		value := strings.TrimSpace(v.value)
		if len(value) < 3 || genericWords[strings.ToLower(value)] {
			continue
		}
		text = replaceWord(text, value, v.label)
		// A full name also shows up word by word.
		if v.label == "<name>" {
			for _, part := range strings.Fields(value) {
				if len(part) >= 3 && !genericWords[strings.ToLower(part)] {
					text = replaceWord(text, part, v.label)
				}
			}
		}
	}
	return text
}

// replaceWord replaces value, ignoring case, where it is not part of a
// longer word. Nix store paths count too: a host name shows up in
// nixos-system-HOST-26.05.
func replaceWord(text, value, label string) string {
	lower := strings.ToLower(text)
	needle := strings.ToLower(value)
	if len(lower) != len(text) {
		// Case folding changed byte lengths; fall back to exact matches.
		lower, needle = text, value
	}
	var b strings.Builder
	last := 0
	for start := 0; ; {
		i := strings.Index(lower[start:], needle)
		if i < 0 {
			break
		}
		i += start
		end := i + len(needle)
		if wordEdge(text, i-1) && wordEdge(text, end) {
			b.WriteString(text[last:i])
			b.WriteString(label)
			last = end
		}
		start = i + 1
		if start < last {
			start = last
		}
	}
	b.WriteString(text[last:])
	return b.String()
}

func wordEdge(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return true
	}
	c := text[i]
	return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80)
}

// outsideStore applies rule to the text between nix store paths. Store
// paths hold version numbers that look like addresses and hashes that look
// like tokens; both are public.
func outsideStore(text string, rule func(string) string) string {
	var b strings.Builder
	last := 0
	for _, span := range storePath.FindAllStringIndex(text, -1) {
		b.WriteString(rule(text[last:span[0]]))
		b.WriteString(text[span[0]:span[1]])
		last = span[1]
	}
	b.WriteString(rule(text[last:]))
	return b.String()
}

// ipAddresses replaces IPv4 and IPv6 addresses, ports kept. Loopback and
// unspecified addresses stay: they say nothing about the network.
func ipAddresses(text string) string {
	return ipToken.ReplaceAllStringFunc(text, func(token string) string {
		trimmed := strings.TrimRight(token, ".:")
		rest := token[len(trimmed):]
		if isAddress(trimmed) {
			return "<ip>" + rest
		}
		// address:port
		if i := strings.LastIndex(trimmed, ":"); i > 0 && strings.Count(trimmed, ":") == 1 && isAddress(trimmed[:i]) {
			return "<ip>" + trimmed[i:] + rest
		}
		return token
	})
}

func isAddress(token string) bool {
	host := token
	if i := strings.Index(host, "%"); i > 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return false
	}
	// IPv4 only in dotted form; IPv6 needs at least two colons.
	return strings.Count(host, ".") == 3 || strings.Count(host, ":") >= 2
}

// opaqueValues replaces long random-looking runs: keys, tokens, machine
// IDs. Git commit IDs and SRI content hashes stay; they are public.
func opaqueValues(text string) string {
	return opaqueRun.ReplaceAllStringFunc(text, func(run string) string {
		if gitID.MatchString(run) || sriHash.MatchString(run) {
			return run
		}
		if strings.Contains(run, "/") {
			// A path: only redact when it looks like base64, not like
			// words joined by slashes.
			if strings.HasPrefix(run, "/") || !mixed(run) {
				return opaqueSegments(run)
			}
			return "<redacted>"
		}
		if wordy(run) {
			return run
		}
		return "<redacted>"
	})
}

func opaqueSegments(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if len(part) >= 32 && !gitID.MatchString(part) && !wordy(part) {
			parts[i] = "<redacted>"
		}
	}
	return strings.Join(parts, "/")
}

// wordy is true for names built from words: lower case letters and digits
// split by - _ or . at least every 24 characters, like
// "linux-firmware-20261010-zstd". Random values have no such breaks.
func wordy(run string) bool {
	if strings.ContainsAny(run, "+/=") {
		return false
	}
	length := 0
	breaks := 0
	for _, r := range run {
		switch {
		case r == '-' || r == '_' || r == '.':
			breaks++
			length = 0
		case r >= 'A' && r <= 'Z':
			return false
		default:
			length++
			if length > 24 {
				return false
			}
		}
	}
	return breaks > 0
}

// mixed is true when run has upper case, lower case and digits, as
// base64 has.
func mixed(run string) bool {
	var upper, lower, digit bool
	for _, r := range run {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

// ErrorText redacts the installer's error and caps it.
func ErrorText(raw string, known Known) string {
	return cutError(Redact(raw, known))
}

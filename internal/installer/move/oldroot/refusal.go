// Package oldroot checks and opens the old gjallarOS disk for the disk move
// (345, CP2). The old disk is never written: it is set read-only in the
// kernel first, its LUKS header is checked by this code before cryptsetup
// reads it (L60), it is opened read-only with external tokens off, and its
// top level is mounted read-only with nothing replayed.
package oldroot

import "errors"

// Reason names why the old disk was refused. The names are the choices of
// the move.header.refused and move.layout.refused anchors.
type Reason string

const (
	// Header (GJA-2104).
	Reencrypt    Reason = "reencrypt"
	UnknownToken Reason = "unknown-token"
	KDFMemory    Reason = "kdf-memory"
	NotLUKS2     Reason = "not-luks2"
	Unsupported  Reason = "unsupported"
	// Layout (GJA-2105).
	NotBtrfs   Reason = "not-btrfs"
	NoEvidence Reason = "no-evidence"
	Managed    Reason = "managed"
	// Others.
	WrongKey      Reason = "wrong-key"
	OldCryptsetup Reason = "old-cryptsetup"
	Busy          Reason = "busy"
)

// texts are what the screen shows. They say what to do; the detail stays in
// Refusal.Detail for the install log on the stick, never in the report.
var texts = map[Reason]string{
	Reencrypt:     "The old disk is in the middle of a change of its encryption. Start the old computer once so it can finish, then try again.",
	UnknownToken:  "The old disk's encryption header holds an unlock method gjallarOS never sets up. Someone or something else changed it, so the move stops here.",
	KDFMemory:     "Unlocking the old disk would need more memory than this computer has free. Move it from a computer with more memory.",
	NotLUKS2:      "This is not a gjallarOS disk, or its encryption header is damaged.",
	Unsupported:   "The old disk is encrypted in a way gjallarOS does not set up, so the move cannot open it.",
	NotBtrfs:      "The old disk opened, but it does not hold a gjallarOS system.",
	NoEvidence:    "The old disk does not hold a complete gjallarOS with the current layout.",
	Managed:       "This computer is managed by your organisation. Ask your administrator to move it.",
	WrongKey:      "That recovery key or disk password did not open the old disk.",
	OldCryptsetup: "This stick's disk tools are too old to open the old disk safely. Make a new stick.",
	Busy:          "The old disk is already open. Restart from the stick and try again.",
}

// Refusal stops the move with a text for the user. It is not a failure of
// the stick: the disk is what it is.
type Refusal struct {
	Reason Reason `json:"reason"`
	Text   string `json:"text"`
	// Detail says what exactly was found, for the log on the stick.
	Detail string `json:"detail,omitempty"`
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return r.Text
	}
	return r.Text + " (" + r.Detail + ")"
}

func refuse(reason Reason, detail string) *Refusal {
	return &Refusal{Reason: reason, Text: texts[reason], Detail: detail}
}

// AsRefusal returns the refusal inside err, if there is one.
func AsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	ok := errors.As(err, &r)
	return r, ok
}
